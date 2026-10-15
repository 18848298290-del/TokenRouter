package provider

import (
	"context"
	"fmt"
	"time"
)

// scheduled_test_service.go manages recurring provider test plans and saves
// results before pruning retained history.
// Recurring provider tests need a calculated next-run time before their plan is
// stored. Creation validates cron and supplies a retention default, while
// updates recalculate the schedule using the caller's retention value. The
// supplied plan is mutated before persistence, so a repository error can leave
// its calculated fields visible to the caller.
//
// A result creation call precedes the separate history-pruning call. A creation
// error returns before pruning; a retention error returns after creation has
// reported success. Repository outcomes determine the persisted history; this
// service returns creation and pruning errors directly. Result listing has its
// own default limit. Background execution and the eventual plan timestamp update
// belong to ScheduledTestRunnerService, which calls these management operations.

// ScheduledTestService manages recurring provider tests and result retention.
type ScheduledTestService struct {
	options ScheduledTestOptions // stored clock and schedule callbacks
	// planRepo and resultRepo are retained for synchronous management and runner
	// calls. Their owner keeps both repositories usable until those calls finish.
	planRepo   ScheduledTestPlanRepository   // plan management storage interface
	resultRepo ScheduledTestResultRepository // test result storage interface
}

// ScheduledTestOptions supplies time and cron calculation for plan management
// calls.
type ScheduledTestOptions struct {
	// Now and NextRun are required by CreatePlan and UpdatePlan. Now supplies the
	// calculation origin, and NextRun validates the expression and computes its
	// next execution time before either operation writes the plan.
	Now     func() time.Time                           // current schedule calculation time
	NextRun func(string, time.Time) (time.Time, error) // cron next-time calculation callback
}

// NewScheduledTestService retains the supplied repositories and copies the
// callback options for plan management. Construct it before runner startup;
// CreatePlan and UpdatePlan require usable Now and NextRun callbacks, while each
// operation requires its corresponding repository.
func NewScheduledTestService(
	planRepo ScheduledTestPlanRepository,
	resultRepo ScheduledTestResultRepository,
	options ScheduledTestOptions,
) *ScheduledTestService {
	return &ScheduledTestService{
		options:    options,
		planRepo:   planRepo,
		resultRepo: resultRepo,
	}
}

// CreatePlan validates cron, calculates the next run, and persists a nonnil
// caller-owned plan. Validation failure returns before mutation; after success,
// NextRunAt is replaced and nonpositive MaxResults becomes fifty before storage.
// Those input changes remain visible if persistence fails.
func (s *ScheduledTestService) CreatePlan(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	nextRun, err := s.options.NextRun(plan.CronExpression, s.options.Now())
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression: %w", err)
	}
	plan.NextRunAt = &nextRun

	if plan.MaxResults <= 0 {
		plan.MaxResults = 50
	}

	return s.planRepo.Create(ctx, plan)
}

// GetPlan returns the stored recurring plan or its repository error.
func (s *ScheduledTestService) GetPlan(ctx context.Context, id int64) (*ScheduledTestPlan, error) {
	return s.planRepo.GetByID(ctx, id)
}

// ListPlansByProvider returns recurring test plans stored for this provider.
func (s *ScheduledTestService) ListPlansByProvider(ctx context.Context, providerID int64) ([]*ScheduledTestPlan, error) {
	return s.planRepo.ListByProviderID(ctx, providerID)
}

// UpdatePlan validates cron, replaces NextRunAt, and persists a nonnil
// caller-owned plan. MaxResults is passed through unchanged, and the calculated
// input timestamp remains visible to the caller if the repository update fails.
func (s *ScheduledTestService) UpdatePlan(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	nextRun, err := s.options.NextRun(plan.CronExpression, s.options.Now())
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression: %w", err)
	}
	plan.NextRunAt = &nextRun

	return s.planRepo.Update(ctx, plan)
}

// DeletePlan removes a stored plan through the plan repository. The PostgreSQL
// implementation cascades that deletion to result history; storage errors are
// returned directly for the management caller to handle.
func (s *ScheduledTestService) DeletePlan(ctx context.Context, id int64) error {
	return s.planRepo.Delete(ctx, id)
}

// ListResults returns recent observations for the plan with the requested
// repository limit. A nonpositive limit selects fifty results; repository
// ordering, empty-history representation, and read errors are forwarded to the
// caller.
func (s *ScheduledTestService) ListResults(ctx context.Context, planID int64, limit int) ([]*ScheduledTestResult, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.resultRepo.ListByPlanID(ctx, planID, limit)
}

// SaveResult assigns planID to a nonnil result before calling Create, then
// requests history pruning after creation reports success. A creation error
// returns before pruning. maxResults reaches the pruning implementation
// unchanged.
//
// Creation and retention are separate repository calls, so a returned pruning
// error follows reported creation success. The repository determines the
// resulting stored history; this method returns that error immediately. With
// PostgreSQL, Create returns generated ID and creation time in a separate result
// value discarded here, so the supplied input keeps its existing metadata apart
// from PlanID assigned before the call.
func (s *ScheduledTestService) SaveResult(ctx context.Context, planID int64, maxResults int, result *ScheduledTestResult) error {
	result.PlanID = planID
	// Insert before retention so the completed observation can participate in
	// history ranking. These are separate store operations, and pruning starts only
	// after creation returns success.
	if _, err := s.resultRepo.Create(ctx, result); err != nil {
		return err
	}
	return s.resultRepo.PruneOldResults(ctx, planID, maxResults)
}
