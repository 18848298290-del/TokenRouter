package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/lib/pq"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// Passkey credentials join their account to a retained WebAuthn user handle
// and serialized credential data. Handle creation uses insert-on-conflict
// followed by a read, so concurrent registration attempts for one account
// return the first stored handle instead of replacing its identity.
//
// Credential lookups join the handle row before JSON decoding, and account
// mutations restrict both owner and credential identifiers. Creation copies
// the supplied user-handle bytes while retaining referenced SDK credential
// data in the returned value. List reads close rows on every exit and keep an
// earlier scan or iteration failure ahead of a later close failure; a close
// failure after successful reading still makes the result an error.

// passkeyRepository persists account handles and WebAuthn credentials through
// the retained SQL pool.
type passkeyRepository struct {
	db *sql.DB // Passkey credential database pool
}

// passkeyScanner abstracts row and rows column decoding for stored credential
// records.
type passkeyScanner interface {
	// Scan decodes the selected credential and handle columns into the supplied
	// destinations. SQL scan errors pass to the repository caller before
	// serialized WebAuthn credential JSON is interpreted.
	Scan(dest ...any) error
}

// NewPasskeyRepository retains the supplied SQL pool for account credential
// operations. The caller owns pool lifetime and keeps it open through handle,
// lookup, and mutation requests.
func NewPasskeyRepository(db *sql.DB) identity.PasskeyRepository {
	return &passkeyRepository{db: db}
}

// EnsureUserHandle retains the first account handle and returns its stored
// bytes. Candidate handles require sixteen through sixty-four bytes;
// concurrent insert conflict leaves the earlier account handle selected.
//
// The insert precedes a separate GetUserHandle read, so a read failure can
// follow a completed handle reservation. The pool owns both operations rather
// than an injected Ent transaction. This stable per-account value is reused by
// registration and credential lookup even when a later caller supplies a
// different candidate.
func (r *passkeyRepository) EnsureUserHandle(
	ctx context.Context,
	userID int64,
	candidate []byte,
) ([]byte, error) {
	if len(candidate) < 16 || len(candidate) > 64 {
		return nil, fmt.Errorf("passkey user handle must contain 16-64 bytes")
	}
	// The insert conflict keeps the winning account handle unchanged, and the
	// following read retrieves that stored value. Concurrent registration
	// candidates therefore do not replace an existing handle.
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO passkey_user_handles (user_id, user_handle)
		VALUES ($1, $2)
		ON CONFLICT (user_id) DO NOTHING
	`, userID, candidate); err != nil {
		return nil, fmt.Errorf("ensure passkey user handle: %w", err)
	}
	return r.GetUserHandle(ctx, userID)
}

// GetUserHandle reads the retained account handle into a returned byte slice.
// Missing rows return ErrPasskeyNotFound; other SQL failures retain operation
// context for the credential caller.
func (r *passkeyRepository) GetUserHandle(ctx context.Context, userID int64) ([]byte, error) {
	var handle []byte
	err := r.db.QueryRowContext(ctx, `
		SELECT user_handle
		FROM passkey_user_handles
		WHERE user_id = $1
	`, userID).Scan(&handle)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, identity.ErrPasskeyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get passkey user handle: %w", err)
	}
	return handle, nil
}

// GetByCredentialID reads one serialized credential joined to its retained
// user handle. Missing credential or handle join results become
// ErrPasskeyNotFound, and malformed credential JSON remains a lookup error.
func (r *passkeyRepository) GetByCredentialID(
	ctx context.Context,
	credentialID []byte,
) (*identity.PasskeyCredentialRecord, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT c.id, c.user_id, h.user_handle, c.name, c.credential_data,
		       c.last_used_at, c.created_at, c.updated_at
		FROM passkey_credentials c
		JOIN passkey_user_handles h ON h.user_id = c.user_id
		WHERE c.credential_id = $1
	`, credentialID)
	record, err := scanPasskeyCredential(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, identity.ErrPasskeyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get passkey credential: %w", err)
	}
	return record, nil
}

// ListByUserID returns the account credential records ordered by creation time
// and ID descending. Empty results use an allocated slice, and every exit
// closes the SQL rows.
//
// Scan or iteration failures take precedence over a later deferred close
// failure. If reading succeeds and closing fails, the returned error reports
// cleanup failure alongside any collected records, so callers inspect the
// error before using that batch. The named return error lets deferred cleanup
// select that result after the main read path returns.
func (r *passkeyRepository) ListByUserID(
	ctx context.Context,
	userID int64,
) (records []identity.PasskeyCredentialRecord, retErr error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT c.id, c.user_id, h.user_handle, c.name, c.credential_data,
		       c.last_used_at, c.created_at, c.updated_at
		FROM passkey_credentials c
		JOIN passkey_user_handles h ON h.user_id = c.user_id
		WHERE c.user_id = $1
		ORDER BY c.created_at DESC, c.id DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list passkey credentials: %w", err)
	}
	// Close every opened result after reading or an early exit. Preserve the
	// first scan or iteration failure, but return cleanup failure when reading
	// otherwise completed successfully.
	defer func() {
		if closeErr := rows.Close(); retErr == nil && closeErr != nil {
			retErr = fmt.Errorf("close passkey credential rows: %w", closeErr)
		}
	}()

	records = make([]identity.PasskeyCredentialRecord, 0)
	for rows.Next() {
		record, scanErr := scanPasskeyCredential(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan passkey credential: %w", scanErr)
		}
		records = append(records, *record)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("list passkey credentials: %w", err)
	}
	return records, nil
}

