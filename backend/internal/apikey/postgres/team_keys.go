package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/team"
)

// team_keys.go reads and mutates team credentials and returns their secrets
// for authentication-cache invalidation. Owner disablement is a separate
// persisted lock beside status, and team deletion saves a tombstone and
// soft-delete time together after locking the targeted row.
//
// Standalone administration uses the shared SQL handle, while member removal,
// rejoin and team teardown accept a caller-owned transaction. An optional
// positive actor identifier restricts operations to that member's created
// credentials; absent or nonpositive actor values leave the whole team
// eligible, so the team service authorizes the caller before invoking these
// storage methods.

// TeamKeys persists team credential administration and participates in
// caller-owned membership transactions.
type TeamKeys struct {
	// db is retained for standalone queries and writes. Assembly closes this
	// shared handle after team administration and membership participants stop using it.
	db *sql.DB
}

// NewTeamKeys retains the supplied database handle for team credential storage.
// Its caller owns database closure after users stop, and member lifecycle
// methods operate on the transaction passed by their caller.
func NewTeamKeys(db *sql.DB) *TeamKeys { return &TeamKeys{db: db} }

// ListTeamKeyStrings returns credentials of nondeleted records in teamID for
// cache invalidation. It includes every stored status; callers scan errors and
// use returned secrets as invalidation input rather than an authentication
// decision.
func (r *TeamKeys) ListTeamKeyStrings(ctx context.Context, teamID int64) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT key FROM api_keys WHERE team_id = $1 AND deleted_at IS NULL`, teamID)
	if err != nil {
		return nil, err
	}
	// Return the shared connection after scanning this team result set;
	// rows.Err still reports iteration failure before the listing can be accepted.
	// Deferred Close errors are discarded, leaving the scan or iteration result
	// as the returned outcome for this listing.
	defer func() { _ = rows.Close() }()
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// ListTeamKeys returns nondeleted team records in descending creation and ID
// order, with optional creator filtering. User and group display fields use
// left joins, and the group name expression follows the request locale; the
// listing does not hydrate full composite group bindings.
func (r *TeamKeys) ListTeamKeys(ctx context.Context, teamID int64, actorUserID *int64) ([]team.TeamAPIKeyItem, error) {
	args := []any{teamID}
	actorCondition, args := TeamKeyActorCondition(actorUserID, args)
	rows, err := r.db.QueryContext(ctx, `
		SELECT k.id, k.user_id, COALESCE(u.email, ''), k.name, k.key, k.status, k.team_owner_disabled, k.group_id,
		       `+postgresinfra.LocalizedTextExpression(ctx, "g.localization", "display_name", "COALESCE(g.name, '')")+`, k.last_used_at, k.created_at
		FROM api_keys k
		LEFT JOIN users u ON u.id = k.user_id
		LEFT JOIN groups g ON g.id = k.group_id
		WHERE k.team_id = $1 AND k.deleted_at IS NULL`+actorCondition+`
		ORDER BY k.created_at DESC, k.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	// Return the shared connection after scanning this team result set;
	// rows.Err still reports iteration failure before the listing can be accepted.
	// Deferred Close errors are discarded, leaving the scan or iteration result
	// as the returned outcome for this listing.
	defer func() { _ = rows.Close() }()
	items := make([]team.TeamAPIKeyItem, 0)
	for rows.Next() {
		var item team.TeamAPIKeyItem
		var groupID sql.NullInt64
		var lastUsedAt sql.NullTime
		if err := rows.Scan(&item.ID, &item.UserID, &item.UserEmail, &item.Name, &item.Key, &item.Status, &item.OwnerDisabled, &groupID, &item.GroupName, &lastUsedAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.GroupID = batchImageNullInt64Ptr(groupID)
		item.LastUsedAt = batchImageNullTimePtr(lastUsedAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

// DisableTeamKey sets disabled status and the independent owner lock on the
// selected nondeleted team record. A positive actor filter restricts its
// creator; a missing match reports ErrAPIKeyNotFound, and the returned
// credential lets callers invalidate authentication after success.
func (r *TeamKeys) DisableTeamKey(ctx context.Context, teamID, keyID int64, actorUserID *int64) (string, error) {
	args := []any{teamID, keyID}
	actorCondition, args := TeamKeyActorCondition(actorUserID, args)
	var key string
	err := r.db.QueryRowContext(ctx, `
		UPDATE api_keys k SET status = 'disabled', team_owner_disabled = TRUE, updated_at = NOW()
		WHERE k.team_id = $1 AND k.id = $2 AND k.deleted_at IS NULL`+actorCondition+`
		RETURNING k.key`, args...).Scan(&key)
	// No row matches this team and optional creator filter. Use the domain
	// missing-Key identity before the caller can notify caches for a successful mutation.
	if errors.Is(err, sql.ErrNoRows) {
		return "", apikey.ErrAPIKeyNotFound
	}
	return key, err
}

// EnableTeamKey sets active status and clears the independent owner lock on the
// selected nondeleted team record. Caller authorization determines whether this
// operation is allowed; a missing match reports ErrAPIKeyNotFound, and
// successful callers receive the credential for invalidation.
func (r *TeamKeys) EnableTeamKey(ctx context.Context, teamID, keyID int64, actorUserID *int64) (string, error) {
	args := []any{teamID, keyID}
	actorCondition, args := TeamKeyActorCondition(actorUserID, args)
	var key string
	err := r.db.QueryRowContext(ctx, `
		UPDATE api_keys k SET status = 'active', team_owner_disabled = FALSE, updated_at = NOW()
		WHERE k.team_id = $1 AND k.id = $2 AND k.deleted_at IS NULL`+actorCondition+`
		RETURNING k.key`, args...).Scan(&key)
	// No row matches this team and optional creator filter. Use the domain
	// missing-Key identity before the caller can notify caches for a successful mutation.
	if errors.Is(err, sql.ErrNoRows) {
		return "", apikey.ErrAPIKeyNotFound
	}
	return key, err
}

// DeleteTeamKey locks the selected nondeleted team record and returns its prior
// credential after successful tombstone deletion. A missing match reports
// ErrAPIKeyNotFound.
//
// The owned SQL transaction keeps the selected credential and tombstone write
// together, then commits before returning the prior value for cache
// invalidation. Failed selection, write or commit returns an error with
// rollback attempted by deferred cleanup. The optional actor condition narrows
// row selection; the team service performs caller authorization before this
// method.
func (r *TeamKeys) DeleteTeamKey(ctx context.Context, teamID, keyID int64, actorUserID *int64) (string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	// Selection holds a row lock through tombstone replacement. Roll back
	// failed deletion before returning so the caller cannot invalidate a surviving credential.
	defer func() { _ = tx.Rollback() }()
	args := []any{teamID, keyID}
	actorCondition, args := TeamKeyActorCondition(actorUserID, args)
	var key string
	err = tx.QueryRowContext(ctx, `SELECT k.key FROM api_keys k WHERE k.team_id = $1 AND k.id = $2 AND k.deleted_at IS NULL`+actorCondition+` FOR UPDATE`, args...).Scan(&key)
	// No row matches this team and optional creator filter. Use the domain
	// missing-Key identity before the caller can notify caches for a successful mutation.
	if errors.Is(err, sql.ErrNoRows) {
		return "", apikey.ErrAPIKeyNotFound
	}
	if err != nil {
		return "", err
	}
	tombstone := fmt.Sprintf("__deleted__%d__%d", keyID, time.Now().UnixNano())
	if _, err = tx.ExecContext(ctx, `UPDATE api_keys SET key = $2, deleted_at = NOW(), updated_at = NOW() WHERE id = $1`, keyID, tombstone); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return key, nil
}

// TeamKeyActorCondition appends a creator predicate and argument when
// actorUserID is positive. Nil and nonpositive inputs return the original
// argument slice unchanged; accepted input can reuse its backing storage during
// append.
func TeamKeyActorCondition(actorUserID *int64, args []any) (string, []any) {
	if actorUserID == nil || *actorUserID <= 0 {
		return "", args
	}
	args = append(args, *actorUserID)
	return fmt.Sprintf(" AND k.user_id = $%d", len(args)), args
}

// batchImageNullInt64Ptr converts a scanned nullable group ID to a pointer.
// Invalid input returns nil; a valid zero is retained as a present value, with
// fresh storage for this copied SQL value.
func batchImageNullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

// batchImageNullTimePtr converts a scanned nullable timestamp to a pointer.
// Invalid input returns nil, while a valid zero timestamp remains present; each
// call returns storage belonging to its copied SQL value.
func batchImageNullTimePtr(v sql.NullTime) *time.Time {
	if !v.Valid {
		return nil
	}
	return &v.Time
}

// DisableMemberInTx disables all nondeleted team credentials created by userID
// within tx. The caller owns the active transaction and its completion; this
// write retains any existing independent owner lock.
func (r *TeamKeys) DisableMemberInTx(ctx context.Context, tx *sql.Tx, teamID, userID int64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE api_keys SET status = 'disabled', updated_at = $3 WHERE team_id = $1 AND user_id = $2 AND deleted_at IS NULL`, teamID, userID, now)
	return err
}

// DisableHistoricalMemberInTx disables nondeleted team credentials created
// before the supplied rejoin time. Equal creation timestamps remain eligible;
// the caller owns tx, and updated_at records the same lifecycle timestamp.
func (r *TeamKeys) DisableHistoricalMemberInTx(ctx context.Context, tx *sql.Tx, teamID, userID int64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE api_keys SET status = 'disabled', updated_at = $3 WHERE team_id = $1 AND user_id = $2 AND created_at < $3 AND deleted_at IS NULL`, teamID, userID, now)
	return err
}

// DisableTeamInTx disables every nondeleted credential in teamID through the
// supplied transaction. The caller owns commit or rollback and publishes
// related invalidation after its team lifecycle operation successfully commits.
func (r *TeamKeys) DisableTeamInTx(ctx context.Context, tx *sql.Tx, teamID int64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE api_keys SET status = 'disabled', updated_at = $2 WHERE team_id = $1 AND deleted_at IS NULL`, teamID, now)
	return err
}
