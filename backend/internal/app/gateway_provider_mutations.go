package app

import (
	"context"
	"time"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// UpdateConfiguration delegates transactions, locking, and protection of funding fields to the provider store.
func (r *executionProviderStore) UpdateConfiguration(ctx context.Context, value *gatewayprovider.ExecutionProvider, change providercore.ConfigurationChange) error {
	v := gatewayprovider.ExecutionRecord(value)
	err := r.data.UpdateConfiguration(ctx, v, change)
	gatewayprovider.ApplyExecutionRecord(value, v)
	return err
}

// ApplyManagedRecoveryStep delegates the recovery step to the provider store.
func (r *executionProviderStore) ApplyManagedRecoveryStep(ctx context.Context, step providercore.ManagedRecoveryStep, v providercore.ManagedRecoveryVersion) (bool, error) {
	return r.data.ApplyManagedRecoveryStep(ctx, step, v)
}

// UpdateOAuthCredentialsIfUnchanged delegates credential identity checks and transactional credential/outbox
// writes to the provider store.
func (r *executionProviderStore) UpdateOAuthCredentialsIfUnchanged(ctx context.Context, version providercore.CredentialVersion, credentials map[string]any) (bool, error) {
	return r.data.UpdateOAuthCredentialsIfUnchanged(ctx, version, credentials)
}

func (r *executionProviderStore) Update(ctx context.Context, provider *gatewayprovider.ExecutionProvider) error {
	v := gatewayprovider.ExecutionRecord(provider)
	err := r.data.Update(ctx, v)
	gatewayprovider.ApplyExecutionRecord(provider, v)
	return err
}

func (r *executionProviderStore) UpdateCredentials(ctx context.Context, id int64, credentials map[string]any) error {
	return r.data.UpdateCredentials(ctx, id, credentials)
}

func (r *executionProviderStore) UpdateLastUsed(ctx context.Context, id int64) error {
	return r.data.UpdateLastUsed(ctx, id)
}

func (r *executionProviderStore) BatchUpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error {
	return r.data.BatchUpdateLastUsed(ctx, updates)
}

func (r *executionProviderStore) SetError(ctx context.Context, id int64, errorMsg string) error {
	return r.data.SetError(ctx, id, errorMsg)
}

func (r *executionProviderStore) UpdateGrokOAuthCredentialsIfUnchanged(
	ctx context.Context,
	id int64,
	expectedCredentials map[string]any,
	expectedProxyID *int64,
	credentials map[string]any,
) (bool, error) {
	return r.data.UpdateGrokOAuthCredentialsIfUnchanged(ctx, id, expectedCredentials, expectedProxyID, credentials)
}

func (r *executionProviderStore) ClearError(ctx context.Context, id int64) error {
	return r.data.ClearError(ctx, id)
}

func (r *executionProviderStore) SetRateLimited(ctx context.Context, id int64, resetAt time.Time) error {
	return r.data.SetRateLimited(ctx, id, resetAt)
}

func (r *executionProviderStore) SetRateLimitedIfLater(ctx context.Context, id int64, resetAt time.Time) error {
	return r.data.SetRateLimitedIfLater(ctx, id, resetAt)
}

func (r *executionProviderStore) ClearRateLimitIfObserved(ctx context.Context, id int64, observedLimitedAt, observedResetAt time.Time) (bool, error) {
	return r.data.ClearRateLimitIfObserved(ctx, id, observedLimitedAt, observedResetAt)
}

func (r *executionProviderStore) SetModelRateLimit(ctx context.Context, id int64, scope string, resetAt time.Time, reason ...string) error {
	return r.data.SetModelRateLimit(ctx, id, scope, resetAt, reason...)
}

func (r *executionProviderStore) SetOverloaded(ctx context.Context, id int64, until time.Time) error {
	return r.data.SetOverloaded(ctx, id, until)
}

func (r *executionProviderStore) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	return r.data.SetTempUnschedulable(ctx, id, until, reason)
}

