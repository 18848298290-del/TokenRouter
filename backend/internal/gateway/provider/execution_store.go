package provider

import (
	"context"
	"time"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// ExecutionProviderStore reads and updates providers during execution.
type ExecutionProviderStore interface {
	GetByID(ctx context.Context, id int64) (*ExecutionProvider, error)
	// GetByIDs performs one batch lookup and omits missing IDs.
	GetByIDs(ctx context.Context, ids []int64) ([]*ExecutionProvider, error)

	Update(ctx context.Context, provider *ExecutionProvider) error

	ListByPlatform(ctx context.Context, platform string) ([]ExecutionProvider, error)

	UpdateLastUsed(ctx context.Context, id int64) error
	BatchUpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error
	SetError(ctx context.Context, id int64, errorMsg string) error
	ClearError(ctx context.Context, id int64) error

	ListSchedulable(ctx context.Context) ([]ExecutionProvider, error)
	ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]ExecutionProvider, error)
	ListSchedulableByPlatform(ctx context.Context, platform string) ([]ExecutionProvider, error)
	ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]ExecutionProvider, error)
	ListSchedulableByPlatforms(ctx context.Context, platforms []string) ([]ExecutionProvider, error)
	ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]ExecutionProvider, error)
	ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]ExecutionProvider, error)
	ListSchedulableUngroupedByPlatforms(ctx context.Context, platforms []string) ([]ExecutionProvider, error)
	// ListModelAvailabilityCandidates returns providers persistently configured as active and schedulable.
	// Transient rate limits, overload, scheduling pauses, and expiry windows do not filter this diagnostic set.
	// With nil groupID, includeGrouped selects all matching providers or only ungrouped providers.
	ListModelAvailabilityCandidates(ctx context.Context, groupID *int64, platforms []string, includeGrouped bool) ([]ExecutionProvider, error)

	SetRateLimited(ctx context.Context, id int64, resetAt time.Time) error
	SetModelRateLimit(ctx context.Context, id int64, scope string, resetAt time.Time, reason ...string) error
	SetOverloaded(ctx context.Context, id int64, until time.Time) error
	SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error
	ClearTempUnschedulable(ctx context.Context, id int64) error
	ClearRateLimit(ctx context.Context, id int64) error
	ClearAntigravityQuotaScopes(ctx context.Context, id int64) error
	ClearModelRateLimits(ctx context.Context, id int64) error
	UpdateSessionWindow(ctx context.Context, id int64, start, end *time.Time, status string) error
	// UpdateSessionWindowEnd writes the five-hour window end from active polling.
	// It leaves status recorded by the request path unchanged.
	UpdateSessionWindowEnd(ctx context.Context, id int64, end time.Time) error
	UpdateExtra(ctx context.Context, id int64, updates map[string]any) error

	// IncrementQuotaUsed atomically increments total, daily, and weekly API Key provider usage.
	IncrementQuotaUsed(ctx context.Context, id int64, amount float64) error
	BulkUpdate(ctx context.Context, ids []int64, updates providercore.ProviderBulkUpdate) (int64, error)
}
