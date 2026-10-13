package postgres

import (
	"context"
	"time"

	infra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// Completed billing keys remain needed for replay detection after their
// active-table rows age out. Archiving moves each selected batch into the
// archive and removes its active rows in one SQL statement, so a failed insert
// cannot leave a successful deletion from that batch.
//
// Existing archive keys retain their saved fingerprint through ON CONFLICT DO
// NOTHING. Settlement consults the archive when claiming an active key,
// keeping replay recognition available after cleanup. Batches are bounded at
// ten thousand victims, and cleanup repeats until the affected deletion count
// is smaller than that cap; the caller supplies cutoff and
// execution/cancellation context.

// usageBillingDedupCleanupBatchSize bounds each archive/delete statement at
// ten thousand rows, limiting one cleanup batch while retaining repeated
// progress to the cutoff.
const usageBillingDedupCleanupBatchSize = 10000

// ArchiveUsageDedup moves rows older than cutoff into the billing-key archive
// in bounded batches. cutoff is converted to UTC, and the supplied executor
// owns statement transaction scope. SQL or affected-count errors stop later
// batches and leave any earlier successful batches completed.
//
// Each statement selects active rows, inserts their key/fingerprint data and
// deletes those same selected ctid values. Existing archive keys are retained
// on conflict, and settlement's archive lookup preserves duplicate
// recognition. A deletion count below the ten-thousand cap ends the loop; an
// exact full batch can require one more statement to establish completion.
func ArchiveUsageDedup(ctx context.Context, exec infra.Executor, cutoff time.Time) error {
	for {
		// Keep archive insertion and selected-row deletion in one SQL statement, so
		// a statement failure rolls back both effects of that batch.
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
		// A full deletion count leaves completion unproven, so another bounded
		// statement follows; a partial reported count is this loop's stopping
		// criterion.
		if affected < usageBillingDedupCleanupBatchSize {
			return nil
		}
	}
}
