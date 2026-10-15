package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// credential_refresh.go conditionally persists refreshed credentials under a
// matching provider-row lock.
// A credential exchange can finish after an administrator changes authentication
// or disables the provider. The expected identity is serialized before a
// transaction locks the matching row, and persistence receives that transaction
// context so its credential update and outbox work share the lock. A comparison
// miss discards the exchange.
//
// Apply borrows an Ent transaction found in the context or starts one using
// Client. It commits and reports committed=true only for its owned transaction,
// leaving the outer owner responsible for borrowed commits. The store wrapper
// publishes a refreshed snapshot after an owned commit; callers carrying a
// transactional Client also carry its transaction context so the Persist
// callback uses the same transaction.

// CredentialRefreshStore coordinates identity-locked persistence using a shared
// Ent transaction context.
type CredentialRefreshStore struct {
	// Client selects the database for an owned transaction and remains required
	// when ctx supplies a transaction. A transactional client also needs its
	// matching Ent transaction in ctx so Persist uses that same transaction.
	Client *dbent.Client
	// Persist runs after the row is locked and must use the supplied transaction
	// context for its writes; production binds ProviderStore.UpdateCredentials.
	Persist func(context.Context, int64, map[string]any) error
}

// Apply locks the row matching version and invokes Persist with the replacement
// credentials. Nil expected credentials compare as an empty JSON object, and
// missing dependencies or encoding errors fail before any write. A
// transactional Client requires its matching Ent transaction in ctx so Persist
// receives the same transaction as this identity lock.
//
// The returned applied value means Persist succeeded; committed is true only
// after this call's owned transaction commits. A comparison miss returns false
// values and leaves a borrowed transaction with its caller. Owned transactions
// have deferred rollback for every early exit, including failed persistence or
// commit, while borrowed transactions retain their caller's rollback and
// publication obligations.
func (s CredentialRefreshStore) Apply(ctx context.Context, version provider.CredentialVersion, credentials map[string]any) (applied, committed bool, err error) {
	if s.Client == nil || s.Persist == nil {
		return false, false, errors.New("provider credential refresh store is not configured")
	}
	expected := version.Credentials
	if expected == nil {
		expected = map[string]any{}
	}
	payload, err := json.Marshal(expected)
	if err != nil {
		return false, false, err
	}
	client := s.Client
	var owned *dbent.Tx
	if tx := dbent.TxFromContext(ctx); tx != nil {
		client = tx.Client()
	} else {
		owned, err = s.Client.Tx(ctx)
		if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
			return false, false, err
		}
		if owned != nil {
			// Retain rollback on every early exit; a successful commit makes this
			// deferred attempt harmless while failed persistence releases the
			// row lock.
			defer func() { _ = owned.Rollback() }()
			ctx = dbent.NewTxContext(ctx, owned)
			client = owned.Client()
		}
	}
	// Match the complete expected row identity and hold its row lock through
	// Persist so a concurrent authorization cannot interleave between comparison
	// and the transaction's credential or Extra update.
	rows, err := client.QueryContext(ctx, `SELECT id FROM providers WHERE id=$1 AND deleted_at IS NULL
 AND platform=$2 AND type=$3 AND status=$4 AND credentials=$5::jsonb
 AND proxy_id IS NOT DISTINCT FROM $6 FOR UPDATE`, version.ID, version.Platform, version.Type, version.Status, string(payload), version.ProxyID)
	if err != nil {
		return false, false, err
	}
	matched := rows.Next()
	scanErr := rows.Err()
	if matched {
		var id int64
		scanErr = rows.Scan(&id)
	}
	// Close the comparison result before invoking Persist so its transaction
	// connection is available for the update while the row lock remains held.
	closeErr := rows.Close()
	if scanErr != nil {
		return false, false, scanErr
	}
	if closeErr != nil {
		return false, false, closeErr
	}
	if !matched {
		return false, false, nil
	}
	if err := s.Persist(ctx, version.ID, credentials); err != nil {
		return false, false, err
	}
	// Commit after Persist has written its fields and outbox together; borrowed
	// transactions instead return ownership to their caller for final
	// publication.
	if owned != nil {
		if err := owned.Commit(); err != nil {
			return false, false, fmt.Errorf("commit credential refresh: %w", err)
		}
		return true, true, nil
	}
	return true, false, nil
}

// UpdateOAuthCredentialsIfUnchanged applies the identity-locked credential write
// and reports whether it matched. Snapshot synchronization follows an owned
// successful commit; a borrowed transaction leaves commit and post-commit
// synchronization to its caller.
func (r *ProviderStore) UpdateOAuthCredentialsIfUnchanged(ctx context.Context, version provider.CredentialVersion, credentials map[string]any) (bool, error) {
	applied, committed, err := (CredentialRefreshStore{Client: r.client, Persist: r.UpdateCredentials}).Apply(ctx, version, credentials)
	// Publish after this call owns a successful commit so readers cannot receive
	// replacement credentials from a transaction that still might roll back.
	if err == nil && committed {
		r.afterChange(ctx, version.ID)
	}
	return applied, err
}
