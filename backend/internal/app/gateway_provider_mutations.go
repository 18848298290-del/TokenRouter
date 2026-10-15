package app

import (
	"context"
	"time"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// gateway_provider_mutations.go adapts gateway provider mutations and copies
// updated records back into their execution targets.
// Execution targets contain a provider record plus request-specific route and
// capability data. Configuration and full-record updates send a provider-module
// copy to storage, then copy the returned record back into the existing
// execution target so callbacks bound to that target see its resulting state.
// That transfer runs even when persistence returns an error.
//
// Other adapters delegate conditional recovery, health windows, usage
// observations, and bulk edits to the provider store. Quota consumption uses
// the billing usage store instead. The storage implementation owns transaction
// completion, matching predicates, outbox delivery, and snapshot
// synchronization; callers retain responsibility for synchronizing access to
// mutable execution targets.

// UpdateConfiguration converts the target record for a declared configuration
// edit and applies the resulting record back to the target. Storage owns
// locking and transaction participation; callers synchronize this target with
// its bound callbacks.
//
// ApplyExecutionRecord runs after the store call even on error. It retains the
// target's existing Record address, Route, and Capabilities, so later execution
// reads the resulting provider fields without losing request-specific routing
// data.
func (r *executionProviderStore) UpdateConfiguration(ctx context.Context, value *gatewayprovider.ExecutionProvider, change providercore.ConfigurationChange) error {
	v := gatewayprovider.ExecutionRecord(value)
	err := r.data.UpdateConfiguration(ctx, v, change)
	// Publish the store-returned record into the existing target even on error.
	// Bound callbacks retain its address, and request Route and Capabilities stay attached.
	gatewayprovider.ApplyExecutionRecord(value, v)
	return err
}

// ApplyManagedRecoveryStep delegates one recovery step using its observed
// provider version. False reports an unapplied conditional step, and earlier
// completed steps keep the store's persistence outcome when a later call fails.
func (r *executionProviderStore) ApplyManagedRecoveryStep(ctx context.Context, step providercore.ManagedRecoveryStep, v providercore.ManagedRecoveryVersion) (bool, error) {
	return r.data.ApplyManagedRecoveryStep(ctx, step, v)
}

// UpdateOAuthCredentialsIfUnchanged delegates identity-checked credential
// replacement to provider storage. The applied result describes the match;
// caller-owned context transactions still require their owner to commit and
// arrange post-commit publication.
func (r *executionProviderStore) UpdateOAuthCredentialsIfUnchanged(ctx context.Context, version providercore.CredentialVersion, credentials map[string]any) (bool, error) {
	return r.data.UpdateOAuthCredentialsIfUnchanged(ctx, version, credentials)
}

// Update copies the execution record for the provider store's full update and
// transfers the resulting fields back afterward. Callers synchronize the
// mutable target while its request-specific callbacks may read it.
//
// The transfer is performed whether the store succeeds or returns an error.
// ApplyExecutionRecord updates the existing Record address and leaves Route and
// Capabilities available, so a persisted-record round trip does not discard the
// attempt's resolved routing data.
func (r *executionProviderStore) Update(ctx context.Context, provider *gatewayprovider.ExecutionProvider) error {
	v := gatewayprovider.ExecutionRecord(provider)
	err := r.data.Update(ctx, v)
	// Copy back after the full update even when persistence fails, so the
	// request target retains the store-returned fields alongside its current route and capabilities.
	gatewayprovider.ApplyExecutionRecord(provider, v)
	return err
}

// UpdateCredentials delegates replacement of the provider credential object to
// storage. The supplied map is passed unchanged to the store, whose shared
// mutation path owns persistence and scheduler propagation.
func (r *executionProviderStore) UpdateCredentials(ctx context.Context, id int64, credentials map[string]any) error {
	return r.data.UpdateCredentials(ctx, id, credentials)
}

// UpdateLastUsed delegates activity persistence using the provider store's
// current clock. Its storage path emits a separate last-used observation after
// the write and leaves immediate snapshot synchronization to other paths.
func (r *executionProviderStore) UpdateLastUsed(ctx context.Context, id int64) error {
	return r.data.UpdateLastUsed(ctx, id)
}

// BatchUpdateLastUsed delegates per-provider activity timestamps in one
// supplied map. The store assigns those timestamps directly, accepts empty
// input, and emits the Unix-second activity observation after the state write.
func (r *executionProviderStore) BatchUpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error {
	return r.data.BatchUpdateLastUsed(ctx, updates)
}

// SetError delegates error status, message, and scheduling disablement to
// provider storage. The store writes state before its event attempt, then
// synchronizes the current provider snapshot used by selection.
func (r *executionProviderStore) SetError(ctx context.Context, id int64, errorMsg string) error {
	return r.data.SetError(ctx, id, errorMsg)
}

// UpdateGrokOAuthCredentialsIfUnchanged delegates Grok credential replacement
// using expected credentials and nullable proxy ID. False reports a missing or
// changed identity; matching writes use the provider store's credential and
// event handling.
func (r *executionProviderStore) UpdateGrokOAuthCredentialsIfUnchanged(
	ctx context.Context,
	id int64,
	expectedCredentials map[string]any,
	expectedProxyID *int64,
	credentials map[string]any,
) (bool, error) {
	return r.data.UpdateGrokOAuthCredentialsIfUnchanged(ctx, id, expectedCredentials, expectedProxyID, credentials)
}

// ClearError delegates active-status restoration and an empty error message.
// The storage method retains the scheduling switch and other health windows, so
// separate recovery calls determine their remaining eligibility.
func (r *executionProviderStore) ClearError(ctx context.Context, id int64) error {
	return r.data.ClearError(ctx, id)
}

// SetRateLimited delegates the provider rate-limit observation and supplied
// reset deadline. This replacement can shorten an existing window; callers
// choose this setter or SetRateLimitedIfLater according to the observation
// rule.
func (r *executionProviderStore) SetRateLimited(ctx context.Context, id int64, resetAt time.Time) error {
	return r.data.SetRateLimited(ctx, id, resetAt)
}

// SetRateLimitedIfLater delegates a deadline extension that retains a later
// stored reset. Storage decides whether an update applies; the caller supplies
// resetAt unchanged for that comparison.
func (r *executionProviderStore) SetRateLimitedIfLater(ctx context.Context, id int64, resetAt time.Time) error {
	return r.data.SetRateLimitedIfLater(ctx, id, resetAt)
}

// ClearRateLimitIfObserved delegates Grok recovery using the observed limit
// timestamps. False reports a missing or changed pair, allowing a newer stored
// window to remain available to execution.
func (r *executionProviderStore) ClearRateLimitIfObserved(ctx context.Context, id int64, observedLimitedAt, observedResetAt time.Time) (bool, error) {
	return r.data.ClearRateLimitIfObserved(ctx, id, observedLimitedAt, observedResetAt)
}

// SetModelRateLimit delegates one scope deadline and optional reason to
// storage. An empty scope succeeds without a write, and the store considers
// only the first supplied reason.
func (r *executionProviderStore) SetModelRateLimit(ctx context.Context, id int64, scope string, resetAt time.Time, reason ...string) error {
	return r.data.SetModelRateLimit(ctx, id, scope, resetAt, reason...)
}

// SetOverloaded delegates replacement of the provider overload deadline using
// until unchanged. Provider storage attempts its event after persistence and
// synchronizes the latest snapshot, including when event failure is observed.
func (r *executionProviderStore) SetOverloaded(ctx context.Context, id int64, until time.Time) error {
	return r.data.SetOverloaded(ctx, id, until)
}

// SetTempUnschedulable delegates a temporary pause deadline and reason to
// storage. The store applies a later deadline and retains metadata when the
// stored pause is equal or longer.
func (r *executionProviderStore) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	return r.data.SetTempUnschedulable(ctx, id, until, reason)
}

