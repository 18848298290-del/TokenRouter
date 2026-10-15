package provider

import (
	"context"
	"time"
)

// scheduled_plans.go defines recurring provider test plans, retained results,
// and their storage interfaces.
// A recurring provider test stores its next execution time separately from the
// observations it produces. Plan eligibility depends on the enabled flag and a
// due next-run time, while result history carries status, response text, and
// measured latency for retention and administrator display.

// ScheduledTestPlan configures a recurring provider test and result retention.
type ScheduledTestPlan struct {
	ID             int64  `json:"id"`              // stored test plan identifier
	ProviderID     int64  `json:"provider_id"`     // tested provider record identifier
	ModelID        string `json:"model_id"`        // requested upstream model identifier
	CronExpression string `json:"cron_expression"` // recurring test cron expression
	// Enabled admits this plan to ListDue when NextRunAt is due. A disabled plan
	// remains stored for management and result-history queries.
	Enabled bool `json:"enabled"`
	// MaxResults is defaulted by CreatePlan when nonpositive and passed unchanged
	// by updates and SaveResult. PostgreSQL pruning with a nonpositive value
	// removes all of that plan's retained results.
	MaxResults int `json:"max_results"`
	// AutoRecover asks runOnePlan to invoke recovery after a success result. A
	// failed result leaves recovery to other maintenance or administrator actions.
	AutoRecover bool `json:"auto_recover"`
	// LastRunAt and NextRunAt are written together after execution. Nil LastRunAt
	// represents an absent run timestamp; nil NextRunAt leaves PostgreSQL ListDue
	// without a comparable due time for this plan.
	LastRunAt *time.Time `json:"last_run_at"` // latest recorded run time
	NextRunAt *time.Time `json:"next_run_at"` // next scheduled run time
	CreatedAt time.Time  `json:"created_at"`  // database plan creation time
	UpdatedAt time.Time  `json:"updated_at"`  // database plan modification time
}

// ScheduledTestResult records one test execution and its latency in
// milliseconds.
type ScheduledTestResult struct {
	ID     int64 `json:"id"`      // stored test result identifier
	PlanID int64 `json:"plan_id"` // owning test plan identifier
	// Status is success or failed for TestService results. runOnePlan attempts
	// automatic recovery only for the exact success string when the plan enables
	// it.
	Status       string `json:"status"`
	ResponseText string `json:"response_text"` // concatenated test content text
	ErrorMessage string `json:"error_message"` // retained test failure message
	LatencyMs    int64  `json:"latency_ms"`    // milliseconds
	// StartedAt and FinishedAt come from the TestService clock around execution;
	// LatencyMs is their difference truncated to whole milliseconds for stored
	// display.
	StartedAt  time.Time `json:"started_at"`  // test execution start time
	FinishedAt time.Time `json:"finished_at"` // test execution finish time
	CreatedAt  time.Time `json:"created_at"`  // database result creation time
}

// ScheduledTestPlanRepository stores plans and updates their execution times.
type ScheduledTestPlanRepository interface {
	// Create stores a nonnil plan and returns its persisted value. The service
	// validates cron and supplies its next-run time and creation retention default
	// before calling the repository.
	Create(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error)
	// GetByID returns the stored recurring test plan for id.
	GetByID(ctx context.Context, id int64) (*ScheduledTestPlan, error)
	// ListByProviderID returns stored test plans owned by the provider.
	ListByProviderID(ctx context.Context, providerID int64) ([]*ScheduledTestPlan, error)
	// ListDue reads enabled plans whose next-run time is at or before now. The
	// runner executes those returned plans and updates their times through
	// subsequent calls; this read leaves execution admission to the runner.
	ListDue(ctx context.Context, now time.Time) ([]*ScheduledTestPlan, error)
	// Update persists the supplied plan changes and returns the resulting value.
	// The service supplies a nonnil plan with its next-run time recalculated before
	// delegating to this repository.
	Update(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error)
	// Delete removes the selected plan through its repository implementation. The
	// PostgreSQL implementation also removes that plan's results through the
	// database foreign-key cascade and succeeds when the ID is absent.
	Delete(ctx context.Context, id int64) error
	// UpdateAfterRun stores lastRunAt and nextRunAt together after a completed
	// executor call. Result persistence and recovery happen in separate earlier
	// calls, so their outcomes remain independent of this timestamp update.
	UpdateAfterRun(ctx context.Context, id int64, lastRunAt time.Time, nextRunAt time.Time) error
}

// ScheduledTestResultRepository stores test results and prunes excess history.
type ScheduledTestResultRepository interface {
	// Create persists a nonnil test result and returns its stored value. SaveResult
	// assigns the owning PlanID before calling and applies retention in a
	// subsequent repository operation.
	Create(ctx context.Context, result *ScheduledTestResult) (*ScheduledTestResult, error)
	// ListByPlanID returns recent results for the selected recurring plan.
	ListByPlanID(ctx context.Context, planID int64, limit int) ([]*ScheduledTestResult, error)
	// PruneOldResults removes history beyond keepCount for this plan. PostgreSQL
	// uses newest creation time first and forwards the count unchanged, so
	// nonpositive retention removes all selected results.
	PruneOldResults(ctx context.Context, planID int64, keepCount int) error
}
