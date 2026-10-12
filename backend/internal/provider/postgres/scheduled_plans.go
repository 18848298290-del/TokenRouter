package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Recurring provider tests store their execution schedule separately from
// result history. Plan reads select enabled due entries or provider-specific
// lists, and result retention ranks each plan's newest observations by
// creation time before deleting entries beyond the requested count. The
// provider service supplies cron validation and default retention values.
//
// Plan timestamps can be absent, so due selection requires an enabled plan with
// a comparable next-run time. Result creation and pruning are independent service
// calls: retention failure leaves the newly inserted result available. History
// order uses creation time without an ID tie-breaker, and deleting a plan delegates
// result removal to its database foreign-key cascade.

// scheduledTestPlanRepository stores scheduled provider-test plans on the
// supplied database.
type scheduledTestPlanRepository struct {
	// db remains application-owned; keep it open until repository reads and writes
	// finish, and close its pool through the application's storage lifecycle.
	db *sql.DB
}

// scheduledTestResultRepository stores retained provider-test results on the
// supplied database.
type scheduledTestResultRepository struct {
	// db remains application-owned; keep it open until repository reads and writes
	// finish, and close its pool through the application's storage lifecycle.
	db *sql.DB
}

// scheduledScannable decodes plan columns from either one row or an iterator.
type scheduledScannable interface {
	// Scan assigns ordered column values to the supplied destinations and reports
	// conversion or row-read failure. Callers pass the same plan column order used
	// by the owning SELECT or RETURNING statement.
	Scan(dest ...any) error
}

// NewScheduledTestPlanRepository retains the caller's database for recurring
// provider-test plan storage. It opens no background work or additional
// connection owner; keep db available until repository calls have finished
// and close it through application lifecycle.
func NewScheduledTestPlanRepository(db *sql.DB) provider.ScheduledTestPlanRepository {
	return &scheduledTestPlanRepository{db: db}
}

// Create inserts a nonnil plan and returns the database-generated fields in a
// new plan value. The service supplies cron validation, retention defaults,
// and next-run calculation before calling this SQL adapter.
func (r *scheduledTestPlanRepository) Create(ctx context.Context, plan *provider.ScheduledTestPlan) (*provider.ScheduledTestPlan, error) {
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO scheduled_test_plans (provider_id, model_id, cron_expression, enabled, max_results, auto_recover, next_run_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW())
		RETURNING id, provider_id, model_id, cron_expression, enabled, max_results, auto_recover, last_run_at, next_run_at, created_at, updated_at
	`, plan.ProviderID, plan.ModelID, plan.CronExpression, plan.Enabled, plan.MaxResults, plan.AutoRecover, plan.NextRunAt)
	return scanScheduledPlan(row)
}

// GetByID reads one stored plan and returns sql.ErrNoRows when absent. The
// provider service determines authorization and user-facing error handling;
// the adapter scans the plan's nullable execution times as stored.
func (r *scheduledTestPlanRepository) GetByID(ctx context.Context, id int64) (*provider.ScheduledTestPlan, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, provider_id, model_id, cron_expression, enabled, max_results, auto_recover, last_run_at, next_run_at, created_at, updated_at
		FROM scheduled_test_plans WHERE id = $1
	`, id)
	return scanScheduledPlan(row)
}

// ListByProviderID returns a provider's plans ordered newest by creation
// time. No rows produce a nil slice; equal creation times have no additional
// tie-breaker, and row resources are closed before return.
func (r *scheduledTestPlanRepository) ListByProviderID(ctx context.Context, providerID int64) ([]*provider.ScheduledTestPlan, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, provider_id, model_id, cron_expression, enabled, max_results, auto_recover, last_run_at, next_run_at, created_at, updated_at
		FROM scheduled_test_plans WHERE provider_id = $1
		ORDER BY created_at DESC
	`, providerID)
	if err != nil {
		return nil, err
	}
	// Release the provider-plan iterator after scanning newest-first results,
	// keeping this borrowed database usable for the service's later plan operations.
	defer func() { _ = rows.Close() }()
	return scanScheduledPlans(rows)
}

// ListDue returns enabled plans whose nonnull next_run_at is at or before
// now, ordered by that timestamp. A nil slice represents no due rows;
// nullable unscheduled plans remain excluded by the SQL comparison.
func (r *scheduledTestPlanRepository) ListDue(ctx context.Context, now time.Time) ([]*provider.ScheduledTestPlan, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, provider_id, model_id, cron_expression, enabled, max_results, auto_recover, last_run_at, next_run_at, created_at, updated_at
		FROM scheduled_test_plans
		WHERE enabled = true AND next_run_at <= $1
		ORDER BY next_run_at ASC
	`, now)
	if err != nil {
		return nil, err
	}
	// Close the due-plan selection after consuming it; runner execution uses
	// returned plan values and does not keep the scan connection occupied
	// during probes.
	defer func() { _ = rows.Close() }()
	return scanScheduledPlans(rows)
}

// Update writes the editable schedule fields and returns the resulting plan
// in a new value. Callers supply a nonnil plan with next_run_at already
// calculated; provider ownership and last_run_at retain their stored values,
// and an absent ID returns sql.ErrNoRows.
func (r *scheduledTestPlanRepository) Update(ctx context.Context, plan *provider.ScheduledTestPlan) (*provider.ScheduledTestPlan, error) {
	row := r.db.QueryRowContext(ctx, `
		UPDATE scheduled_test_plans
		SET model_id = $2, cron_expression = $3, enabled = $4, max_results = $5, auto_recover = $6, next_run_at = $7, updated_at = NOW()
		WHERE id = $1
		RETURNING id, provider_id, model_id, cron_expression, enabled, max_results, auto_recover, last_run_at, next_run_at, created_at, updated_at
	`, plan.ID, plan.ModelID, plan.CronExpression, plan.Enabled, plan.MaxResults, plan.AutoRecover, plan.NextRunAt)
	return scanScheduledPlan(row)
}

