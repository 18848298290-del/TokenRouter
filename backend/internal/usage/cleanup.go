package usage

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// Administrator usage cleanup persists its filters and progress so interrupted
// tasks can be reclaimed. Cleanup uses an inclusive start and end, while
// ordinary report queries use exclusive ends; the worker requests bucket repair
// after records have been deleted.

// Cleanup status labels track task admission, execution, and terminal outcomes
// for administrator lists, cancellation checks, and stale-running reclamation.
const (
	UsageCleanupStatusPending   = "pending"   // persisted and awaiting execution
	UsageCleanupStatusRunning   = "running"   // claimed for deletion work
	UsageCleanupStatusSucceeded = "succeeded" // scan completed and reported successful
	UsageCleanupStatusFailed    = "failed"    // execution failed with diagnostic text
	UsageCleanupStatusCanceled  = "canceled"  // administrator cancellation was recorded
)

// UsageCleanupFilters contains the time range and optional exact-match inputs
// persisted with a deletion task. The service sanitizes unsupported optional
// values before storing them; the PostgreSQL deletion predicate includes both
// time endpoints.
type UsageCleanupFilters struct {
	// StartTime and EndTime are required instants stored as timestamp JSON.
	// Deletion includes both endpoints. For positive-width ranges, aggregate
	// repair includes the bucket beginning exactly at EndTime; equal endpoints
	// select one instant, but the aggregation repair method returns a no-op.
	StartTime  time.Time `json:"start_time"`            // inclusive deletion start instant
	EndTime    time.Time `json:"end_time"`              // inclusive deletion end instant
	UserID     *int64    `json:"user_id,omitempty"`     // optional acting user selector
	APIKeyID   *int64    `json:"api_key_id,omitempty"`  // optional API key selector
	ProviderID *int64    `json:"provider_id,omitempty"` // optional execution provider selector
	GroupID    *int64    `json:"group_id,omitempty"`    // optional routed group selector
	TeamID     *int64    `json:"team_id,omitempty"`     // optional owning team selector
	Model      *string   `json:"model,omitempty"`       // optional stored model selector
	// RequestType takes precedence over Stream after service sanitization. A valid
	// explicit category clears Stream so older transport flags cannot further
	// restrict the selected request category.
	RequestType *int16 `json:"request_type,omitempty"` // optional recorded request category
	Stream      *bool  `json:"stream,omitempty"`       // optional legacy stream selector
	BillingType *int8  `json:"billing_type,omitempty"` // optional funding category selector
}

// UsageCleanupTask records administrator deletion state, progress, filters, and
// lifecycle timestamps.
type UsageCleanupTask struct {
	ID        int64               // persisted cleanup task ID
	Status    string              // current cleanup task label
	Filters   UsageCleanupFilters // persisted deletion selection inputs
	CreatedBy int64               // task creator user ID
	// DeletedRows is saved after successful deletion batches and reused when stale
	// running tasks are reclaimed. A progress-write failure can leave it behind
	// actual committed deletions.
	DeletedRows int64   // reported cumulative deletion count
	ErrorMsg    *string // optional execution failure text
	// CanceledBy and CanceledAt record administrator cancellation; StartedAt and
	// FinishedAt describe execution. Reclaiming a stale running task replaces
	// StartedAt and clears the previous finish and error outcome.
	CanceledBy *int64     // optional canceling user ID
	CanceledAt *time.Time // optional recorded cancellation instant
	StartedAt  *time.Time // latest task claim instant
	FinishedAt *time.Time // optional terminal result instant
	CreatedAt  time.Time  // task persistence creation instant
	UpdatedAt  time.Time  // latest task update instant
}

// UsageCleanupRepository persists cleanup tasks and deletes matching usage records.
type UsageCleanupRepository interface {
	// CreateTask persists task filters and the initial progress fields, then fills
	// its ID and creation timestamps. The supplied task remains mutable for that
	// readback; nil input is accepted by the PostgreSQL implementation.
	CreateTask(ctx context.Context, task *UsageCleanupTask) error
	// ListTasks returns cleanup tasks ordered newest first together with exact
	// pagination metadata. Filter decoding errors fail the read so an
	// administrator is not shown a task with incomplete deletion parameters.
	ListTasks(ctx context.Context, params pagination.PaginationParams) ([]UsageCleanupTask, *pagination.PaginationResult, error)
	// ClaimNextPendingTask claims the oldest eligible pending or stale running
	// task and retains its deletion count. Nonpositive stale timeouts select
	// thirty minutes; no eligible task returns nil successfully. Filter decoding
	// follows the SQL claim, so a decoding failure can leave the task running
	// until stale-task reclamation.
	ClaimNextPendingTask(ctx context.Context, staleRunningAfterSeconds int64) (*UsageCleanupTask, error)
	// GetTaskStatus returns the persisted state used for cancellation checks.
	// Missing tasks return ErrCleanupTaskNotFound; callers distinguish that case
	// from repository failures before deciding whether execution may continue.
	GetTaskStatus(ctx context.Context, taskID int64) (string, error)
	// UpdateTaskProgress stores the cumulative successful deletion count for
	// administrator display and resumption. The write is separate from deleting a
	// batch, so callers handle progress errors after records may already be gone.
	UpdateTaskProgress(ctx context.Context, taskID int64, deletedRows int64) error
	// CancelTask conditionally marks a pending or running task canceled and
	// records the operator. False means no eligible state was updated; callers can
	// reread to recognize a concurrent cancellation.
	CancelTask(ctx context.Context, taskID int64, canceledBy int64) (bool, error)
	// MarkTaskSucceeded stores the completed scan outcome and cumulative deletion
	// count. This task-row update follows the deletion batches and can fail
	// independently after their record changes have persisted. PostgreSQL writes
	// the status even if cancellation was recorded after the caller's last check.
	MarkTaskSucceeded(ctx context.Context, taskID int64, deletedRows int64) error
	// MarkTaskFailed stores an execution failure together with the cumulative
	// deletion count and error text. Earlier deleted batches remain deleted, and
	// callers still arrange aggregation repair for that affected range. PostgreSQL
	// replaces the status even if cancellation occurred after the caller's check.
	MarkTaskFailed(ctx context.Context, taskID int64, deletedRows int64, errorMsg string) error
	// DeleteUsageLogsBatch deletes up to limit records matching the inclusive time
	// range and optional selectors. Callers provide a positive limit, retain the
	// returned count in cumulative progress, and arrange aggregate repair after
	// records have been deleted.
	DeleteUsageLogsBatch(ctx context.Context, filters UsageCleanupFilters, limit int) (int64, error)
}
