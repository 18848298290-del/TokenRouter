package postgres

import (
	"context"
	"database/sql"

	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// Proxy updates can invalidate an Ollama usage observation even when the
// provider's API key remains unchanged. Expiration may also move a provider
// to a backup or direct connection while retaining the original proxy ID for
// restoration; records already in fallback keep their existing origin.
//
// These operations use the egress caller's executor and return the IDs
// requiring subsequent publication. They remove stale managed snapshots as
// part of the provider SQL update, then consume RETURNING rows before control
// returns to the caller. Commit, rollback, scheduler notification, and cache
// synchronization remain with the owner of that connection.

// ProxyChanges updates proxy associations on the caller's connection.
// The caller owns commit, event publication, and cache invalidation.
type ProxyChanges struct {
	// exec runs within the egress owner's transaction and stays available through
	// provider SQL work. The owner publishes returned changes after committing
	// that transaction.
	exec postgresinfra.Executor
}

// ProxyChangesInTx binds provider proxy operations to the egress caller's
// executor. The adapter borrows that handle; the owner keeps its connection
// open and completes transaction, event, and snapshot work after the returned
// affected IDs are read.
func ProxyChangesInTx(exec postgresinfra.Executor) ProxyChanges { return ProxyChanges{exec: exec} }

// InvalidateSnapshots removes nonnull Ollama usage snapshots from nondeleted
// API Key providers on openai or anthropic that reference proxyID. It returns
// the affected IDs after consuming the result set; the caller owns commit and
// publishing those provider changes.
func (p ProxyChanges) InvalidateSnapshots(ctx context.Context, proxyID int64) ([]int64, error) {
	rows, err := p.exec.QueryContext(ctx, `
		UPDATE providers
		SET extra = COALESCE(extra, '{}'::jsonb) - 'ollama_cloud_usage_snapshot',
			updated_at = NOW()
		WHERE proxy_id = $1
			AND type = 'apikey'
			AND platform IN ('openai', 'anthropic')
			AND extra ? 'ollama_cloud_usage_snapshot'
			AND extra -> 'ollama_cloud_usage_snapshot' <> 'null'::jsonb
			AND deleted_at IS NULL
		RETURNING id
	`, proxyID)
	if err != nil {
		return nil, err
	}
	// Consume the update result before returning IDs to egress, releasing the
	// query resource so its caller can continue work on the same
	// transaction connection.
	defer func() { _ = rows.Close() }()
	providerIDs := make([]int64, 0)
	for rows.Next() {
		var providerID int64
		if err := rows.Scan(&providerID); err != nil {
			return nil, err
		}
		providerIDs = append(providerIDs, providerID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return providerIDs, nil
}

// Reassign moves nondeleted providers from proxyID while storing that ID as
// their fallback origin. A nil target selects direct connections; providers
// already carrying an origin are excluded so a later expiration cannot
// replace their restoration target.
//
// The same SQL operation removes Ollama usage snapshots for supported API Key
// platforms and strips the two retired billing-probe keys. All RETURNING rows
// are consumed and explicitly closed before the affected IDs are returned,
// allowing the caller's transaction connection to finish its remaining work.
// The caller owns commit, scheduler events, and cache synchronization.
func (p ProxyChanges) Reassign(ctx context.Context, proxyID int64, target *int64) ([]int64, error) {
	var (
		rows *sql.Rows
		err  error
	)
	// Retain the original proxy in both reassignment branches so egress can later
	// restore it, and clear snapshots whose fetch used the connection being replaced.
	if target == nil {
		rows, err = p.exec.QueryContext(ctx, `
			UPDATE providers SET proxy_id=NULL, proxy_fallback_origin_id=$1,
				extra=(CASE
					WHEN platform IN ('openai', 'anthropic') AND type='apikey'
					THEN COALESCE(extra, '{}'::jsonb) - 'ollama_cloud_usage_snapshot'
					ELSE COALESCE(extra, '{}'::jsonb)
				END) - 'upstream_billing_probe' - 'upstream_billing_probe_enabled',
				updated_at=NOW()
			WHERE proxy_id=$1 AND proxy_fallback_origin_id IS NULL AND deleted_at IS NULL
			RETURNING id`, proxyID)
	} else {
		rows, err = p.exec.QueryContext(ctx, `
			UPDATE providers SET proxy_id=$2, proxy_fallback_origin_id=$1,
				extra=(CASE
					WHEN platform IN ('openai', 'anthropic') AND type='apikey'
					THEN COALESCE(extra, '{}'::jsonb) - 'ollama_cloud_usage_snapshot'
					ELSE COALESCE(extra, '{}'::jsonb)
				END) - 'upstream_billing_probe' - 'upstream_billing_probe_enabled',
				updated_at=NOW()
			WHERE proxy_id=$1 AND proxy_fallback_origin_id IS NULL AND deleted_at IS NULL
			RETURNING id`, proxyID, *target)
	}
	if err != nil {
		return nil, err
	}

	// Drain and close RETURNING before giving IDs to the transaction owner; lib/pq
	// still has result messages to consume before that connection can accept commit.
	providerIDs := make([]int64, 0)
	for rows.Next() {
		var providerID int64
		if err := rows.Scan(&providerID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		providerIDs = append(providerIDs, providerID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return providerIDs, nil
}
