package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	infra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// cleanup.go executes retention cleanup through table truncation or throttled
// batches of old-row deletions.
// Retention cleanup removes whole targets or old rows using separate statements
// rather than one transaction spanning the run. Batched deletes select physical
// rows in timestamp and ID order, accumulate returned affected counts, and
// pause after full batches. Completed statements remain committed when a later
// batch or throttle wait fails, and their partial counters can be returned with
// that error.
//
// Whole-table cleanup reports PostgreSQL's relation estimate, not an exact
// pre-deletion count. A zero or stale estimate can therefore make its batch
// count zero even though truncation succeeds. Missing-relation text is treated
// as successful absence, while other failures propagate. Callers supply trusted
// table and column names and hold maintenance ownership outside these
// operations; the adapter borrows the SQL pool for both cleanup statements and
// advisory locking.

// These cleanup defaults govern per-statement context budgets and nonpositive
// batch settings delegated from the domain retention plan.
const (
	opsCleanupBatchTimeout      = 15 * time.Second                // per-statement context lifetime
	opsCleanupDefaultBatchSize  = ops.OpsCleanupDefaultBatchSize  // domain default row batch limit
	opsCleanupDefaultBatchPause = ops.OpsCleanupDefaultBatchPause // domain default full-batch delay
)

// CleanupStore borrows SQL storage for independently committed retention work
// and maintenance locking.
type CleanupStore struct {
	*Advisory // collector-style advisory lock adapter
	// db remains owned by its caller and is shared with the constructed Advisory
	// adapter. Keep it open until cleanup and release callbacks finish.
	db *sql.DB // borrowed cleanup SQL pool
}

// opsCleanupRunOne selects whole-table truncation or ordered old-row deletion
// for a trusted target. Truncation reports one batch only when its estimated
// count is positive; deletion receives cutoff, batch defaults, and throttle
// settings.
func opsCleanupRunOne(
	ctx context.Context,
	db *sql.DB,
	truncate bool,
	cutoff time.Time,
	table, timeCol string,
	castDate bool,
	batchSize int,
	batchPause time.Duration,
) (ops.CleanupTargetResult, error) {
	if truncate {
		deleted, err := truncateOpsTable(ctx, db, table)
		result := ops.CleanupTargetResult{Deleted: deleted}
		if deleted > 0 {
			result.Batches = 1
		}
		return result, err
	}
	return deleteOldRowsByCTID(ctx, db, table, timeCol, cutoff, batchSize, batchPause, castDate)
}

// deleteOldRowsByCTID removes rows older than cutoff in timestamp/ID-ordered
// CTID batches, returning accumulated deleted rows, nonempty batches, and
// completed throttle time. A nil pool succeeds with zero; nonpositive batch
// size or pause uses domain defaults, and identifiers are trusted SQL text.
//
// Each delete has its own child context and independent commit. A
// missing-relation diagnostic ends successfully, while another failure returns
// partial counters and leaves earlier statements committed. An affected-row
// read error can omit its completed statement from counters. A short or empty
// batch ends the loop. Only full batches pause, and a canceled pause returns
// before its elapsed time is added to Throttled. Optional date casting is
// performed in SQL on the cutoff parameter.
func deleteOldRowsByCTID(
	ctx context.Context,
	db *sql.DB,
	table string,
	timeColumn string,
	cutoff time.Time,
	batchSize int,
	batchPause time.Duration,
	castCutoffToDate bool,
) (ops.CleanupTargetResult, error) {
	result := ops.CleanupTargetResult{}
	if db == nil {
		return result, nil
	}
	if batchSize <= 0 {
		batchSize = opsCleanupDefaultBatchSize
	}
	if batchPause <= 0 {
		batchPause = opsCleanupDefaultBatchPause
	}

	// Build the cutoff comparison from trusted identifiers, casting the bound
	// parameter only for date-column targets selected by the cleanup plan.
	where := fmt.Sprintf("%s < $1", timeColumn)
	if castCutoffToDate {
		where = fmt.Sprintf("%s < $1::date", timeColumn)
	}

	q := fmt.Sprintf(`
WITH batch AS MATERIALIZED (
	  SELECT ctid AS row_ctid FROM %s
	  WHERE %s
	  ORDER BY %s ASC, id ASC
	  LIMIT $2
)
DELETE FROM %s AS target
USING batch
WHERE target.ctid = batch.row_ctid
`, table, where, timeColumn, table)

	// Commit each selected CTID batch independently through ExecContext; later
	// errors cannot roll back completed batches already counted in this result.
	for {
		// Give each statement a fresh child context and cancel it immediately after
		// execution so per-batch resources do not accumulate across the loop.
		batchCtx, cancel := context.WithTimeout(ctx, opsCleanupBatchTimeout)
		res, err := db.ExecContext(batchCtx, q, cutoff, batchSize)
		cancel()
		if err != nil {
			// Treat missing target relations as completed absence, retaining
			// progress from any earlier statements instead of failing the entire
			// retention run.
			if isMissingRelationError(err) {
				return result, nil
			}
			return result, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return result, err
		}
		result.Deleted += affected
		if affected == 0 {
			break
		}
		result.Batches++
		if affected < int64(batchSize) {
			break
		}
		// Pause after a full batch before requesting another; only a completed wait
		// contributes elapsed throttle time to returned progress.
		pauseStarted := time.Now()
		if err := sleepOpsCleanupWithContext(ctx, batchPause); err != nil {
			return result, err
		}
		result.Throttled += time.Since(pauseStarted)
	}
	return result, nil
}

