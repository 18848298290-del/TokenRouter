package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// job.go stores batch-image jobs, indexed items, lifecycle transitions, and
// their events in PostgreSQL.
// Batch image jobs retain provider bindings, model identities, pricing, funding,
// and lifecycle timestamps while their item rows describe indexed output.
// The SELECT column lists and scanners share positional order, so changing one
// side requires changing the other. Nullable values become pointers and stored
// subscription allocations are decoded before a job is returned.
//
// Multi-step transitions, submission recording, settlement, and item replacement
// use a repository-owned transaction when a SQL pool is supplied. An injected
// executor instead owns its transaction lifecycle. Other update-and-event
// methods use that executor directly: with a plain pool, an event error can
// follow an already applied state update. Item replacement locks the job,
// requires indexing, and carries existing prompt previews into replacement
// rows before updating counts.
//
// Deferred rollback is attempted for failures before Commit. Once database/sql
// starts the driver's commit, a deferred rollback cannot undo it; an uncertain
// commit response requires rereading the persisted job to reconcile its outcome.

// Column lists fix the positional order used by job and item scanners.
// Their SELECT forms also supply RETURNING layouts for newly inserted metadata
// rows.
const (
	batchImageJobColumns = `
id, batch_id, user_id, billing_user_id, team_id, api_key_id, provider_id, group_id, platform, model, requested_model, internal_model, task_name, parent_batch_id, status,
provider_job_name, provider_input_ref, provider_output_ref, gcs_input_uri, gcs_output_uri,
item_count, success_count, fail_count, cancelled_count,
estimated_cost, hold_amount, actual_cost, allowance_reserved,
balance_hold_amount, subscription_hold_allocations,
subscription_rate_multiplier, balance_rate_multiplier, plan_group_rate_multiplier_enabled,
base_unit_price, group_rate_multiplier, provider_rate_multiplier,
batch_discount_multiplier, hold_multiplier, billable_unit_price, hold_unit_price,
pricing_snapshot_version,
currency, hold_id,
idempotency_key, request_hash, manifest_hash,
retry_count, version, session_id, output_expires_at, input_deleted_at, output_deleted_at, downloaded_at, user_deleted_at,
last_error_code, last_error_message,
billing_mode, preferred_subscription_id,
created_at, updated_at, submitted_at, started_at, finished_at, settled_at`

	batchImageJobSelectSQL = `SELECT ` + batchImageJobColumns + ` FROM batch_image_jobs`

	batchImageItemColumns = `
id, job_id, custom_id, status, request_hash, prompt_preview, provider_source_object,
source_line_number, source_byte_offset, source_byte_length,
mime_type, file_extension, image_count,
error_code, error_message, billed_amount,
created_at, indexed_at`

	batchImageItemSelectSQL = `SELECT ` + batchImageItemColumns + ` FROM batch_image_items`
)

// The repository assertion keeps SQL metadata operations compatible with the
// batch image use cases' persistence interface as their required methods evolve.
var _ batchimage.BatchImageRepository = (*Repository)(nil)

// SQLExecutor supplies the database or caller-owned transaction used by batch
// image metadata operations.
type SQLExecutor interface {
	// ExecContext executes a parameterized metadata statement and returns its
	// affected-row result. The executor owner determines transaction commit and
	// rollback; statement errors return to the repository operation.
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	// QueryContext opens metadata rows for a parameterized query and returns any
	// initial execution error. The repository closes those rows after scanning;
	// transaction and connection lifetimes remain with the executor owner.
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	// QueryRowContext prepares one row for a parameterized metadata query.
	// The repository calls Scan to observe missing rows or deferred query errors;
	// the executor owner controls its transaction lifetime.
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Repository stores batch job, indexed item, and event metadata through its
// selected SQL executor.
type Repository struct {
	// db is populated by NewBatchImageRepository for owned multi-step
	// transactions. NewRepositoryWithSQL leaves it nil, transferring those
	// transaction outcomes to the injected executor's owner.
	db *sql.DB // pool for owned transactions
	// sql executes ordinary metadata statements and all injected-executor
	// sequences. A caller passing a transaction keeps it usable through the
	// operation and handles its final commit or rollback.
	sql SQLExecutor // selected metadata statement executor
}

// rowScanner reads one positional SQL row into job or item metadata fields.
type rowScanner interface {
	// Scan assigns the selected columns to ordered destinations and returns any
	// row or conversion error. Callers provide the complete job or item layout,
	// including nullable holders for optional database values.
	Scan(dest ...any) error
}

// translatePersistenceError maps missing rows and unique-key failures to
// supplied application errors with their causes attached. Nil mappings leave
// those database errors unchanged, and nil input returns nil.
func translatePersistenceError(err error, notFound, conflict *apperror.ApplicationError) error {
	if err == nil {
		return nil
	}
	if notFound != nil && (errors.Is(err, sql.ErrNoRows) || ent.IsNotFound(err)) {
		return notFound.WithCause(err)
	}
	if conflict != nil && postgres.IsUniqueConstraintViolation(err) {
		return conflict.WithCause(err)
	}
	return err
}

// NewBatchImageRepository retains the SQL pool for metadata calls and owned
// multi-statement transactions. The caller keeps the pool available and controls
// its shutdown after repository and funding users finish.
func NewBatchImageRepository(db *sql.DB) batchimage.BatchImageRepository {
	return &Repository{db: db, sql: db}
}

// NewRepositoryWithSQL retains the supplied executor without a transaction
// pool. Multi-step operations run through that executor, so callers provide
// and finish a transaction when those steps require a shared outcome.
func NewRepositoryWithSQL(sqlq SQLExecutor) *Repository { return &Repository{sql: sqlq} }

// CreateBatchImageJob inserts a supported-platform job with normalized billing
// mode and default ID, created status, USD currency, and payer when omitted.
// Subscription mode requires a positive preferred subscription; other modes
// discard that preference. Unique-key failures become ErrBatchImageJobExists.
func (r *Repository) CreateBatchImageJob(ctx context.Context, params batchimage.CreateBatchImageJobParams) (*batchimage.BatchImageJob, error) {
	if !batchimage.IsSupportedBatchImageProvider(params.Platform) {
		return nil, batchimage.ErrBatchImageInvalidProvider
	}
	if params.BatchID == "" {
		batchID, err := batchimage.NewBatchImageID()
		if err != nil {
			return nil, err
		}
		params.BatchID = batchID
	}
	if params.Status == "" {
		params.Status = batchimage.BatchImageJobStatusCreated
	}
	if params.Currency == "" {
		params.Currency = "USD"
	}
	if params.BillingUserID <= 0 {
		params.BillingUserID = params.UserID
	}
	billingMode, ok := billing.NormalizeAPIKeyBillingMode(params.BillingMode)
	if !ok {
		return nil, apikey.ErrInvalidAPIKeyBillingMode
	}
	params.BillingMode = billingMode
	if billingMode != billing.APIKeyBillingModeSubscription {
		params.PreferredSubscriptionID = nil
	} else if params.PreferredSubscriptionID == nil || *params.PreferredSubscriptionID <= 0 {
		return nil, apikey.ErrPreferredSubscriptionRequired
	}

	job, err := createBatchImageJobWithSQL(ctx, r.sql, params)
	if err != nil {
		return nil, translatePersistenceError(err, nil, batchimage.ErrBatchImageJobExists)
	}
	return job, nil
}

// GetBatchImageJobByBatchID returns internal metadata for the supplied batch ID.
// It includes hidden records for processing and cleanup; missing rows become
// ErrBatchImageJobNotFound with the database cause attached.
func (r *Repository) GetBatchImageJobByBatchID(ctx context.Context, batchID string) (*batchimage.BatchImageJob, error) {
	job, err := scanBatchImageJob(r.sql.QueryRowContext(ctx, batchImageJobSelectSQL+" WHERE batch_id = $1", batchID))
	if err != nil {
		return nil, translatePersistenceError(err, batchimage.ErrBatchImageJobNotFound, nil)
	}
	return job, nil
}

// GetBatchImageJobByIdempotencyKey returns the newest matching job for a user,
// API key, and idempotency key. Hidden jobs remain eligible for retry lookup;
// missing rows become ErrBatchImageJobNotFound.
func (r *Repository) GetBatchImageJobByIdempotencyKey(ctx context.Context, userID, apiKeyID int64, key string) (*batchimage.BatchImageJob, error) {
	job, err := scanBatchImageJob(r.sql.QueryRowContext(ctx, batchImageJobSelectSQL+`
 WHERE user_id = $1 AND api_key_id = $2 AND idempotency_key = $3
 ORDER BY id DESC LIMIT 1`, userID, apiKeyID, key))
	if err != nil {
		return nil, translatePersistenceError(err, batchimage.ErrBatchImageJobNotFound, nil)
	}
	return job, nil
}

// GetBatchImageJobByBatchIDForOwner returns a visible job matching its batch ID,
// user, and API key together. Missing, differently owned, or user-hidden rows
// all produce ErrBatchImageJobNotFound with the database cause attached.
func (r *Repository) GetBatchImageJobByBatchIDForOwner(ctx context.Context, userID, apiKeyID int64, batchID string) (*batchimage.BatchImageJob, error) {
	job, err := scanBatchImageJob(r.sql.QueryRowContext(ctx, batchImageJobSelectSQL+`
 WHERE batch_id = $1 AND user_id = $2 AND api_key_id = $3 AND user_deleted_at IS NULL`, batchID, userID, apiKeyID))
	if err != nil {
		return nil, translatePersistenceError(err, batchimage.ErrBatchImageJobNotFound, nil)
	}
	return job, nil
}

// ListBatchImageJobsForOwner lists matching user and API key jobs in descending
// creation and ID order. Invalid limits select twenty, negative offsets select
// zero, and ExcludeDeleted controls whether hidden records participate.
// CreatedBefore is exclusive and task-name input retains SQL pattern meaning.
func (r *Repository) ListBatchImageJobsForOwner(ctx context.Context, userID, apiKeyID int64, filter batchimage.BatchImageJobFilter) ([]*batchimage.BatchImageJob, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}

	query := batchImageJobSelectSQL + " WHERE user_id = $1 AND api_key_id = $2"
	args := []any{userID, apiKeyID}
	if filter.ExcludeDeleted {
		query += " AND user_deleted_at IS NULL"
	}
	if filter.Status != "" {
		query += " AND status = $" + strconv.Itoa(len(args)+1)
		args = append(args, filter.Status)
	}
	if filter.TaskNameLike != "" {
		query += " AND task_name ILIKE $" + strconv.Itoa(len(args)+1)
		args = append(args, "%"+filter.TaskNameLike+"%")
	}
	if filter.Downloaded != nil {
		if *filter.Downloaded {
			query += " AND downloaded_at IS NOT NULL"
		} else {
			query += " AND downloaded_at IS NULL"
		}
	}
	if filter.CreatedAfter != nil {
		query += " AND created_at >= $" + strconv.Itoa(len(args)+1)
		args = append(args, *filter.CreatedAfter)
	}
	if filter.CreatedBefore != nil {
		query += " AND created_at < $" + strconv.Itoa(len(args)+1)
		args = append(args, *filter.CreatedBefore)
	}
	query += " ORDER BY created_at DESC, id DESC LIMIT $" + strconv.Itoa(len(args)+1) + " OFFSET $" + strconv.Itoa(len(args)+2)
	args = append(args, limit, filter.Offset)

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanBatchImageJobs(rows)
}