// ClearTempUnschedulable delegates removal of the temporary pause deadline and
// reason. Provider storage accepts a missing row, attempts its scheduling
// event, and then synchronizes the current snapshot.
func (r *executionProviderStore) ClearTempUnschedulable(ctx context.Context, id int64) error {
	return r.data.ClearTempUnschedulable(ctx, id)
}

// ClearRateLimit delegates clearing provider rate-limit and overload columns.
// It leaves the manual scheduling switch and temporary pause unchanged, so
// their eligibility rules still apply after this health cleanup.
func (r *executionProviderStore) ClearRateLimit(ctx context.Context, id int64) error {
	return r.data.ClearRateLimit(ctx, id)
}

// ClearAntigravityQuotaScopes delegates removal of stored quota-scope
// observations. The provider store returns ErrProviderNotFound for an absent
// row and relies on event propagation rather than immediate snapshot
// synchronization.
func (r *executionProviderStore) ClearAntigravityQuotaScopes(ctx context.Context, id int64) error {
	return r.data.ClearAntigravityQuotaScopes(ctx, id)
}

// ClearModelRateLimits delegates removal of the provider's model-limit Extra
// key. The store returns ErrProviderNotFound for an absent row, then attempts
// scheduling propagation and current snapshot synchronization for a matching
// write.
func (r *executionProviderStore) ClearModelRateLimits(ctx context.Context, id int64) error {
	return r.data.ClearModelRateLimits(ctx, id)
}

