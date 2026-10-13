package provider

import (
	"context"
	"time"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Credit consumption can succeed before provider recovery, cache refresh, or
// display reload fails. Reset therefore returns the consumed upstream result
// with stage-specific warning flags, preserving completed effects while later
// stages stop or remain unavailable.
//
// Consumption follows caller cancellation and service shutdown. After a nonnil
// successful result, recovery and refresh run within an eight-second
// service-owned context that survives caller cancellation but still responds
// to shutdown. Recovery precedes quota/cache work, and a recovery failure
// prevents both cache refresh and provider reload. Later cache failure still
// allows provider reload, whose warning only replaces an empty warning field.
// Refresh returns live queried usage when credit caching fails, so callers
// inspect completion flags rather than infer persistence from nil error.

// OpenAI quota action constants identify incomplete follow-up stages and bound
// reset post-processing while preserving the accepted upstream consumption
// result.
const (
	// OpenAIQuotaResetWarningCacheRefreshFailed identifies incomplete quota query
	// or cache refresh.
	OpenAIQuotaResetWarningCacheRefreshFailed = "reset_credit_cache_refresh_failed"
	// OpenAIQuotaResetWarningProviderRecoveryFailed identifies absent or failed
	// post-consumption recovery.
	OpenAIQuotaResetWarningProviderRecoveryFailed = "provider_state_recovery_failed"
	// OpenAIQuotaResetWarningProviderRefreshFailed identifies display reload
	// failure without earlier warning.
	OpenAIQuotaResetWarningProviderRefreshFailed = "provider_state_refresh_failed"
	// OpenAIQuotaResetPostProcessTimeout bounds accepted-consumption follow-up
	// lifetime.
	OpenAIQuotaResetPostProcessTimeout = 8 * time.Second
)

// OpenAIQuotaOperations supplies quota queries, credit consumption, and
// observation cache writes.
type OpenAIQuotaOperations interface {
	// QueryUsage returns live quota data for the requested provider row.
	// The action treats nil successful data as unusable and retains nested values
	// in its returned shallow observation.
	QueryUsage(context.Context, int64) (*wire.OpenAIQuotaUsage, error)
	// CacheResetCreditsSnapshot persists credit details for later display.
	// The action retains live usage on failure, so error reports persistence
	// separately from the successful upstream query observation.
	CacheResetCreditsSnapshot(context.Context, int64, *wire.OpenAIRateLimitResetCredits) error
	// CachePostResetSnapshot persists credit and usage observations after reset.
	// Failure leaves accepted consumption/recovery available in the action result
	// and does not request rollback of those earlier effects.
	CachePostResetSnapshot(context.Context, int64, *wire.OpenAIQuotaUsage) error
	// ResetCredit consumes an upstream reset credit under supplied cancellation.
	// Return a nonnil result on success; transport/conversion errors can follow
	// upstream effects, so the action performs no retry or compensation.
	ResetCredit(context.Context, int64) (*wire.OpenAIQuotaResetResult, error)
}

// OpenAIQuotaRecoverer clears recoverable provider state after an accepted
// credit action.
type OpenAIQuotaRecoverer interface {
	// RecoverProviderState applies requested runtime recovery for the row.
	// The action requests token invalidation after consumption and considers nil
	// error completion even if the returned recovery observation is nil.
	RecoverProviderState(context.Context, int64, ProviderRecoveryOptions) (*SuccessfulTestRecovery, error)
}

// OpenAIQuotaProviderReader supplies a provider display record after reset
// recovery.
type OpenAIQuotaProviderReader interface {
	// GetProvider returns the current row observation or a display lookup error.
	// Successful pointers remain borrowed in the action outcome, including nil;
	// the reader owns record construction and mutable sharing rules.
	GetProvider(context.Context, int64) (*Record, error)
}

// OpenAIQuotaOutcomeError represents an unusable empty success result from
// quota operations.
type OpenAIQuotaOutcomeError struct {
	Message string // empty-result diagnostic text
}

// OpenAIQuotaResetOutcome combines accepted consumption with independently
// completed follow-up observations.
type OpenAIQuotaResetOutcome struct {
	// OpenAIQuotaResetResult is shallow-copied from accepted consumption. Nested
	// values can remain shared with the operation result while follow-up flags
	// describe independently completed stages.
	wire.OpenAIQuotaResetResult
	// Quota is set only after accepted post-reset caching and remains borrowed.
	// Missing or failed quota/cache work leaves it nil beside its warning.
	Quota *wire.OpenAIQuotaUsage
	// Provider retains the reader's successful display pointer without copying. It
	// can be nil even after a nil-error lookup, and callers synchronize shared
	// values.
	Provider *Record `json:"-"`
	// CacheRefreshed becomes true only after query and post-reset cache success. A
	// false value retains earlier consumption and possibly completed recovery.
	CacheRefreshed bool
	// ProviderStateRecovered marks nil-error recovery completion, regardless of
	// returned recovery data. Later query/cache/reload failure leaves this flag
	// true.
	ProviderStateRecovered bool
	// WarningCode identifies the first incomplete recovery/cache/display stage.
	// Recovery stops follow-up; display failure preserves a previously chosen
	// cache warning.
	WarningCode string
}

// OpenAIQuotaRefreshOutcome returns live queried usage with its
// cache-persistence result.
type OpenAIQuotaRefreshOutcome struct {
	// OpenAIQuotaUsage is shallow-copied from the live query. Nested pointers and
	// slices remain shared, so callers synchronize their mutable use or copy
	// values independently before editing.
	wire.OpenAIQuotaUsage
	// CachePersisted marks accepted reset-credit caching after live querying.
	// False can accompany nil method error and usable live display data.
	CachePersisted bool
}

// OpenAIQuotaActions owns reset follow-up sequencing and tracked cancellation
// lifetimes.
type OpenAIQuotaActions struct {
	// Quota is required for consumption, queries, and cache writes. Keep this
	// dependency usable throughout admitted actions and their bounded follow-up.
	Quota OpenAIQuotaOperations
	// Recovery optionally handles state after accepted consumption. Nil chooses a
	// recovery warning and stops remaining post-reset stages without undoing
	// consumption.
	Recovery OpenAIQuotaRecoverer
	// Providers is required after successful recovery when display reload is
	// reached. Returned records remain borrowed by the action outcome.
	Providers OpenAIQuotaProviderReader
	// Warn is required on reached follow-up/cache failure paths. Diagnostics
	// report incomplete work while earlier accepted effects remain in the result.
	Warn func(string, ...any)
	// activity owns admitted action lifetime and cached shutdown result. Keep the
	// containing instance stable and do not copy it after requests begin.
	activity operationActivity
}

// Error returns the stored outcome diagnostic from a nonnil error value.
func (e *OpenAIQuotaOutcomeError) Error() string { return e.Message }

// NewOpenAIQuotaActions retains dependencies without invoking or validating
// them. Quota is required for requests, Recovery can be absent, and reached
// Providers/Warn must be usable and stay stable throughout tracked work.
func NewOpenAIQuotaActions(quota OpenAIQuotaOperations, recovery OpenAIQuotaRecoverer, providers OpenAIQuotaProviderReader, warn func(string, ...any)) *OpenAIQuotaActions {
	return &OpenAIQuotaActions{Quota: quota, Recovery: recovery, Providers: providers, Warn: warn}
}

// StopContext rejects later actions, cancels admitted work, and waits within
// ctx. It retains the first shutdown result, including timeout while a
// dependency remains unfinished, for subsequent calls on this same instance.
// Pass a nonnil context when registered work or shutdown waiting is reached.
func (s *OpenAIQuotaActions) StopContext(ctx context.Context) error {
	return s.activity.stop(ctx, "OpenAIQuotaActions")
}

// Reset returns consumed credit data with independent recovery/cache/display
// completion flags. Required Quota must return a nonnil successful result;
// absent Recovery or follow-up failures produce a warning alongside nil method
// error.
//
// Consumption uses caller cancellation plus shutdown, while accepted
// consumption starts bounded follow-up on a service-owned context that
// survives caller cancellation. Recovery runs first with token invalidation
// requested and stops remaining stages on failure. After recovery, quota/cache
// failure retains recovery success and still permits provider reload; reload
// failure only sets its warning when no earlier warning exists. Returned
// embedded data is shallow-copied, and provider/quota pointers remain
// borrowed. Callers retain reached dependencies and perform no automatic
// rollback/retry merely because a follow-up stage is incomplete.
// Pass a nonnil context when registered work or shutdown waiting is reached.
func (s *OpenAIQuotaActions) Reset(ctx context.Context, providerID int64) (*OpenAIQuotaResetOutcome, error) {
	// Register service ownership before credit dispatch so shutdown cancels both
	// consumption and accepted-consumption follow-up, while caller cancellation
	// remains separately attached only to the credit request below.
	runtimeCtx, done, err := s.activity.begin(context.WithoutCancel(ctx), ErrOpenAIQuotaStopped)
	if err != nil {
		return nil, err
	}
	defer done()
	// Link consumption to caller and service cancellation before dispatch. The
	// separate runtime context retains values without caller cancellation so
	// accepted-consumption follow-up can finish until shutdown or its own timeout.
	creditCtx, cancelCredit := context.WithCancel(ctx)
	stopRuntimeCancel := context.AfterFunc(runtimeCtx, cancelCredit)
	defer func() { stopRuntimeCancel(); cancelCredit() }()
	result, err := s.Quota.ResetCredit(creditCtx, providerID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, &OpenAIQuotaOutcomeError{Message: "openai quota reset returned an empty result"}
	}

	resetResponse := OpenAIQuotaResetOutcome{OpenAIQuotaResetResult: *result}
	// Start the bounded post-processing budget only after accepted consumption.
	// Its parent retains service shutdown cancellation while caller cancellation
	// no longer prevents recovery/cache/display work from reaching completion.
	postCtx, cancelPost := context.WithTimeout(runtimeCtx, OpenAIQuotaResetPostProcessTimeout)
	defer cancelPost()

	// Recover runtime state before querying new windows or caching them, so later
	// accepted observations follow the recovery stage. Missing/failed recovery
	// returns its warning immediately and retains already consumed credit data.
	if s.Recovery == nil {
		resetResponse.WarningCode = OpenAIQuotaResetWarningProviderRecoveryFailed
		return &resetResponse, nil
	}
	if _, err := s.Recovery.RecoverProviderState(postCtx, providerID, ProviderRecoveryOptions{
		InvalidateToken: true,
	}); err != nil {
		s.Warn("openai_quota_reset_provider_recovery_failed", "provider_id", providerID, "error", err)
		resetResponse.WarningCode = OpenAIQuotaResetWarningProviderRecoveryFailed
		return &resetResponse, nil
	}
	resetResponse.ProviderStateRecovered = true

	// Query and persist after recovery, preserving its success flag on either
	// failure. These failures become warnings so provider reload still has a
	// chance to return current display state.
	usage, usageErr := s.Quota.QueryUsage(postCtx, providerID)
	switch {
	case usageErr != nil || usage == nil:
		s.Warn("openai_quota_reset_cache_refresh_failed", "provider_id", providerID, "error", usageErr)
		resetResponse.WarningCode = OpenAIQuotaResetWarningCacheRefreshFailed
	default:
		if err := s.Quota.CachePostResetSnapshot(postCtx, providerID, usage); err != nil {
			s.Warn("openai_quota_reset_cache_refresh_failed", "provider_id", providerID, "error", err)
			resetResponse.WarningCode = OpenAIQuotaResetWarningCacheRefreshFailed
		} else {
			resetResponse.Quota = usage
			resetResponse.CacheRefreshed = true
		}
	}

	// Reload display data after cache work even when that stage failed. Preserve
	// an earlier warning when lookup also fails, so the result identifies the
	// first incomplete follow-up stage while retaining accepted earlier effects.
	provider, err := s.Providers.GetProvider(postCtx, providerID)
	if err != nil {
		s.Warn("openai_quota_reset_provider_refresh_failed", "provider_id", providerID, "error", err)
		if resetResponse.WarningCode == "" {
			resetResponse.WarningCode = OpenAIQuotaResetWarningProviderRefreshFailed
		}
		return &resetResponse, nil
	}
	resetResponse.Provider = provider
	return &resetResponse, nil
}

// Refresh queries live usage under caller/shutdown cancellation and rejects
// nil successful usage. Required Quota and reached Warn remain usable through
// completion.
//
// The returned embedded usage is a shallow copy and can share nested
// pointers/slices with the query result. Credit-cache failure is diagnosed but
// returns that live observation with CachePersisted false and nil error; only
// accepted caching sets the flag. This makes display availability independent
// of successful persistence, while query failure remains a method error and no
// post-reset provider recovery occurs.
// Pass a nonnil context when registered work or shutdown waiting is reached.
func (s *OpenAIQuotaActions) Refresh(ctx context.Context, providerID int64) (*OpenAIQuotaRefreshOutcome, error) {
	// Register the live refresh before querying so shutdown owns cancellation
	// and waits through the cache attempt before deferred completion releases
	// this operation from its active set.
	ctx, done, err := s.activity.begin(ctx, ErrOpenAIQuotaStopped)
	if err != nil {
		return nil, err
	}
	defer done()
	usage, err := s.Quota.QueryUsage(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if usage == nil {
		return nil, &OpenAIQuotaOutcomeError{Message: "openai quota query returned an empty result"}
	}

	refreshResponse := OpenAIQuotaRefreshOutcome{OpenAIQuotaUsage: *usage}
	// Cache accepted live credit data before claiming persistence. A write failure
	// retains the shallow live usage in the result with CachePersisted false,
	// allowing display without claiming a successful durable refresh.
	if err := s.Quota.CacheResetCreditsSnapshot(ctx, providerID, usage.RateLimitResetCredits); err != nil {
		s.Warn("openai_quota_reset_credit_cache_persist_failed", "provider_id", providerID, "error", err)
		return &refreshResponse, nil
	}
	refreshResponse.CachePersisted = true
	return &refreshResponse, nil
}