// GetBatchImageJobByID reads internal job metadata by its numeric database ID.
// It includes hidden records and translates a missing row into
// ErrBatchImageJobNotFound; owner checks belong to public lookup methods.
func (r *Repository) GetBatchImageJobByID(ctx context.Context, id int64) (*batchimage.BatchImageJob, error) {
	job, err := scanBatchImageJob(r.sql.QueryRowContext(ctx, batchImageJobSelectSQL+" WHERE id = $1", id))
	if err != nil {
		return nil, translatePersistenceError(err, batchimage.ErrBatchImageJobNotFound, nil)
	}
	return job, nil
}

// TransitionBatchImageJobStatus validates and records a lifecycle transition
// with its optional event. A configured pool supplies one owned transaction;
// an injected executor instead determines whether the transition and event
// share a commit and rollback outcome.
func (r *Repository) TransitionBatchImageJobStatus(ctx context.Context, batchID, toStatus string, opts batchimage.BatchImageTransitionOptions) error {
	return r.withTransaction(ctx, func(sqlq SQLExecutor) error {
		return r.transitionBatchImageJobStatusWithSQL(ctx, sqlq, batchID, toStatus, opts)
	})
}

// TouchBatchImageJobSubmitting refreshes updated_at for created or uploading
// jobs so recovery sees recent submission activity. Missing jobs or other
// statuses affect no rows and still return a successful execution result.
func (r *Repository) TouchBatchImageJobSubmitting(ctx context.Context, batchID string) error {
	_, err := r.sql.ExecContext(ctx, `
UPDATE batch_image_jobs
SET updated_at = $2
WHERE batch_id = $1
  AND status IN ('created', 'uploading')`, batchID, time.Now())
	return err
}

// FailStaleUnsubmittedBatchImageJob marks an old created or uploading job
// failed when it still has no provider job name. It returns false when the
// conditional update finds no eligible row, allowing recovery to leave recent
// or submitted work alone.
//
// A successful update stamps the first finish time and increments version
// before appending the recovery event. The true result remains true when event
// insertion fails; with a plain SQL pool, callers can receive that error after
// the failed state is already persistent.
func (r *Repository) FailStaleUnsubmittedBatchImageJob(ctx context.Context, batchID string, cutoff time.Time, code, message string) (bool, error) {
	now := time.Now()
	res, err := r.sql.ExecContext(ctx, `
UPDATE batch_image_jobs
SET status = 'failed',
    last_error_code = $2,
    last_error_message = $3,
    finished_at = CASE WHEN finished_at IS NULL THEN $4 ELSE finished_at END,
    updated_at = $4,
    version = version + 1
WHERE batch_id = $1
  AND status IN ('created', 'uploading')
  AND provider_job_name IS NULL
  AND updated_at <= $5`, batchID, code, message, now, cutoff)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		return false, nil
	}
	// The conditional update has already accepted this stale job for failure.
	// Retain true when event insertion fails so recovery can distinguish an applied
	// state change from an ineligible job.
	return true, appendBatchImageEventWithSQL(ctx, r.sql, batchID, "billing_hold_recovery_failed_unsubmitted", map[string]any{
		"batch_id":   batchID,
		"error_code": code,
	})
}

// UpdateBatchImageJobProviderOutputRef stores the discovered output reference
// and refreshes the job timestamp. A confirmed zero affected count reports a
// missing job; unavailable affected-row counts are accepted after execution.
func (r *Repository) UpdateBatchImageJobProviderOutputRef(ctx context.Context, batchID, providerOutputRef string) error {
	res, err := r.sql.ExecContext(ctx, `
UPDATE batch_image_jobs
SET provider_output_ref = $2, updated_at = $3
WHERE batch_id = $1`, batchID, providerOutputRef, time.Now())
	if err != nil {
		return translatePersistenceError(err, batchimage.ErrBatchImageJobNotFound, nil)
	}
	// The output-reference write has already succeeded. A known zero count reports
	// a missing job; unavailable counts are accepted, leaving the caller's later
	// job read to observe its stored reference.
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return batchimage.ErrBatchImageJobNotFound
	}
	return nil
}

// UpdateBatchImageJobProviderSubmit records the submitted state, provider
// references, and event through one pool-owned transaction when available.
// An injected executor handles that sequence directly and owns its transaction
// outcome, including any rollback.
func (r *Repository) UpdateBatchImageJobProviderSubmit(ctx context.Context, params batchimage.UpdateBatchImageJobProviderSubmitParams) error {
	return r.withTransaction(ctx, func(sqlq SQLExecutor) error {
		return r.updateBatchImageJobProviderSubmitWithSQL(ctx, sqlq, params)
	})
}