// Delete removes a plan by ID and succeeds when no row matches. The database
// foreign key cascades to its results, so the service can remove retained
// history through this single plan operation.
func (r *scheduledTestPlanRepository) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM scheduled_test_plans WHERE id = $1`, id)
	return err
}

// UpdateAfterRun records the supplied last and next execution times for an
// ID. It writes both schedule observations together and succeeds when no row
// matches, leaving result creation and pruning to their separate service
// calls.
func (r *scheduledTestPlanRepository) UpdateAfterRun(ctx context.Context, id int64, lastRunAt time.Time, nextRunAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE scheduled_test_plans SET last_run_at = $2, next_run_at = $3, updated_at = NOW() WHERE id = $1
	`, id, lastRunAt, nextRunAt)
	return err
}

// NewScheduledTestResultRepository retains the caller's database for
// provider-test result history. The service owns result retention decisions,
// and the application keeps this borrowed database open until repository use
// has finished.
func NewScheduledTestResultRepository(db *sql.DB) provider.ScheduledTestResultRepository {
	return &scheduledTestResultRepository{db: db}
}

// Create inserts a nonnil result and returns its generated ID and creation
// timestamp in a new result value. The service fills PlanID before
// persistence and performs any history pruning through a later independent
// call.
func (r *scheduledTestResultRepository) Create(ctx context.Context, result *provider.ScheduledTestResult) (*provider.ScheduledTestResult, error) {
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO scheduled_test_results (plan_id, status, response_text, error_message, latency_ms, started_at, finished_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		RETURNING id, plan_id, status, response_text, error_message, latency_ms, started_at, finished_at, created_at
	`, result.PlanID, result.Status, result.ResponseText, result.ErrorMessage, result.LatencyMs, result.StartedAt, result.FinishedAt)

	out := &provider.ScheduledTestResult{}
	if err := row.Scan(
		&out.ID, &out.PlanID, &out.Status, &out.ResponseText, &out.ErrorMessage,
		&out.LatencyMs, &out.StartedAt, &out.FinishedAt, &out.CreatedAt,
	); err != nil {
		return nil, err
	}
	return out, nil
}

// ListByPlanID returns newest-first result rows with the supplied SQL limit.
// The service defaults nonpositive limits before calling; this adapter
// forwards limit unchanged, and an empty history produces a nil slice. Scan
// failures return no slice; iteration failures can accompany earlier decoded rows.
func (r *scheduledTestResultRepository) ListByPlanID(ctx context.Context, planID int64, limit int) ([]*provider.ScheduledTestResult, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, plan_id, status, response_text, error_message, latency_ms, started_at, finished_at, created_at
		FROM scheduled_test_results
		WHERE plan_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, planID, limit)
	if err != nil {
		return nil, err
	}
	// Close the history iterator after scanning the requested page, so result
	// display does not retain a connection needed by independent creation or pruning.
	defer func() { _ = rows.Close() }()

	var results []*provider.ScheduledTestResult
	for rows.Next() {
		r := &provider.ScheduledTestResult{}
		if err := rows.Scan(
			&r.ID, &r.PlanID, &r.Status, &r.ResponseText, &r.ErrorMessage,
			&r.LatencyMs, &r.StartedAt, &r.FinishedAt, &r.CreatedAt,
		); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// PruneOldResults removes rows ranked after keepCount within the requested
// plan's newest-first creation order. The supplied count is used unchanged,
// so zero or negative values remove all results in that selected plan.
//
// The row-number selection compares stored creation times and has no added ID
// tie-breaker for equal timestamps. Result creation belongs to the service's
// earlier independent call, so a pruning failure leaves its saved result in
// place. Other plans are excluded before ranks are selected for deletion.
func (r *scheduledTestResultRepository) PruneOldResults(ctx context.Context, planID int64, keepCount int) error {
	// Rank the selected plan's stored history before deletion; service result creation
	// happens in a separate call and remains saved if this retention statement fails.
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM scheduled_test_results
		WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY plan_id ORDER BY created_at DESC) AS rn
				FROM scheduled_test_results
				WHERE plan_id = $1
			) ranked
			WHERE rn > $2
		)
	`, planID, keepCount)
	return err
}

// scanScheduledPlan decodes a fixed-order row into a newly allocated
// scheduled plan.
func scanScheduledPlan(row scheduledScannable) (*provider.ScheduledTestPlan, error) {
	p := &provider.ScheduledTestPlan{}
	if err := row.Scan(
		&p.ID, &p.ProviderID, &p.ModelID, &p.CronExpression, &p.Enabled, &p.MaxResults, &p.AutoRecover,
		&p.LastRunAt, &p.NextRunAt, &p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return p, nil
}

// scanScheduledPlans consumes rows in their supplied order and returns a nil
// slice when no plan is present. The caller closes rows; a scan error returns
// nil, while an iteration error can accompany the plans already decoded.
func scanScheduledPlans(rows *sql.Rows) ([]*provider.ScheduledTestPlan, error) {
	var plans []*provider.ScheduledTestPlan
	for rows.Next() {
		p, err := scanScheduledPlan(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	return plans, rows.Err()
}
