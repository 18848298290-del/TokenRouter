package provider

import (
	"context"
	"fmt"
	"time"
)

// ScheduledTestService manages recurring provider tests and result retention.
type ScheduledTestService struct {
	options    ScheduledTestOptions
	planRepo   ScheduledTestPlanRepository
	resultRepo ScheduledTestResultRepository
}

type ScheduledTestOptions struct {
	Now     func() time.Time
	NextRun func(string, time.Time) (time.Time, error)
}

// NewScheduledTestService stores dependencies without starting background work.
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

// CreatePlan validates the cron schedule, computes the next run, and persists the plan.
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

// GetPlan reads a plan by ID.
func (s *ScheduledTestService) GetPlan(ctx context.Context, id int64) (*ScheduledTestPlan, error) {
	return s.planRepo.GetByID(ctx, id)
}

// ListPlansByProvider returns all plans for the provider.
func (s *ScheduledTestService) ListPlansByProvider(ctx context.Context, providerID int64) ([]*ScheduledTestPlan, error) {
	return s.planRepo.ListByProviderID(ctx, providerID)
}

// UpdatePlan validates the cron schedule and updates the next execution time.
func (s *ScheduledTestService) UpdatePlan(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	nextRun, err := s.options.NextRun(plan.CronExpression, s.options.Now())
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression: %w", err)
	}
	plan.NextRunAt = &nextRun

	return s.planRepo.Update(ctx, plan)
}

// DeletePlan deletes the plan; the database cascades deletion to its test results.
func (s *ScheduledTestService) DeletePlan(ctx context.Context, id int64) error {
	return s.planRepo.Delete(ctx, id)
}

// ListResults returns recent test results, defaulting to 50 when limit is nonpositive.
func (s *ScheduledTestService) ListResults(ctx context.Context, planID int64, limit int) ([]*ScheduledTestResult, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.resultRepo.ListByPlanID(ctx, planID, limit)
}

// SaveResult stores a result, then prunes excess history; a prune failure leaves the saved result in place.
func (s *ScheduledTestService) SaveResult(ctx context.Context, planID int64, maxResults int, result *ScheduledTestResult) error {
	result.PlanID = planID
	if _, err := s.resultRepo.Create(ctx, result); err != nil {
		return err
	}
	return s.resultRepo.PruneOldResults(ctx, planID, maxResults)
}