// updateBatchImageJobProviderSubmitWithSQL locks the job row and validates its
// move to submitted before storing the returned provider identity. The supplied
// executor owns the transaction that keeps this row lock effective across the
// validation, update, and event.
//
// Empty optional references become SQL NULL, the first submitted timestamp is
// retained, and version increments with the update. The provider-submitted event
// follows that update through the same executor; a caller using a plain pool
// accepts separate statement outcomes.
func (r *Repository) updateBatchImageJobProviderSubmitWithSQL(ctx context.Context, sqlq SQLExecutor, params batchimage.UpdateBatchImageJobProviderSubmitParams) error {
	var current string
	// Lock the current status before accepting the submitted transition so a
	// transaction owner serializes competing state changes through the following
	// provider update and event insertion.
	if err := sqlq.QueryRowContext(ctx, `SELECT status FROM batch_image_jobs WHERE batch_id = $1 FOR UPDATE`, params.BatchID).Scan(&current); err != nil {
		return translatePersistenceError(err, batchimage.ErrBatchImageJobNotFound, nil)
	}
	if !batchimage.CanTransitionBatchImageJob(current, batchimage.BatchImageJobStatusSubmitted) {
		return batchimage.ErrBatchImageInvalidTransition
	}
	now := time.Now()
	if _, err := sqlq.ExecContext(ctx, `
UPDATE batch_image_jobs
SET status = 'submitted',
    provider_job_name = $2,
    provider_input_ref = NULLIF($3, ''),
    provider_output_ref = NULLIF($4, ''),
    gcs_input_uri = NULLIF($5, ''),
    gcs_output_uri = NULLIF($6, ''),
    submitted_at = CASE WHEN submitted_at IS NULL THEN $7 ELSE submitted_at END,
    updated_at = $7,
    version = version + 1
WHERE batch_id = $1`, params.BatchID, params.ProviderJobName, params.ProviderInputRef, params.ProviderOutputRef, params.GCSInputURI, params.GCSOutputURI, now); err != nil {
		return err
	}
	// Append the event after storing the provider references using the same
	// executor. The surrounding wrapper commits both together when it owns a pool
	// transaction; injected executors control their own outcome.
	return appendBatchImageEventWithSQL(ctx, sqlq, params.BatchID, "provider_submitted", params.EventPayload)
}

// RecordBatchImageJobSubmitFailure records submission or queue errors and
// increments the job version. markFailed selects failed status and a first
// finish timestamp; a queue failure keeps the existing state so its upstream
// job can still be processed.
//
// The error metadata update precedes a submit_failed or queue_failed event.
// The method uses its executor directly, so a plain pool can retain the update
// when event insertion fails. Callers receive that event error after the
// earlier statement has succeeded.
func (r *Repository) RecordBatchImageJobSubmitFailure(ctx context.Context, batchID, code, message string, markFailed bool) error {
	now := time.Now()
	statusSQL := "status"
	if markFailed {
		statusSQL = "'failed'"
	}
	_, err := r.sql.ExecContext(ctx, `
UPDATE batch_image_jobs
SET status = `+statusSQL+`,
    last_error_code = $2,
    last_error_message = $3,
    finished_at = CASE WHEN `+statusSQL+` = 'failed' AND finished_at IS NULL THEN $4 ELSE finished_at END,
    updated_at = $4,
    version = version + 1
WHERE batch_id = $1`, batchID, code, message, now)
	if err != nil {
		return err
	}
	// The job error update precedes this event choice. Queue failure keeps the
	// existing status because submission already produced work upstream, while
	// markFailed records an ended submission.
	eventType := "submit_failed"
	if !markFailed {
		eventType = "queue_failed"
	}
	return appendBatchImageEventWithSQL(ctx, r.sql, batchID, eventType, map[string]any{"error_code": code})
}

// MarkBatchImageJobSettled records completed metadata after funding capture.
// A configured pool wraps settlement and its event in an owned transaction;
// with an injected executor, callers supply the transaction and decide its
// commit or rollback outcome.
func (r *Repository) MarkBatchImageJobSettled(ctx context.Context, params batchimage.MarkBatchImageJobSettledParams) error {
	return r.withTransaction(ctx, func(sqlq SQLExecutor) error {
		return r.markBatchImageJobSettledWithSQL(ctx, sqlq, params)
	})
}

// markBatchImageJobSettledWithSQL completes a settling job whose stored
// manifest is absent or matches the supplied hash. It uses the optional time
// override and retains previously set settlement, finish, and output expiry
// timestamps while incrementing version.
//
// A zero-row update is re-read to distinguish a missing job, completed retry,
// invalid state, or conflicting manifest. Successful completion appends its
// event through the same executor; its caller controls the transaction joining
// those steps and handles the already-settled result during recovery.
func (r *Repository) markBatchImageJobSettledWithSQL(ctx context.Context, sqlq SQLExecutor, params batchimage.MarkBatchImageJobSettledParams) error {
	now := time.Now()
	if params.Now != nil {
		now = *params.Now
	}
	outputExpiresAt := params.OutputExpiresAt

	res, err := sqlq.ExecContext(ctx, `
UPDATE batch_image_jobs
SET status = 'completed',
    actual_cost = $2,
    manifest_hash = $3,
    settled_at = CASE WHEN settled_at IS NULL THEN $4 ELSE settled_at END,
    finished_at = CASE WHEN finished_at IS NULL THEN $4 ELSE finished_at END,
    output_expires_at = CASE WHEN output_expires_at IS NULL THEN $5 ELSE output_expires_at END,
    updated_at = $4,
    version = version + 1
WHERE batch_id = $1
  AND status = 'settling'
  AND (manifest_hash IS NULL OR manifest_hash = '' OR manifest_hash = $3)`, params.BatchID, params.ActualCost, params.ManifestHash, now, outputExpiresAt)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	// A rejected settlement update can mean completion was already recorded or
	// the manifest changed. Re-read its current metadata to distinguish those
	// recovery decisions from a missing job or invalid state.
	if affected == 0 {
		job, getErr := scanBatchImageJob(sqlq.QueryRowContext(ctx, batchImageJobSelectSQL+" WHERE batch_id = $1", params.BatchID))
		if getErr != nil {
			return translatePersistenceError(getErr, batchimage.ErrBatchImageJobNotFound, nil)
		}
		if job.Status != batchimage.BatchImageJobStatusSettling {
			if job.Status == batchimage.BatchImageJobStatusCompleted {
				return batchimage.ErrBatchImageAlreadySettled
			}
			return batchimage.ErrBatchImageSettlementInvalidStatus
		}
		return batchimage.ErrBatchImageSettlementManifestConflict
	}
	// The settlement event follows the accepted manifest and completed status.
	// Using the same executor lets its transaction owner roll back both if audit
	// insertion fails.
	return appendBatchImageEventWithSQL(ctx, sqlq, params.BatchID, "settlement_completed", params.EventPayload)
}

// SetBatchImageJobSettlementFailed stores the latest billing error and
// increments retry_count, returning the new count for the settlement retry
// decision. It leaves the lifecycle status unchanged and translates missing
// rows into ErrBatchImageJobNotFound.
//
// The returned count is available before the settlement-failed event is
// inserted. With a plain pool, event insertion can fail after that increment
// has persisted, so callers can receive both the new count and an error
// without a completed audit insertion.
func (r *Repository) SetBatchImageJobSettlementFailed(ctx context.Context, batchID, code, message string) (int, error) {
	var retryCount int
	err := r.sql.QueryRowContext(ctx, `
UPDATE batch_image_jobs
SET last_error_code = $2,
    last_error_message = $3,
    retry_count = retry_count + 1,
    updated_at = $4
WHERE batch_id = $1
RETURNING retry_count`, batchID, code, message, time.Now()).Scan(&retryCount)
	if err != nil {
		return 0, translatePersistenceError(err, batchimage.ErrBatchImageJobNotFound, nil)
	}
	// Return the incremented retry count even when event insertion fails.
	// With a plain pool, recovery needs to account for that already applied
	// increment before deciding another settlement attempt.
	return retryCount, appendBatchImageEventWithSQL(ctx, r.sql, batchID, "settlement_failed", map[string]any{
		"error_code": code,
	})
}