// Create stores a credential with a nonempty credential ID and returns its row
// metadata. Blank trimmed names use Passkey; duplicate PostgreSQL credential
// constraints return ErrPasskeyExists. UserHandle bytes are copied, while
// referenced SDK credential values remain shared.
func (r *passkeyRepository) Create(
	ctx context.Context,
	record *identity.PasskeyCredentialRecord,
) (*identity.PasskeyCredentialRecord, error) {
	if record == nil || len(record.Credential.ID) == 0 {
		return nil, fmt.Errorf("passkey credential is required")
	}
	credentialJSON, err := json.Marshal(record.Credential)
	if err != nil {
		return nil, fmt.Errorf("encode passkey credential: %w", err)
	}
	name := strings.TrimSpace(record.Name)
	if name == "" {
		name = "Passkey"
	}
	created := &identity.PasskeyCredentialRecord{
		UserID:     record.UserID,
		UserHandle: append([]byte(nil), record.UserHandle...),
		Name:       name,
		Credential: record.Credential,
	}
	err = r.db.QueryRowContext(ctx, `
		INSERT INTO passkey_credentials
		    (user_id, credential_id, name, credential_data)
		VALUES ($1, $2, $3, $4::jsonb)
		RETURNING id, name, created_at, updated_at
	`, record.UserID, record.Credential.ID, name, string(credentialJSON)).
		Scan(&created.ID, &created.Name, &created.CreatedAt, &created.UpdatedAt)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			return nil, identity.ErrPasskeyExists
		}
		return nil, fmt.Errorf("create passkey credential: %w", err)
	}
	return created, nil
}

// UpdateCredential serializes a nonempty credential ID and records its
// supplied usage instant in UTC for the owning account. Zero affected rows
// return ErrPasskeyNotFound, and encoding or SQL failures remain errors.
func (r *passkeyRepository) UpdateCredential(
	ctx context.Context,
	userID int64,
	credential *webauthn.Credential,
	usedAt time.Time,
) error {
	if credential == nil || len(credential.ID) == 0 {
		return fmt.Errorf("passkey credential is required")
	}
	credentialJSON, err := json.Marshal(credential)
	if err != nil {
		return fmt.Errorf("encode passkey credential: %w", err)
	}
	// Persist the supplied usage instant in UTC beside the updated serialized
	// credential, so stored last-used values represent the same instant
	// independently of the caller zone.
	result, err := r.db.ExecContext(ctx, `
		UPDATE passkey_credentials
		SET credential_data = $3::jsonb, last_used_at = $4, updated_at = NOW()
		WHERE user_id = $1 AND credential_id = $2
	`, userID, credential.ID, string(credentialJSON), usedAt.UTC())
	if err != nil {
		return fmt.Errorf("update passkey credential: %w", err)
	}
	return requirePasskeyAffected(result)
}

// Rename saves the supplied name for the account-owned credential row ID. This
// storage method does not trim the name; SQL errors propagate with context,
// and zero affected rows return ErrPasskeyNotFound.
func (r *passkeyRepository) Rename(
	ctx context.Context,
	userID, credentialID int64,
	name string,
) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE passkey_credentials
		SET name = $3, updated_at = NOW()
		WHERE user_id = $1 AND id = $2
	`, userID, credentialID, name)
	if err != nil {
		return fmt.Errorf("rename passkey credential: %w", err)
	}
	return requirePasskeyAffected(result)
}

// Delete removes the account-owned credential selected by its row ID. SQL
// failures retain deletion context, while zero affected rows return
// ErrPasskeyNotFound so callers can reject a missing or foreign target.
func (r *passkeyRepository) Delete(
	ctx context.Context,
	userID, credentialID int64,
) error {
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM passkey_credentials
		WHERE user_id = $1 AND id = $2
	`, userID, credentialID)
	if err != nil {
		return fmt.Errorf("delete passkey credential: %w", err)
	}
	return requirePasskeyAffected(result)
}

// scanPasskeyCredential decodes the selected SQL columns and serialized
// WebAuthn credential into a new record. Scan and JSON failures remain errors;
// successful values include the account handle joined by lookup callers.
func scanPasskeyCredential(scanner passkeyScanner) (*identity.PasskeyCredentialRecord, error) {
	var (
		record         identity.PasskeyCredentialRecord
		credentialJSON []byte
	)
	if err := scanner.Scan(
		&record.ID,
		&record.UserID,
		&record.UserHandle,
		&record.Name,
		&credentialJSON,
		&record.LastUsedAt,
		&record.CreatedAt,
		&record.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(credentialJSON, &record.Credential); err != nil {
		return nil, fmt.Errorf("decode passkey credential: %w", err)
	}
	return &record, nil
}

// requirePasskeyAffected validates that a credential mutation affected at
// least one row. Zero returns ErrPasskeyNotFound, and an affected-count read
// failure passes through before any successful mutation result is reported.
func requirePasskeyAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return identity.ErrPasskeyNotFound
	}
	return nil
}
