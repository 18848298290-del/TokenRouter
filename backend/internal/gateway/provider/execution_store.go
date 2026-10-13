package provider

import (
	"context"
	"time"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// Gateway execution reads hydrated provider targets and writes activity,
// credentials, and health through an app-owned store. Eligibility queries
// exclude current persistent and transient scheduling failures, while
// model-availability diagnosis deliberately reads a broader persistent
// candidate set; request-specific model and protocol rules remain with their
// consumers.

// ExecutionProviderStore supplies persistent provider reads and
// execution-state writes for gateway adapters.
type ExecutionProviderStore interface {
	// GetByID loads one provider as a request execution target by its persistent
	// ID.
	GetByID(ctx context.Context, id int64) (*ExecutionProvider, error)
	// GetByIDs performs a batch target lookup and omits missing IDs from the
	// returned records. Consumers associate results by ID, since an omitted entry
	// cannot represent the corresponding requested position.
	GetByIDs(ctx context.Context, ids []int64) ([]*ExecutionProvider, error)

	// Update persists the supplied execution target's provider configuration
	// through the bound store.
	Update(ctx context.Context, provider *ExecutionProvider) error

	// ListByPlatform lists active providers for a platform in stored priority
	// order. Persistent scheduling switches and transient health still require
	// caller checks before these targets can execute a request.
	ListByPlatform(ctx context.Context, platform string) ([]ExecutionProvider, error)

	// UpdateLastUsed records current provider activity for later selection
	// ordering.
	UpdateLastUsed(ctx context.Context, id int64) error
	// BatchUpdateLastUsed records supplied provider activity timestamps in one
	// batch. Empty input succeeds; the app store assigns the supplied times
	// directly, so callers decide observation ordering before constructing
	// updates.
	BatchUpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error
	// SetError records provider error status, its message, and a disabled
	// scheduling switch. Recovery through ClearError restores active status and
	// clears the message while leaving that scheduling switch unchanged.
	SetError(ctx context.Context, id int64, errorMsg string) error
	// ClearError restores active provider status and clears the error message.
	// Stored scheduling switches and transient health windows continue to
	// determine execution eligibility after this status recovery.
	ClearError(ctx context.Context, id int64) error

	// ListSchedulable lists providers passing persistent status, scheduling
	// switch, expiry, and transient health filters. Model and request-specific
	// capability checks remain with selection after these hydrated targets are
	// read.
	ListSchedulable(ctx context.Context) ([]ExecutionProvider, error)
	// ListSchedulableByGroupID lists eligible members of groupID in provider
	// priority and ID order. Request selection still applies model, protocol, and
	// platform-specific policy to each returned target.
	ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]ExecutionProvider, error)
	// ListSchedulableByPlatform lists hydrated eligible providers belonging to
	// platform. Its persistent and transient filters provide a candidate set;
	// request model and protocol eligibility remain later selection decisions.
	ListSchedulableByPlatform(ctx context.Context, platform string) ([]ExecutionProvider, error)
	// ListSchedulableByGroupIDAndPlatform narrows eligible group members to
	// platform before returning execution targets. Membership and provider
	// priority determine the stored candidates; request model policy is evaluated
	// afterward.
	ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]ExecutionProvider, error)
	// ListSchedulableByPlatforms lists eligible providers from the selected
	// platforms. An empty platform list returns nil without a query, allowing the
	// caller to represent an empty permitted platform set.
	ListSchedulableByPlatforms(ctx context.Context, platforms []string) ([]ExecutionProvider, error)
	// ListSchedulableByGroupIDAndPlatforms reads eligible group members on any
	// supplied platform in priority and ID order. Empty platforms return nil;
	// model and protocol checks still use each request's route.
	ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]ExecutionProvider, error)
	// ListSchedulableUngroupedByPlatform reads eligible targets on platform that
	// have no group memberships. These records are available to dedicated
	// ungrouped consumers; ordinary admitted group selection uses membership
	// queries.
	ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]ExecutionProvider, error)
	// ListSchedulableUngroupedByPlatforms reads eligible targets on selected
	// platforms with no group memberships. Empty platforms return nil, and
	// returned targets still need the consumer's model and capability checks.
	ListSchedulableUngroupedByPlatforms(ctx context.Context, platforms []string) ([]ExecutionProvider, error)
	// ListModelAvailabilityCandidates returns persistently active, schedulable
	// targets for model diagnosis. Transient pause, expiry, overload, and
	// rate-limit windows remain unfiltered; a supplied group selects its members,
	// otherwise includeGrouped chooses all matching records or ungrouped records.
	ListModelAvailabilityCandidates(ctx context.Context, groupID *int64, platforms []string, includeGrouped bool) ([]ExecutionProvider, error)

	// SetRateLimited stores provider rate-limit observation and reset time for
	// subsequent eligibility reads. The app store replaces the deadline, so
	// callers use a conditional extension capability when older observations must
	// not shorten it.
	SetRateLimited(ctx context.Context, id int64, resetAt time.Time) error
	// SetModelRateLimit writes one named model or shared scope window while
	// retaining other scope entries. Empty scope succeeds without a write, and
	// the first optional reason supplies the recorded explanation.
	SetModelRateLimit(ctx context.Context, id int64, scope string, resetAt time.Time, reason ...string) error
	// SetOverloaded stores an overload deadline used by provider eligibility
	// checks. The supplied time replaces the prior value, so the caller
	// determines the useful window for its observation.
	SetOverloaded(ctx context.Context, id int64, until time.Time) error
	// SetTempUnschedulable extends a temporary pause when until is later than the
	// stored deadline. A retained longer or equal pause keeps its metadata,
	// preventing shorter concurrent failures from weakening it.
	SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error
	// ClearTempUnschedulable clears the stored temporary scheduling pause and its
	// reason.
	ClearTempUnschedulable(ctx context.Context, id int64) error
	// ClearRateLimit clears global rate-limit and overload windows used during
	// provider eligibility checks. The scheduling switch and temporary pause
	// remain separate state that can still keep the provider unavailable.
	ClearRateLimit(ctx context.Context, id int64) error
	// ClearAntigravityQuotaScopes removes stored Antigravity quota-scope
	// observations for the provider.
	ClearAntigravityQuotaScopes(ctx context.Context, id int64) error
	// ClearModelRateLimits removes the stored collection of model and
	// shared-scope limit windows.
	ClearModelRateLimits(ctx context.Context, id int64) error
	// UpdateSessionWindow records session status and each nonnil supplied window
	// timestamp. Nil start or end retains its column, allowing a partial
	// observation to update status without inventing a missing time.
	UpdateSessionWindow(ctx context.Context, id int64, start, end *time.Time, status string) error
	// UpdateSessionWindowEnd writes the observed five-hour window end from active
	// polling. Request-path session status remains unchanged, so a polling
	// timestamp cannot replace the status already observed during execution.
	UpdateSessionWindowEnd(ctx context.Context, id int64, end time.Time) error
	// UpdateExtra merges supplied provider Extra keys into their current stored
	// values. The app adapter delegates deprecated-key filtering and publication
	// to provider storage, which can mutate the supplied update map.
	UpdateExtra(ctx context.Context, id int64, updates map[string]any) error

	// IncrementQuotaUsed atomically increments the provider's total, daily, and
	// weekly quota counters. The app binding uses the billing usage store, and
	// its executor determines transaction completion before later consumers read.
	IncrementQuotaUsed(ctx context.Context, id int64, amount float64) error
	// BulkUpdate applies a provider-field patch to selected IDs and returns the
	// affected record count. Empty ID input succeeds; callers supply the
	// partial-update structure whose optional values select fields to change.
	BulkUpdate(ctx context.Context, ids []int64, updates providercore.ProviderBulkUpdate) (int64, error)
}