// transitionBatchImageJobStatusWithSQL locks a job and checks the requested
// move with CanTransitionBatchImageJob before writing its status and version.
// The supplied executor owns the transaction needed to keep the row lock
// effective until the update and optional event finish.
//
// Lifecycle timestamps are filled on their first matching transition, including
// first submission, start, finish, settlement, and output deletion. Failure text
// changes when entering failed. An optional event follows the update through
// the same executor, letting a transaction owner commit or roll back the
// transition and its audit entry together.
func (r *Repository) transitionBatchImageJobStatusWithSQL(ctx context.Context, sqlq SQLExecutor, batchID, toStatus string, opts batchimage.BatchImageTransitionOptions) error {
	var current string
	// Read status under a row lock before applying transition rules.
	// The supplied transaction keeps competing lifecycle updates serialized until
	// the metadata and optional event share their final outcome.
	if err := sqlq.QueryRowContext(ctx, `SELECT status FROM batch_image_jobs WHERE batch_id = $1 FOR UPDATE`, batchID).Scan(&current); err != nil {
		return translatePersistenceError(err, batchimage.ErrBatchImageJobNotFound, nil)
	}
	if !batchimage.CanTransitionBatchImageJob(current, toStatus) {
		return batchimage.ErrBatchImageInvalidTransition
	}

	now := time.Now()
	if opts.Now != nil {
		now = *opts.Now
	}

	if _, err := sqlq.ExecContext(ctx, `
UPDATE batch_image_jobs
SET
    status = $2::varchar,
    version = version + 1,
    updated_at = $3,
    last_error_code = CASE WHEN $2::varchar = 'failed' THEN $4 ELSE last_error_code END,
    last_error_message = CASE WHEN $2::varchar = 'failed' THEN $5 ELSE last_error_message END,
    submitted_at = CASE WHEN $2::varchar = 'submitted' AND submitted_at IS NULL THEN $3 ELSE submitted_at END,
    started_at = CASE WHEN $2::varchar = 'running' AND started_at IS NULL THEN $3 ELSE started_at END,
    finished_at = CASE WHEN $2::varchar IN ('completed', 'failed', 'cancelled') AND finished_at IS NULL THEN $3 ELSE finished_at END,
    settled_at = CASE WHEN $2::varchar = 'completed' AND settled_at IS NULL THEN $3 ELSE settled_at END,
    output_deleted_at = CASE WHEN $2::varchar = 'output_deleted' AND output_deleted_at IS NULL THEN $3 ELSE output_deleted_at END
WHERE batch_id = $1`, batchID, toStatus, now, opts.ErrorCode, opts.ErrorMessage); err != nil {
		return err
	}

	// Append an optional event after the status write using the same executor.
	// A transaction joins those effects, while a plain pool can retain the update
	// if this event fails.
	if opts.EventType != "" {
		return appendBatchImageEventWithSQL(ctx, sqlq, batchID, opts.EventType, opts.EventPayload)
	}
	return nil
}

// CreateBatchImageItem inserts one indexed item and returns its stored
// metadata. The caller supplies the job identity and result fields; a
// unique-key failure becomes ErrBatchImageItemExists for duplicate job/custom-ID
// combinations.
func (r *Repository) CreateBatchImageItem(ctx context.Context, params batchimage.CreateBatchImageItemParams) (*batchimage.BatchImageItem, error) {
	item, err := createBatchImageItemWithSQL(ctx, r.sql, params)
	if err != nil {
		return nil, translatePersistenceError(err, nil, batchimage.ErrBatchImageItemExists)
	}
	return item, nil
}

// BulkCreateBatchImageItems inserts the supplied item rows in order and treats
// an empty input as success. A repository with a SQL pool uses one transaction,
// so any insertion failure leaves its earlier rows uncommitted and ready for
// rollback.
//
// An injected executor receives each insertion directly and owns the shared
// outcome. Its owner supplies a transaction when partial insertion is
// unacceptable; uniqueness failures are translated before returning, and
// pool-owned success commits the complete set after all rows were inserted.
func (r *Repository) BulkCreateBatchImageItems(ctx context.Context, params []batchimage.CreateBatchImageItemParams) error {
	if len(params) == 0 {
		return nil
	}
	return r.withTransaction(ctx, func(sqlq SQLExecutor) error {
		// Insert in input order so the first failed row determines the returned
		// error and the remaining item rows stay pending for the caller.
		for _, param := range params {
			if _, err := createBatchImageItemWithSQL(ctx, sqlq, param); err != nil {
				return translatePersistenceError(err, nil, batchimage.ErrBatchImageItemExists)
			}
		}
		return nil
	})
}

// ReplaceBatchImageItemsForJob replaces indexed items and result counts for an
// indexing job. A configured pool supplies one owned transaction; an injected
// executor determines the outcome of deleting old rows, inserting replacements,
// and updating counts.
func (r *Repository) ReplaceBatchImageItemsForJob(ctx context.Context, batchID string, items []batchimage.CreateBatchImageItemParams, counts batchimage.BatchImageCounts) error {
	return r.withTransaction(ctx, func(sqlq SQLExecutor) error {
		return r.replaceBatchImageItemsForJobWithSQL(ctx, sqlq, batchID, items, counts)
	})
}

// withTransaction runs one metadata operation using the repository's selected
// transaction ownership. An injected executor receives the operation directly,
// and its caller decides whether to commit or roll back its surrounding work.
// A configured pool begins a transaction with default options before any
// operation statements execute.
//
// The owned transaction commits after the operation succeeds. Begin, operation,
// and commit errors return in their execution order; cleanup rollback errors
// are discarded so they cannot replace the operation's result. The callback
// chooses SQL ordering and time sampling after its executor is selected.
func (r *Repository) withTransaction(ctx context.Context, run func(SQLExecutor) error) error {
	if r.db == nil {
		return run(r.sql)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// A failed operation can have already written metadata or acquired row locks.
	// Attempt rollback on exit to release those changes, retaining the first error.
	// Once database/sql starts driver commit, this attempt returns ErrTxDone.
	defer func() {
		_ = tx.Rollback()
	}()

	if err := run(tx); err != nil {
		return err
	}
	// Publish the operation's complete metadata sequence after its final statement
	// succeeds, keeping all accepted writes and audit records in one transaction.
	return tx.Commit()
}

// replaceBatchImageItemsForJobWithSQL locks the job and replaces its items
// while status is indexing. Other statuses return ErrBatchImageIndexStateConflict
// before deletion, protecting results that may already determine captured
// charges from a worker that resumes late.
//
// Existing nonempty prompt previews are read before deleting old items and
// are reused when a replacement omits its preview. Each row receives the
// target batch ID, then success and failure counts update after insertion.
// The supplied executor owns the transaction joining these writes; failures
// otherwise can leave partial replacement under a plain pool.
func (r *Repository) replaceBatchImageItemsForJobWithSQL(ctx context.Context, sqlq SQLExecutor, batchID string, items []batchimage.CreateBatchImageItemParams, counts batchimage.BatchImageCounts) error {
	var id int64
	var status string
	// Serialize status validation and result replacement under the transaction's
	// job-row lock. Competing transitions then wait until deletion, insertion, and
	// count updates have reached their shared commit or rollback outcome.
	if err := sqlq.QueryRowContext(ctx, `SELECT id, status FROM batch_image_jobs WHERE batch_id = $1 FOR UPDATE`, batchID).Scan(&id, &status); err != nil {
		return translatePersistenceError(err, batchimage.ErrBatchImageJobNotFound, nil)
	}
	// A worker can resume after losing its execution lock. Require indexing
	// before touching result rows so a late attempt cannot replace items whose
	// successful count may already determine captured charges.
	if status != batchimage.BatchImageJobStatusIndexing {
		return batchimage.ErrBatchImageIndexStateConflict
	}
	// Read previews before deleting the pending items that hold submission text.
	// Replacement results can omit previews, so retaining them by custom ID keeps
	// public item descriptions available after indexing.
	promptPreviews, err := r.batchImageItemPromptPreviews(ctx, sqlq, batchID)
	if err != nil {
		return err
	}
	// Delete, insert replacements, and update counts through one executor while
	// the job row remains locked by its transaction. A rollback restores the old
	// rows when any replacement step fails.
	if _, err := sqlq.ExecContext(ctx, `DELETE FROM batch_image_items WHERE job_id = $1`, batchID); err != nil {
		return err
	}
	for _, item := range items {
		item.JobID = batchID
		// Provider-indexed rows need not repeat submission text. Reuse the existing
		// preview for the same custom ID when this replacement omits it, while a
		// supplied preview keeps precedence.
		if item.PromptPreview == nil {
			if preview := promptPreviews[item.CustomID]; preview != "" {
				item.PromptPreview = &preview
			}
		}
		if _, err := createBatchImageItemWithSQL(ctx, sqlq, item); err != nil {
			return translatePersistenceError(err, nil, batchimage.ErrBatchImageItemExists)
		}
	}
	// Update counts after all replacement rows are inserted so settlement observes
	// counts derived from the accepted set when the surrounding executor's
	// transaction commits those writes together.
	_, err = sqlq.ExecContext(ctx, `
UPDATE batch_image_jobs
SET success_count = $2,
    fail_count = $3,
    updated_at = $4
WHERE batch_id = $1`, batchID, counts.SuccessCount, counts.FailCount, time.Now())
	return err
}

// batchImageItemPromptPreviews returns existing nonempty previews keyed by
// custom ID before result replacement. It closes its query rows and propagates
// scan or iteration errors, while omitting NULL and empty preview values.
func (r *Repository) batchImageItemPromptPreviews(ctx context.Context, sqlq SQLExecutor, batchID string) (map[string]string, error) {
	rows, err := sqlq.QueryContext(ctx, `SELECT custom_id, prompt_preview FROM batch_image_items WHERE job_id = $1 AND prompt_preview IS NOT NULL`, batchID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]string)
	for rows.Next() {
		var customID string
		var preview sql.NullString
		if err := rows.Scan(&customID, &preview); err != nil {
			return nil, err
		}
		if preview.Valid && preview.String != "" {
			out[customID] = preview.String
		}
	}
	return out, rows.Err()
}

