package usage

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

const (
	UsageCleanupStatusPending   = "pending"
	UsageCleanupStatusRunning   = "running"
	UsageCleanupStatusSucceeded = "succeeded"
	UsageCleanupStatusFailed    = "failed"
	UsageCleanupStatusCanceled  = "canceled"
)

// UsageCleanupFilters selects records for a persisted cleanup task.
// The time range is required; other filters are optional.
// Task parameters are stored as JSON.
//
// start_time and end_time use RFC3339 timestamps,
// with offsets resolved from UTC or the user's timezone before storage.
//
// Optional filter rules:
//   - nil leaves the field unrestricted.
//   - Supplied fields use exact matching.
type UsageCleanupFilters struct {
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	UserID      *int64    `json:"user_id,omitempty"`
	APIKeyID    *int64    `json:"api_key_id,omitempty"`
	ProviderID  *int64    `json:"provider_id,omitempty"`
	GroupID     *int64    `json:"group_id,omitempty"`
	TeamID      *int64    `json:"team_id,omitempty"`
	Model       *string   `json:"model,omitempty"`
	RequestType *int16    `json:"request_type,omitempty"`
	Stream      *bool     `json:"stream,omitempty"`
	BillingType *int8     `json:"billing_type,omitempty"`
}

// UsageCleanupTask tracks deletion of usage records.
// States are pending, running, succeeded, failed, and canceled.
type UsageCleanupTask struct {
	ID          int64
	Status      string
	Filters     UsageCleanupFilters
	CreatedBy   int64
	DeletedRows int64
	ErrorMsg    *string
	CanceledBy  *int64
	CanceledAt  *time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// UsageCleanupRepository persists cleanup tasks and deletes matching usage records.
type UsageCleanupRepository interface {
	CreateTask(ctx context.Context, task *UsageCleanupTask) error
	ListTasks(ctx context.Context, params pagination.PaginationParams) ([]UsageCleanupTask, *pagination.PaginationResult, error)
	// ClaimNextPendingTask claims the next executable task:
	//   - Select the oldest eligible task by creation time.
	//   - Reclaim running tasks older than staleRunningAfterSeconds to resume interrupted work.
	ClaimNextPendingTask(ctx context.Context, staleRunningAfterSeconds int64) (*UsageCleanupTask, error)
	// GetTaskStatus returns the task state or ErrCleanupTaskNotFound when absent.
	GetTaskStatus(ctx context.Context, taskID int64) (string, error)
	// UpdateTaskProgress saves deleted_rows for progress display and resumed tasks.
	UpdateTaskProgress(ctx context.Context, taskID int64, deletedRows int64) error
	// CancelTask marks pending or running tasks canceled.
	CancelTask(ctx context.Context, taskID int64, canceledBy int64) (bool, error)
	MarkTaskSucceeded(ctx context.Context, taskID int64, deletedRows int64) error
	MarkTaskFailed(ctx context.Context, taskID int64, deletedRows int64, errorMsg string) error
	DeleteUsageLogsBatch(ctx context.Context, filters UsageCleanupFilters, limit int) (int64, error)
}