// sleepOpsCleanupWithContext waits for its timer or returns the supplied
// context error. A nonnil context is required; if cancellation and timer are
// ready together, select can choose either outcome.
func sleepOpsCleanupWithContext(ctx context.Context, delay time.Duration) error {
	// Stop the throttle timer on either return path while letting parent
	// cancellation interrupt waiting between independently committed cleanup
	// batches.
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// truncateOpsTable empties a trusted table and returns its pg_class row
// estimate for cleanup accounting. A nil pool succeeds with zero, and callers
// hold any maintenance lease before invoking this operation.
//
// The estimate and TRUNCATE use separate statements under one child timeout,
// with no transaction coupling their observations. reltuples is clamped
// nonnegative and a missing relation estimates zero. TRUNCATE success returns
// that estimate even if stale; recognized missing-relation execution failure
// also succeeds with zero. Other estimate or truncate errors propagate with
// target context, and exact deleted cardinality is not measured.
func truncateOpsTable(ctx context.Context, db *sql.DB, table string) (int64, error) {
	if db == nil {
		return 0, nil
	}

	// Share one child context across estimate and truncate so both phases consume
	// the same statement budget, with cancellation resources released afterward.
	truncateCtx, cancel := context.WithTimeout(ctx, opsCleanupBatchTimeout)
	defer cancel()

	// Use planner statistics for accounting before TRUNCATE; GREATEST removes
	// negative unknown estimates, while no exact row scan occurs in this method.
	var estimatedRows int64
	const estimateQuery = `
		SELECT COALESCE((
			SELECT GREATEST(reltuples, 0)::bigint
			FROM pg_class
			WHERE oid = to_regclass($1)
		), 0)`
	if err := db.QueryRowContext(truncateCtx, estimateQuery, table).Scan(&estimatedRows); err != nil {
		return 0, fmt.Errorf("estimate %s rows: %w", table, err)
	}
	if _, err := db.ExecContext(truncateCtx, fmt.Sprintf("TRUNCATE TABLE %s", table)); err != nil {
		// Suppress missing-relation truncation errors as zero progress so retention
		// can tolerate an absent target from schema evolution.
		if isMissingRelationError(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("truncate %s: %w", table, err)
	}
	return estimatedRows, nil
}

// isMissingRelationError recognizes nil-safe, case-folded error text containing
// both relation and does not exist. It inspects wording rather than SQLSTATE,
// so unrelated errors containing those substrings can also qualify.
func isMissingRelationError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "does not exist") && strings.Contains(s, "relation")
}

// NewCleanupStore returns a backend borrowing one SQL pool for retention
// statements and advisory locking. A nil pool returns a nil interface; callers
// keep the pool alive through cleanup and acquired-lock release.
func NewCleanupStore(db *sql.DB) ops.CleanupBackend {
	if db == nil {
		return nil
	}
	return &CleanupStore{&Advisory{db}, db}
}

// AcquireMaintenance attempts the shared database-heavy maintenance session
// lock through the borrowed pool. Contention returns no acquisition, errors
// remain visible, and a successful caller invokes release once after protected
// work; cleanup errors in that callback are suppressed.
func (s *CleanupStore) AcquireMaintenance(ctx context.Context) (func(), bool, error) {
	return infra.TryAcquireDBAdvisoryLockWithError(ctx, s.db, infra.HashAdvisoryLockID("maintenance:database-heavy"))
}

// RunTarget delegates the trusted table and column to truncation or old-row
// deletion and returns its progress result. Callers hold maintenance ownership
// externally; target failure can follow independently committed work reported
// in partial counters.
func (s *CleanupStore) RunTarget(ctx context.Context, truncate bool, cutoff time.Time, table, col string, cast bool, batch int, pause time.Duration) (ops.CleanupTargetResult, error) {
	return opsCleanupRunOne(ctx, s.db, truncate, cutoff, table, col, cast, batch, pause)
}
