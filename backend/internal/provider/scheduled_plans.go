package provider

import (
	"context"
	"time"
)

// ScheduledTestPlan configures a recurring provider test and result retention.
type ScheduledTestPlan struct {
	ID             int64      `json:"id"`
	ProviderID     int64      `json:"provider_id"`
	ModelID        string     `json:"model_id"`
	CronExpression string     `json:"cron_expression"`
	Enabled        bool       `json:"enabled"`
	MaxResults     int        `json:"max_results"`
	AutoRecover    bool       `json:"auto_recover"`
	LastRunAt      *time.Time `json:"last_run_at"`
	NextRunAt      *time.Time `json:"next_run_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// ScheduledTestResult records one test execution and its latency in milliseconds.
type ScheduledTestResult struct {
	ID           int64     `json:"id"`
	PlanID       int64     `json:"plan_id"`
	Status       string    `json:"status"`
	ResponseText string    `json:"response_text"`
	ErrorMessage string    `json:"error_message"`
	LatencyMs    int64     `json:"latency_ms"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	CreatedAt    time.Time `json:"created_at"`
}

// ScheduledTestPlanRepository stores plans and updates their execution times.
type ScheduledTestPlanRepository interface {
	Create(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error)
	GetByID(ctx context.Context, id int64) (*ScheduledTestPlan, error)
	ListByProviderID(ctx context.Context, providerID int64) ([]*ScheduledTestPlan, error)
	ListDue(ctx context.Context, now time.Time) ([]*ScheduledTestPlan, error)
	Update(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error)
	Delete(ctx context.Context, id int64) error
	UpdateAfterRun(ctx context.Context, id int64, lastRunAt time.Time, nextRunAt time.Time) error
}

// ScheduledTestResultRepository stores test results and prunes excess history.
type ScheduledTestResultRepository interface {
	Create(ctx context.Context, result *ScheduledTestResult) (*ScheduledTestResult, error)
	ListByPlanID(ctx context.Context, planID int64, limit int) ([]*ScheduledTestResult, error)
	PruneOldResults(ctx context.Context, planID int64, keepCount int) error
}
