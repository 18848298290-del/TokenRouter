package postgres

import (
	"context"
	"time"

	infra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

const usageBillingDedupCleanupBatchSize = 10000

// ArchiveUsageDedup moves records older than cutoff into the archive in bounded batches.
// Each batch inserts and deletes in one statement so billing keys remain available for duplicate detection.
func ArchiveUsageDedup(ctx context.Context, exec infra.Executor, cutoff time.Time) error {
	for {
		res, err := exec.ExecContext(ctx, `
			WITH victims AS (
				SELECT ctid, request_id, api_key_id, request_fingerprint, created_at
				FROM usage_billing_dedup
				WHERE created_at < $1
				LIMIT $2
			), archived AS (
				INSERT INTO usage_billing_dedup_archive (request_id, api_key_id, request_fingerprint, created_at)
				SELECT request_id, api_key_id, request_fingerprint, created_at
				FROM victims
				ON CONFLICT (request_id, api_key_id) DO NOTHING
			)
			DELETE FROM usage_billing_dedup
			WHERE ctid IN (SELECT ctid FROM victims)
		`, cutoff.UTC(), usageBillingDedupCleanupBatchSize)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected < usageBillingDedupCleanupBatchSize {
			return nil
		}
	}
}
