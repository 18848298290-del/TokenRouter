package postgres

import (
	"context"
	"database/sql"

	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// ProxyChanges updates proxy associations on the caller's connection; the caller owns commit, event publication, and cache invalidation.
type ProxyChanges struct{ exec postgresinfra.Executor }

func ProxyChangesInTx(exec postgresinfra.Executor) ProxyChanges { return ProxyChanges{exec: exec} }
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

func (p ProxyChanges) Reassign(ctx context.Context, proxyID int64, target *int64) ([]int64, error) {
	var (
		rows *sql.Rows
		err  error
	)
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

	// Read and close RETURNING rows before committing the transaction so the connection is no longer busy.
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