func (r *executionProviderStore) ClearTempUnschedulable(ctx context.Context, id int64) error {
	return r.data.ClearTempUnschedulable(ctx, id)
}

func (r *executionProviderStore) ClearRateLimit(ctx context.Context, id int64) error {
	return r.data.ClearRateLimit(ctx, id)
}

func (r *executionProviderStore) ClearAntigravityQuotaScopes(ctx context.Context, id int64) error {
	return r.data.ClearAntigravityQuotaScopes(ctx, id)
}

func (r *executionProviderStore) ClearModelRateLimits(ctx context.Context, id int64) error {
	return r.data.ClearModelRateLimits(ctx, id)
}

func (r *executionProviderStore) UpdateSessionWindow(ctx context.Context, id int64, start, end *time.Time, status string) error {
	return r.data.UpdateSessionWindow(ctx, id, start, end, status)
}

func (r *executionProviderStore) UpdateSessionWindowEnd(ctx context.Context, id int64, end time.Time) error {
	return r.data.UpdateSessionWindowEnd(ctx, id, end)
}

func (r *executionProviderStore) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	return r.data.UpdateExtra(ctx, id, updates)
}

func (r *executionProviderStore) UpdateCNUsageMonitorSnapshotCAS(
	ctx context.Context,
	providerID int64,
	expectedUpdatedAt time.Time,
	snapshot *providercore.CNUsageMonitorSnapshot,
	clearExtraKey string,
) (bool, error) {
	return r.data.UpdateCNUsageMonitorSnapshotCAS(ctx, providerID, expectedUpdatedAt, snapshot, clearExtraKey)
}

// IncrementQuotaUsed delegates atomic total, daily, and weekly usage increments to the billing store.
// Expired daily and weekly periods reset before incrementing.
// The store supports rolling and fixed reset schedules.
func (r *executionProviderStore) IncrementQuotaUsed(ctx context.Context, id int64, amount float64) error {
	return r.usage.IncrementQuotaUsed(ctx, id, amount)
}

func (r *executionProviderStore) BulkUpdate(ctx context.Context, ids []int64, updates providercore.ProviderBulkUpdate) (int64, error) {
	return r.data.BulkUpdate(ctx, ids, updates)
}

// UpdateUsageExtraIfUnchanged stores usage observations when provider identity still matches.
func (r *executionProviderStore) UpdateUsageExtraIfUnchanged(ctx context.Context, v providercore.UsageObservationVersion, updates map[string]any) (bool, error) {
	return r.data.UpdateUsageExtraIfUnchanged(ctx, v, updates)
}

func (r *executionProviderStore) SetUsageRateLimitIfUnchanged(ctx context.Context, v providercore.UsageObservationVersion, reset time.Time) (bool, error) {
	return r.data.SetUsageRateLimitIfUnchanged(ctx, v, reset)
}

func (r *executionProviderStore) ClearUsageRateLimitIfUnchanged(ctx context.Context, v providercore.UsageObservationVersion) (bool, error) {
	return r.data.ClearUsageRateLimitIfUnchanged(ctx, v)
}

// ClearUsageErrorIfUnchanged delegates identity checks and usage error clearing to the provider store.
func (r *executionProviderStore) ClearUsageErrorIfUnchanged(ctx context.Context, v providercore.UsageRecoveryVersion) (bool, error) {
	return r.data.ClearUsageErrorIfUnchanged(ctx, v)
}

// UpdateUsageSessionWindowEndIfUnchanged delegates the conditional window update to the provider store.
func (r *executionProviderStore) UpdateUsageSessionWindowEndIfUnchanged(ctx context.Context, v providercore.UsageObservationVersion, observed *time.Time, end time.Time) (bool, error) {
	return r.data.UpdateUsageSessionWindowEndIfUnchanged(ctx, v, observed, end)
}
