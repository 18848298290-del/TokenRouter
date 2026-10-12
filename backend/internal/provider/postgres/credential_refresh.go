package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// CredentialRefreshStore locks the matching row in an Ent transaction until credentials and outbox changes commit.
// Persist receives that transaction context and is bound to the same ProviderStore's credential writer in production.
type CredentialRefreshStore struct {
	Client  *dbent.Client
	Persist func(context.Context, int64, map[string]any) error
}

// Apply reports whether credentials were written and whether this call committed the transaction.
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
			defer func() { _ = owned.Rollback() }()
			ctx = dbent.NewTxContext(ctx, owned)
			client = owned.Client()
		}
	}
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
	if owned != nil {
		if err := owned.Commit(); err != nil {
			return false, false, fmt.Errorf("commit credential refresh: %w", err)
		}
		return true, true, nil
	}
	return true, false, nil
}

// UpdateOAuthCredentialsIfUnchanged synchronizes the snapshot after a transaction owned by this call commits.
func (r *ProviderStore) UpdateOAuthCredentialsIfUnchanged(ctx context.Context, version provider.CredentialVersion, credentials map[string]any) (bool, error) {
	applied, committed, err := (CredentialRefreshStore{Client: r.client, Persist: r.UpdateCredentials}).Apply(ctx, version, credentials)
	if err == nil && committed {
		r.afterChange(ctx, version.ID)
	}
	return applied, err
}