// ListBatchImageItems reads a batch's items in ascending database ID order
// with an optional exact status filter. Invalid limits select one hundred and
// negative offsets select zero; this internal method performs no owner lookup.
func (r *Repository) ListBatchImageItems(ctx context.Context, batchID string, filter batchimage.BatchImageItemFilter) ([]*batchimage.BatchImageItem, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}

	query := batchImageItemSelectSQL + " WHERE job_id = $1"
	args := []any{batchID}
	if filter.Status != "" {
		query += " AND status = $2"
		args = append(args, filter.Status)
	}
	query += " ORDER BY id ASC LIMIT $" + strconv.Itoa(len(args)+1) + " OFFSET $" + strconv.Itoa(len(args)+2)
	args = append(args, limit, filter.Offset)

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var items []*batchimage.BatchImageItem
	for rows.Next() {
		item, err := scanBatchImageItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// ListBatchImageItemsForOwner checks the visible job's user and API key before
// listing its indexed items. Item pagination follows ListBatchImageItems;
// owner lookup and listing use separate statements through the selected executor.
func (r *Repository) ListBatchImageItemsForOwner(ctx context.Context, userID, apiKeyID int64, batchID string, filter batchimage.BatchImageItemFilter) ([]*batchimage.BatchImageItem, error) {
	if _, err := r.GetBatchImageJobByBatchIDForOwner(ctx, userID, apiKeyID, batchID); err != nil {
		return nil, err
	}
	return r.ListBatchImageItems(ctx, batchID, filter)
}

// GetBatchImageJobForDownload returns the visible job matching its user,
// API key, and batch ID. The download use case then checks lifecycle and output
// availability before reading any provider results.
func (r *Repository) GetBatchImageJobForDownload(ctx context.Context, userID, apiKeyID int64, batchID string) (*batchimage.BatchImageJob, error) {
	return r.GetBatchImageJobByBatchIDForOwner(ctx, userID, apiKeyID, batchID)
}

// GetBatchImageItemForDownload returns the indexed item matching batch and
// custom ID, translating absence into ErrBatchImageItemNotFound. The download
// caller checks job ownership separately before using this internal item lookup.
func (r *Repository) GetBatchImageItemForDownload(ctx context.Context, batchID, customID string) (*batchimage.BatchImageItem, error) {
	item, err := scanBatchImageItem(r.sql.QueryRowContext(ctx, batchImageItemSelectSQL+`
 WHERE job_id = $1 AND custom_id = $2`, batchID, customID))
	if err != nil {
		return nil, translatePersistenceError(err, batchimage.ErrBatchImageItemNotFound, nil)
	}
	return item, nil
}

// ListBatchImageItemsForDownload delegates a status-filtered batch item page.
// Its limit follows ListBatchImageItems normalization; download callers check
// job ownership and lifecycle before requesting the indexed result rows.
func (r *Repository) ListBatchImageItemsForDownload(ctx context.Context, batchID string, status string, limit int) ([]*batchimage.BatchImageItem, error) {
	return r.ListBatchImageItems(ctx, batchID, batchimage.BatchImageItemFilter{Status: status, Limit: limit})
}

// ListBatchImageJobsDueForInputCleanup returns terminal jobs with a non-NULL
// provider input reference and no input-deletion marker, ordered by job ID.
// Invalid limits select one hundred; the cutoff includes its exact timestamp.
//
// Retention uses finished_at first, then settled_at, updated_at, and created_at
// when earlier fields are NULL. This is field precedence, not an earliest-time
// calculation. Cleanup.RunOnce uses the selected timestamp to find inputs old
// enough for deletion, including hidden jobs retained for internal cleanup.
func (r *Repository) ListBatchImageJobsDueForInputCleanup(ctx context.Context, cutoff time.Time, limit int) ([]*batchimage.BatchImageJob, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	// A recorded finish anchors input retention ahead of later metadata writes.
	// Use fallback timestamps only when that lifecycle evidence is absent so those
	// writes do not postpone cleanup of finished jobs.
	rows, err := r.sql.QueryContext(ctx, batchImageJobSelectSQL+`
 WHERE input_deleted_at IS NULL
   AND provider_input_ref IS NOT NULL
   AND status IN ('completed', 'failed', 'cancelled', 'output_deleted')
   AND COALESCE(finished_at, settled_at, updated_at, created_at) <= $1
 ORDER BY id ASC
 LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanBatchImageJobs(rows)
}

// ListBatchImageJobsDueForOutputCleanup finds completed jobs with retained
// output references and an expiry at or before now. Invalid limits select one
// hundred; expiry and ID order make the earliest retained outputs appear first.
func (r *Repository) ListBatchImageJobsDueForOutputCleanup(ctx context.Context, now time.Time, limit int) ([]*batchimage.BatchImageJob, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := r.sql.QueryContext(ctx, batchImageJobSelectSQL+`
 WHERE output_deleted_at IS NULL
   AND provider_output_ref IS NOT NULL
   AND status = 'completed'
   AND output_expires_at IS NOT NULL
   AND output_expires_at <= $1
 ORDER BY output_expires_at ASC, id ASC
 LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanBatchImageJobs(rows)
}

// ListStaleUnsubmittedBatchImageJobs returns old created or uploading jobs with
// no provider job name, ordered by updated_at and ID. Invalid limits select one
// hundred; the cutoff includes jobs updated exactly at that time.
//
// Eligibility uses hold_amount when present and estimated_cost only when the
// hold is NULL. A stored zero hold therefore excludes a job even with a positive
// estimate. BillingRecovery rechecks submission activity in its conditional
// failure update before releasing a returned job's reservation.
func (r *Repository) ListStaleUnsubmittedBatchImageJobs(ctx context.Context, cutoff time.Time, limit int) ([]*batchimage.BatchImageJob, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	// Match settlement's held-amount precedence when deciding whether stale funding
	// work exists. A recorded hold already supplies that basis, so an estimate
	// participates only before any hold amount has been stored.
	rows, err := r.sql.QueryContext(ctx, batchImageJobSelectSQL+`
 WHERE status IN ('created', 'uploading')
   AND provider_job_name IS NULL
   AND COALESCE(hold_amount, estimated_cost, 0) > 0
   AND updated_at <= $1
 ORDER BY updated_at ASC, id ASC
 LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanBatchImageJobs(rows)
}

// MarkBatchImageInputDeleted stores the first input-deletion timestamp while
// refreshing updated_at and incrementing version on each successful update.
// A confirmed zero affected count returns ErrBatchImageJobNotFound; unavailable
// affected-row counts are accepted.
//
// An input-cleanup event follows the metadata update using the supplied
// deletion time rendered in UTC. These statements use the executor directly,
// so a plain pool can keep the deletion marker after an event error. Callers
// handle that partial audit outcome separately from provider object deletion.
func (r *Repository) MarkBatchImageInputDeleted(ctx context.Context, batchID string, deletedAt time.Time) error {
	res, err := r.sql.ExecContext(ctx, `
UPDATE batch_image_jobs
SET input_deleted_at = CASE WHEN input_deleted_at IS NULL THEN $2 ELSE input_deleted_at END,
    updated_at = $2,
    version = version + 1
WHERE batch_id = $1`, batchID, deletedAt)
	if err != nil {
		return err
	}
	// CleanupJob calls this update after accepting provider input cleanup.
	// A known zero count reports missing local progress before event insertion;
	// unavailable counts are accepted and let that insertion report its own outcome.
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return batchimage.ErrBatchImageJobNotFound
	}
	// Persist the input deletion marker before appending its cleanup audit event.
	// A plain pool retains that marker on an event failure, so cleanup retries can
	// observe that input deletion was recorded.
	return appendBatchImageEventWithSQL(ctx, r.sql, batchID, "input_cleanup_completed", map[string]any{
		"batch_id":       batchID,
		"cleanup_target": "input",
		"deleted_at":     deletedAt.UTC().Format(time.RFC3339),
	})
}

// MarkBatchImageOutputDeleted records the first output-deletion timestamp and
// changes completed jobs to output_deleted. Other statuses remain as stored,
// while version and updated_at change on every successful update; confirmed
// absence returns ErrBatchImageJobNotFound.
//
// The output-cleanup event follows the update through the same executor.
// A plain pool can retain the metadata change after event insertion fails,
// and unavailable affected-row counts are accepted after execution. Provider
// deletion has already been handled by the cleanup use case before this
// metadata method runs.
func (r *Repository) MarkBatchImageOutputDeleted(ctx context.Context, batchID string, deletedAt time.Time) error {
	res, err := r.sql.ExecContext(ctx, `
UPDATE batch_image_jobs
SET status = CASE WHEN status = 'completed' THEN 'output_deleted' ELSE status END,
    output_deleted_at = CASE WHEN output_deleted_at IS NULL THEN $2 ELSE output_deleted_at END,
    finished_at = CASE WHEN status = 'completed' AND finished_at IS NULL THEN $2 ELSE finished_at END,
    updated_at = $2,
    version = version + 1
WHERE batch_id = $1`, batchID, deletedAt)
	if err != nil {
		return err
	}
	// CleanupJob calls this update after accepting provider output cleanup.
	// A known zero count reports missing local state before its audit event;
	// unavailable counts are accepted after the output-deletion statement succeeds.
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return batchimage.ErrBatchImageJobNotFound
	}
	// Persist output deletion state before its audit event. With a plain pool an
	// event error can leave that state applied, allowing later downloads to observe
	// the removed output even while audit insertion failed.
	return appendBatchImageEventWithSQL(ctx, r.sql, batchID, "output_cleanup_completed", map[string]any{
		"batch_id":       batchID,
		"cleanup_target": "output",
		"deleted_at":     deletedAt.UTC().Format(time.RFC3339),
	})
}

// MarkBatchImageDownloaded records the first delivery timestamp and refreshes
// updated_at for the batch ID. Repeated calls retain the first downloaded_at
// value while appending another delivery event; confirmed missing rows return
// ErrBatchImageJobNotFound.
//
// The event uses the supplied download time, and its insertion follows the
// metadata update through the same executor. With a plain pool the marker can
// persist despite an event error. Unavailable affected-row counts are accepted,
// so successful execution can continue to event insertion without row-count
// confirmation.
func (r *Repository) MarkBatchImageDownloaded(ctx context.Context, batchID string, downloadedAt time.Time) error {
	res, err := r.sql.ExecContext(ctx, `
UPDATE batch_image_jobs
SET downloaded_at = CASE WHEN downloaded_at IS NULL THEN $2 ELSE downloaded_at END,
    updated_at = $2
WHERE batch_id = $1`, batchID, downloadedAt)
	if err != nil {
		return err
	}
	// HTTP handlers record delivery after sending their image or ZIP.
	// A known zero count reports a missing record before appending its event;
	// unavailable counts are accepted after the marker update.
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return batchimage.ErrBatchImageJobNotFound
	}
	// Record the first delivery marker before its audit event. With a plain pool
	// an event failure leaves that marker applied, so callers logging the failure
	// cannot assume delivery metadata is absent.
	return appendBatchImageEventWithSQL(ctx, r.sql, batchID, "download_completed", map[string]any{
		"batch_id":      batchID,
		"downloaded_at": downloadedAt.UTC().Format(time.RFC3339),
	})
}

// MarkBatchImageJobUserDeleted hides an unhidden completed, failed, cancelled,
// or output-deleted job matching the supplied user and API key. It retains the
// first deletion time and updates metadata, returning
// ErrBatchImageRecordDeleteNotReady when a confirmed zero count prevents deletion.
//
// The user-deletion event follows the conditional update. These statements use
// the executor directly, so a plain pool can keep the hidden record after an
// event error; unavailable affected-row counts are accepted. Internal lookup
// and cleanup can still reach the retained job metadata.
func (r *Repository) MarkBatchImageJobUserDeleted(ctx context.Context, userID, apiKeyID int64, batchID string, deletedAt time.Time) error {
	res, err := r.sql.ExecContext(ctx, `
UPDATE batch_image_jobs
SET user_deleted_at = CASE WHEN user_deleted_at IS NULL THEN $4 ELSE user_deleted_at END,
    updated_at = $4
WHERE batch_id = $1
  AND user_id = $2
  AND api_key_id = $3
  AND user_deleted_at IS NULL
  AND status IN ('completed', 'failed', 'cancelled', 'output_deleted')`, batchID, userID, apiKeyID, deletedAt)
	if err != nil {
		return err
	}
	// A confirmed zero count covers missing, hidden, differently owned, or
	// unfinished records. Return delete-not-ready before the audit event in those
	// cases; unavailable counts are accepted after execution.
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return batchimage.ErrBatchImageRecordDeleteNotReady
	}
	// Hide the record before appending the owner's deletion event.
	// A plain pool can keep it hidden after an audit error, while internal
	// processing and cleanup still use the retained metadata.
	return appendBatchImageEventWithSQL(ctx, r.sql, batchID, "user_record_deleted", map[string]any{
		"batch_id":   batchID,
		"deleted_at": deletedAt.UTC().Format(time.RFC3339),
		"user_id":    userID,
		"api_key_id": apiKeyID,
	})
}

// SetBatchImageOutputExpiresAt fills an absent output expiry and refreshes
// updated_at while retaining an existing expiry. Confirmed absence reports
// ErrBatchImageJobNotFound; unavailable affected-row counts are accepted after
// successful execution.
func (r *Repository) SetBatchImageOutputExpiresAt(ctx context.Context, batchID string, expiresAt time.Time) error {
	res, err := r.sql.ExecContext(ctx, `
UPDATE batch_image_jobs
SET output_expires_at = CASE WHEN output_expires_at IS NULL THEN $2 ELSE output_expires_at END,
    updated_at = $3
WHERE batch_id = $1`, batchID, expiresAt, time.Now())
	if err != nil {
		return err
	}
	// An existing expiry remains the cleanup deadline when this update is retried.
	// A known zero count reports a missing job, while unavailable counts are
	// accepted after that first-expiry rule has executed.
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return batchimage.ErrBatchImageJobNotFound
	}
	return nil
}

// RecordBatchImageCleanupFailure stores the latest cleanup error and increases
// retry_count while leaving job status unchanged. The cleanup use case can
// observe this metadata on a later attempt; the update itself does not require
// a particular lifecycle state.
//
// An output-cleanup-failed event follows the update through the selected
// executor. A plain pool can retain the error and retry increment when event
// insertion fails, so a returned error does not imply that neither statement
// changed the database.
func (r *Repository) RecordBatchImageCleanupFailure(ctx context.Context, batchID, code, message string) error {
	_, err := r.sql.ExecContext(ctx, `
UPDATE batch_image_jobs
SET last_error_code = $2,
    last_error_message = $3,
    retry_count = retry_count + 1,
    updated_at = $4
WHERE batch_id = $1`, batchID, code, message, time.Now())
	if err != nil {
		return err
	}
	// Store cleanup diagnostics and its retry increment before appending the
	// failure event. A plain pool can retain that recovery metadata when event
	// insertion returns an error.
	return appendBatchImageEventWithSQL(ctx, r.sql, batchID, "output_cleanup_failed", map[string]any{"error_code": code})
}

// AppendBatchImageEvent inserts an event for the supplied batch through the
// repository executor. Non-nil payloads are JSON encoded; nil uses SQL NULL,
// and transaction grouping belongs to the executor's caller.
func (r *Repository) AppendBatchImageEvent(ctx context.Context, batchID, eventType string, payload any) error {
	return appendBatchImageEventWithSQL(ctx, r.sql, batchID, eventType, payload)
}

// createBatchImageJobWithSQL inserts prepared job metadata and scans its
// returned row using the shared job column order. Its caller supplies validated
// platform, billing mode, IDs, status, and currency before invoking this
// lower-level persistence helper.
//
// Requested and internal model spellings are trimmed, then fall back to the
// upstream model when absent. This keeps partial identity snapshots usable
// by public responses and settlement, while complete snapshots retain distinct
// requested, internal, and upstream names. Subscription allocations are encoded
// into JSON alongside the submitted pricing and funding values.
func createBatchImageJobWithSQL(ctx context.Context, sqlq SQLExecutor, params batchimage.CreateBatchImageJobParams) (*batchimage.BatchImageJob, error) {
	subscriptionHoldAllocations, err := json.Marshal(params.SubscriptionHoldAllocations)
	if err != nil {
		return nil, err
	}
	requestedModel := strings.TrimSpace(params.RequestedModel)
	// When no requested identity is supplied, use the upstream model as its stored
	// fallback. Public metadata can then expose a model name for jobs created with
	// a partial identity snapshot.
	if requestedModel == "" {
		requestedModel = params.Model
	}
	internalModel := strings.TrimSpace(params.InternalModel)
	// When the caller omits the model before provider mapping, use the upstream
	// model as its stored fallback. Settlement can then record a usage model from
	// jobs with a partial identity snapshot.
	if internalModel == "" {
		internalModel = params.Model
	}
	return scanBatchImageJob(sqlq.QueryRowContext(ctx, `
INSERT INTO batch_image_jobs (
    batch_id, user_id, billing_user_id, team_id, api_key_id, provider_id, group_id, platform, model, requested_model, internal_model, task_name, parent_batch_id, status,
    provider_job_name, provider_input_ref, provider_output_ref, gcs_input_uri, gcs_output_uri,
    item_count, success_count, fail_count, cancelled_count,
    estimated_cost, hold_amount, actual_cost, balance_hold_amount, subscription_hold_allocations,
    subscription_rate_multiplier, balance_rate_multiplier, plan_group_rate_multiplier_enabled,
    base_unit_price, group_rate_multiplier, provider_rate_multiplier,
    batch_discount_multiplier, hold_multiplier, billable_unit_price, hold_unit_price,
    pricing_snapshot_version,
    currency, hold_id,
    idempotency_key, request_hash, manifest_hash, retry_count, session_id, output_expires_at,
    billing_mode, preferred_subscription_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
    $15, $16, $17, $18, $19,
    $20, $21, $22, $23,
    $24, $25, $26, $27, $28,
    $29, $30, $31,
    $32, $33, $34,
    $35, $36, $37, $38,
    $39,
    $40, $41,
    $42, $43, $44, $45, $46, $47,
    $48, $49
)
RETURNING `+batchImageJobColumns,
		params.BatchID, params.UserID, params.BillingUserID, params.TeamID, params.APIKeyID, params.ProviderID, params.GroupID, params.Platform, params.Model, requestedModel, internalModel, params.TaskName, params.ParentBatchID, params.Status,
		params.ProviderJobName, params.ProviderInputRef, params.ProviderOutputRef, params.GCSInputURI, params.GCSOutputURI,
		params.ItemCount, params.SuccessCount, params.FailCount, params.CancelledCount,
		params.EstimatedCost, params.HoldAmount, params.ActualCost, params.BalanceHoldAmount, string(subscriptionHoldAllocations),
		params.SubscriptionRateMultiplier, params.BalanceRateMultiplier, params.PlanGroupRateEnabled,
		params.BaseUnitPrice, params.GroupRateMultiplier, params.ProviderRateMultiplier,
		params.BatchDiscountMultiplier, params.HoldMultiplier, params.BillableUnitPrice, params.HoldUnitPrice,
		params.PricingSnapshotVersion,
		params.Currency, params.HoldID,
		params.IdempotencyKey, params.RequestHash, params.ManifestHash, params.RetryCount, params.SessionID, params.OutputExpiresAt,
		params.BillingMode, params.PreferredSubscriptionID,
	))
}

// createBatchImageItemWithSQL inserts the supplied indexed item and scans its
// returned metadata in the shared item column order. Its caller owns validation,
// duplicate-error translation, and transaction grouping with surrounding job
// updates.
func createBatchImageItemWithSQL(ctx context.Context, sqlq SQLExecutor, params batchimage.CreateBatchImageItemParams) (*batchimage.BatchImageItem, error) {
	return scanBatchImageItem(sqlq.QueryRowContext(ctx, `
INSERT INTO batch_image_items (
    job_id, custom_id, status, request_hash, prompt_preview, provider_source_object,
    source_line_number, source_byte_offset, source_byte_length,
    mime_type, file_extension, image_count,
    error_code, error_message, billed_amount, indexed_at
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9,
    $10, $11, $12,
    $13, $14, $15, $16
)
RETURNING `+batchImageItemColumns,
		params.JobID, params.CustomID, params.Status, params.RequestHash, params.PromptPreview, params.ProviderSourceObject,
		params.SourceLineNumber, params.SourceByteOffset, params.SourceByteLength,
		params.MimeType, params.FileExtension, params.ImageCount,
		params.ErrorCode, params.ErrorMessage, params.BilledAmount, params.IndexedAt,
	))
}

// appendBatchImageEventWithSQL inserts an event with JSON payload or SQL NULL.
// Encoding errors stop insertion; the supplied executor determines whether the
// event participates in a surrounding state update's transaction.
func appendBatchImageEventWithSQL(ctx context.Context, sqlq SQLExecutor, batchID, eventType string, payload any) error {
	var payloadArg any
	if payload != nil {
		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		payloadArg = string(payloadBytes)
	}
	_, err := sqlq.ExecContext(ctx, `
INSERT INTO batch_image_events (job_id, event_type, payload)
VALUES ($1, $2, $3)`, batchID, eventType, payloadArg)
	return err
}

// scanBatchImageJob decodes one row in batchImageJobColumns order into job
// metadata. SQL NULL becomes nil optional fields, while present zero values
// remain pointers; invalid subscription-allocation JSON fails the complete
// read rather than returning partial funding metadata.
func scanBatchImageJob(row rowScanner) (*batchimage.BatchImageJob, error) {
	var job batchimage.BatchImageJob
	var teamID, apiKeyID, providerID, groupID sql.NullInt64
	var providerJobName, providerInputRef, providerOutputRef, gcsInputURI, gcsOutputURI sql.NullString
	var parentBatchID sql.NullString
	var holdAmount, actualCost sql.NullFloat64
	var subscriptionHoldAllocationsRaw []byte
	var holdID, idempotencyKey, requestHash, manifestHash sql.NullString
	var preferredSubscriptionID sql.NullInt64
	var sessionID sql.NullString
	var outputExpiresAt, inputDeletedAt, outputDeletedAt, downloadedAt, userDeletedAt sql.NullTime
	var lastErrorCode, lastErrorMessage sql.NullString
	var submittedAt, startedAt, finishedAt, settledAt sql.NullTime

	err := row.Scan(
		&job.ID, &job.BatchID, &job.UserID, &job.BillingUserID, &teamID, &apiKeyID, &providerID, &groupID, &job.Platform, &job.Model, &job.RequestedModel, &job.InternalModel, &job.TaskName, &parentBatchID, &job.Status,
		&providerJobName, &providerInputRef, &providerOutputRef, &gcsInputURI, &gcsOutputURI,
		&job.ItemCount, &job.SuccessCount, &job.FailCount, &job.CancelledCount,
		&job.EstimatedCost, &holdAmount, &actualCost, &job.AllowanceReserved,
		&job.BalanceHoldAmount, &subscriptionHoldAllocationsRaw,
		&job.SubscriptionRateMultiplier, &job.BalanceRateMultiplier, &job.PlanGroupRateEnabled,
		&job.BaseUnitPrice, &job.GroupRateMultiplier, &job.ProviderRateMultiplier,
		&job.BatchDiscountMultiplier, &job.HoldMultiplier, &job.BillableUnitPrice, &job.HoldUnitPrice,
		&job.PricingSnapshotVersion,
		&job.Currency, &holdID,
		&idempotencyKey, &requestHash, &manifestHash,
		&job.RetryCount, &job.Version, &sessionID, &outputExpiresAt, &inputDeletedAt, &outputDeletedAt, &downloadedAt, &userDeletedAt,
		&lastErrorCode, &lastErrorMessage,
		&job.BillingMode, &preferredSubscriptionID,
		&job.CreatedAt, &job.UpdatedAt, &submittedAt, &startedAt, &finishedAt, &settledAt,
	)
	if err != nil {
		return nil, err
	}

	job.APIKeyID = batchImageNullInt64Ptr(apiKeyID)
	job.TeamID = batchImageNullInt64Ptr(teamID)
	job.ProviderID = batchImageNullInt64Ptr(providerID)
	job.GroupID = batchImageNullInt64Ptr(groupID)
	job.PreferredSubscriptionID = batchImageNullInt64Ptr(preferredSubscriptionID)
	job.ProviderJobName = batchImageNullStringPtr(providerJobName)
	job.ProviderInputRef = batchImageNullStringPtr(providerInputRef)
	job.ProviderOutputRef = batchImageNullStringPtr(providerOutputRef)
	job.ParentBatchID = batchImageNullStringPtr(parentBatchID)
	job.GCSInputURI = batchImageNullStringPtr(gcsInputURI)
	job.GCSOutputURI = batchImageNullStringPtr(gcsOutputURI)
	job.HoldAmount = batchImageNullFloat64Ptr(holdAmount)
	job.ActualCost = batchImageNullFloat64Ptr(actualCost)
	if len(subscriptionHoldAllocationsRaw) > 0 {
		if err := json.Unmarshal(subscriptionHoldAllocationsRaw, &job.SubscriptionHoldAllocations); err != nil {
			return nil, err
		}
	}
	job.HoldID = batchImageNullStringPtr(holdID)
	job.IdempotencyKey = batchImageNullStringPtr(idempotencyKey)
	job.RequestHash = batchImageNullStringPtr(requestHash)
	job.ManifestHash = batchImageNullStringPtr(manifestHash)
	job.SessionID = batchImageNullStringPtr(sessionID)
	job.OutputExpiresAt = batchImageNullTimePtr(outputExpiresAt)
	job.InputDeletedAt = batchImageNullTimePtr(inputDeletedAt)
	job.OutputDeletedAt = batchImageNullTimePtr(outputDeletedAt)
	job.DownloadedAt = batchImageNullTimePtr(downloadedAt)
	job.UserDeletedAt = batchImageNullTimePtr(userDeletedAt)
	job.LastErrorCode = batchImageNullStringPtr(lastErrorCode)
	job.LastErrorMessage = batchImageNullStringPtr(lastErrorMessage)
	job.SubmittedAt = batchImageNullTimePtr(submittedAt)
	job.StartedAt = batchImageNullTimePtr(startedAt)
	job.FinishedAt = batchImageNullTimePtr(finishedAt)
	job.SettledAt = batchImageNullTimePtr(settledAt)
	return &job, nil
}

// scanBatchImageJobs reads jobs until row iteration ends and returns scan or
// iteration failures. It leaves rows ownership with its caller, which closes
// the query; an empty result retains a nil slice.
func scanBatchImageJobs(rows *sql.Rows) ([]*batchimage.BatchImageJob, error) {
	var jobs []*batchimage.BatchImageJob
	for rows.Next() {
		job, err := scanBatchImageJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

// scanBatchImageItem decodes one row in batchImageItemColumns order into item
// metadata. Nullable references, offsets, error values, amounts, and times become
// optional pointers so absent database values retain their distinction from
// present zero values.
func scanBatchImageItem(row rowScanner) (*batchimage.BatchImageItem, error) {
	var item batchimage.BatchImageItem
	var requestHash, promptPreview, providerSourceObject sql.NullString
	var sourceLineNumber sql.NullInt64
	var sourceByteOffset, sourceByteLength sql.NullInt64
	var mimeType, fileExtension, errorCode, errorMessage sql.NullString
	var billedAmount sql.NullFloat64
	var indexedAt sql.NullTime

	err := row.Scan(
		&item.ID, &item.JobID, &item.CustomID, &item.Status, &requestHash, &promptPreview, &providerSourceObject,
		&sourceLineNumber, &sourceByteOffset, &sourceByteLength,
		&mimeType, &fileExtension, &item.ImageCount,
		&errorCode, &errorMessage, &billedAmount,
		&item.CreatedAt, &indexedAt,
	)
	if err != nil {
		return nil, err
	}

	item.RequestHash = batchImageNullStringPtr(requestHash)
	item.PromptPreview = batchImageNullStringPtr(promptPreview)
	item.ProviderSourceObject = batchImageNullStringPtr(providerSourceObject)
	item.SourceLineNumber = batchImageNullIntPtr(sourceLineNumber)
	item.SourceByteOffset = batchImageNullInt64Ptr(sourceByteOffset)
	item.SourceByteLength = batchImageNullInt64Ptr(sourceByteLength)
	item.MimeType = batchImageNullStringPtr(mimeType)
	item.FileExtension = batchImageNullStringPtr(fileExtension)
	item.ErrorCode = batchImageNullStringPtr(errorCode)
	item.ErrorMessage = batchImageNullStringPtr(errorMessage)
	item.BilledAmount = batchImageNullFloat64Ptr(billedAmount)
	item.IndexedAt = batchImageNullTimePtr(indexedAt)
	return &item, nil
}

// batchImageNullStringPtr returns nil for SQL NULL or a pointer to the present
// string value. Empty strings remain present, and the returned pointer belongs
// to this copied nullable holder.
func batchImageNullStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

// batchImageNullInt64Ptr returns nil for SQL NULL or a pointer to the present
// 64-bit integer. Zero remains present, and the returned pointer belongs to this
// copied nullable holder.
func batchImageNullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

// batchImageNullIntPtr converts a present SQL integer to Go int and returns its
// pointer, or nil for SQL NULL. The conversion follows the platform int width;
// zero remains a present value.
func batchImageNullIntPtr(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	// Convert stored line numbers to the platform's Go int width.
	i := int(v.Int64)
	return &i
}

// batchImageNullFloat64Ptr returns nil for SQL NULL or a pointer to the present
// floating-point amount. Zero remains present, allowing scanned funding fields
// to distinguish absent amounts from stored zero amounts.
func batchImageNullFloat64Ptr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}

// batchImageNullTimePtr returns nil for SQL NULL or a pointer to the present
// timestamp. It retains the scanned time value, allowing absent lifecycle events
// to remain distinct from a stored timestamp.
func batchImageNullTimePtr(v sql.NullTime) *time.Time {
	if !v.Valid {
		return nil
	}
	return &v.Time
}