// UpdateSessionWindow delegates session status and optional start/end
// timestamps to storage. Nil timestamp pointers keep their respective stored
// columns, while a supplied timestamp causes the store to attempt its event.
func (r *executionProviderStore) UpdateSessionWindow(ctx context.Context, id int64, start, end *time.Time, status string) error {
	return r.data.UpdateSessionWindow(ctx, id, start, end, status)
}

// UpdateSessionWindowEnd delegates replacement of the provider's stored
// session-window end. Its event follows the state write, and this storage path
// relies on later propagation rather than immediate snapshot synchronization.
func (r *executionProviderStore) UpdateSessionWindowEnd(ctx context.Context, id int64, end time.Time) error {
	return r.data.UpdateSessionWindowEnd(ctx, id, end)
}

// UpdateExtra delegates merging Extra values through the provider patch path.
// Storage can remove deprecated keys from updates before serialization, so
// callers account for that map mutation when reusing their input.
func (r *executionProviderStore) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	return r.data.UpdateExtra(ctx, id, updates)
}

// UpdateCNUsageMonitorSnapshotCAS delegates CN snapshot persistence using the
// observed updated_at value. Nil snapshots or mismatched rows return false; a
// nonblank clearExtraKey requests removal during the same snapshot patch.
func (r *executionProviderStore) UpdateCNUsageMonitorSnapshotCAS(
	ctx context.Context,
	providerID int64,
	expectedUpdatedAt time.Time,
	snapshot *providercore.CNUsageMonitorSnapshot,
	clearExtraKey string,
) (bool, error) {
	return r.data.UpdateCNUsageMonitorSnapshotCAS(ctx, providerID, expectedUpdatedAt, snapshot, clearExtraKey)
}

// IncrementQuotaUsed delegates provider consumption to billing usage storage
// and its period rules. The store invokes its change callback when the
// increment reports a total-limit crossing; this adapter returns the store
// error.
func (r *executionProviderStore) IncrementQuotaUsed(ctx context.Context, id int64, amount float64) error {
	return r.usage.IncrementQuotaUsed(ctx, id, amount)
}

// BulkUpdate delegates shared fields and protocol patches for selected provider
// IDs. It returns the affected count; empty IDs or an effective empty patch
// produce zero through the store.
func (r *executionProviderStore) BulkUpdate(ctx context.Context, ids []int64, updates providercore.ProviderBulkUpdate) (int64, error) {
	return r.data.BulkUpdate(ctx, ids, updates)
}

// UpdateUsageExtraIfUnchanged delegates an identity-checked usage patch. A
// changed version returns false, while an empty effective patch can succeed
// before identity comparison; storage copies its supported update containers.
func (r *executionProviderStore) UpdateUsageExtraIfUnchanged(ctx context.Context, v providercore.UsageObservationVersion, updates map[string]any) (bool, error) {
	return r.data.UpdateUsageExtraIfUnchanged(ctx, v, updates)
}

// SetUsageRateLimitIfUnchanged delegates a rate-limit observation using the
// recorded provider identity. False reports that the state statement matched no
// row; the supplied reset can replace an existing deadline.
func (r *executionProviderStore) SetUsageRateLimitIfUnchanged(ctx context.Context, v providercore.UsageObservationVersion, reset time.Time) (bool, error) {
	return r.data.SetUsageRateLimitIfUnchanged(ctx, v, reset)
}

// ClearUsageRateLimitIfUnchanged delegates recovery using identity and observed
// health windows. False reports a changed or missing row, retaining a newer
// window that no longer matches the observation.
func (r *executionProviderStore) ClearUsageRateLimitIfUnchanged(ctx context.Context, v providercore.UsageObservationVersion) (bool, error) {
	return r.data.ClearUsageRateLimitIfUnchanged(ctx, v)
}

// ClearUsageErrorIfUnchanged delegates active-status recovery using the
// observed error identity and text. False reports an inapplicable, changed, or
// missing error row; scheduling and other health rules remain owned by storage.
func (r *executionProviderStore) ClearUsageErrorIfUnchanged(ctx context.Context, v providercore.UsageRecoveryVersion) (bool, error) {
	return r.data.ClearUsageErrorIfUnchanged(ctx, v)
}

// UpdateUsageSessionWindowEndIfUnchanged delegates the new end with identity
// and nullable observed-end checks. False reports a superseded or missing row,
// so callers can leave the older usage observation unapplied.
func (r *executionProviderStore) UpdateUsageSessionWindowEndIfUnchanged(ctx context.Context, v providercore.UsageObservationVersion, observed *time.Time, end time.Time) (bool, error) {
	return r.data.UpdateUsageSessionWindowEndIfUnchanged(ctx, v, observed, end)
}
