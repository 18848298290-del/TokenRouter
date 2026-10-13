package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// quotaActionsSource delegates serial quota calls to selected lifecycle test
// callbacks.
type quotaActionsSource struct {
	// query is required only on reached query paths and receives their tracked
	// context. Tests keep it stable while its admission/release signals progress.
	query func(context.Context) (*openai.OpenAIQuotaUsage, error)
	// reset is required only on reached consumption paths and receives their
	// linked caller/service context. Tests configure its accepted result
	// explicitly.
	reset func(context.Context) (*openai.OpenAIQuotaResetResult, error)
}

// quotaActionsRecovery adapts a context callback into the action recovery
// dependency.
type quotaActionsRecovery func(context.Context) (*SuccessfulTestRecovery, error)

// QueryUsage delegates supplied context to required query and ignores provider
// ID. Tests set this callback only for reached query paths and keep it usable
// throughout the blocked lifecycle.
func (s quotaActionsSource) QueryUsage(ctx context.Context, _ int64) (*openai.OpenAIQuotaUsage, error) {
	return s.query(ctx)
}

// ResetCredit delegates supplied context to required reset and ignores
// provider ID. The callback supplies the observed consumption result, and
// tests serialize its configured use before shutdown assertions.
func (s quotaActionsSource) ResetCredit(ctx context.Context, _ int64) (*openai.OpenAIQuotaResetResult, error) {
	return s.reset(ctx)
}

// CacheResetCreditsSnapshot accepts fixture credit writes without storing or
// copying data.
func (quotaActionsSource) CacheResetCreditsSnapshot(context.Context, int64, *openai.OpenAIRateLimitResetCredits) error {
	return nil
}

// CachePostResetSnapshot accepts fixture post-reset writes without durable
// cache effects.
func (quotaActionsSource) CachePostResetSnapshot(context.Context, int64, *openai.OpenAIQuotaUsage) error {
	return nil
}

// RecoverProviderState forwards context to the configured callback, ignoring
// row and options. Tests use its blocked return to observe post-consumption
// cancellation ownership rather than actual provider mutation or token
// invalidation.
func (f quotaActionsRecovery) RecoverProviderState(ctx context.Context, _ int64, _ ProviderRecoveryOptions) (*SuccessfulTestRecovery, error) {
	return f(ctx)
}

// TestOpenAIQuotaActionsOwnPostConsumptionLifecycle consumes once then blocks
// recovery during caller cancellation and shutdown. It checks follow-up
// survives caller cancellation, returns its recovery warning on shutdown, and
// later reset admission cannot consume again.
func TestOpenAIQuotaActionsOwnPostConsumptionLifecycle(t *testing.T) {
	recoveryEntered := make(chan context.Context, 1)
	var consumed atomic.Int32
	source := quotaActionsSource{reset: func(context.Context) (*openai.OpenAIQuotaResetResult, error) {
		consumed.Add(1)
		return &openai.OpenAIQuotaResetResult{Code: "fixture-consumed"}, nil
	}}
	// Signal the follow-up context before waiting for cancellation, so the test
	// can distinguish caller cancellation from the later service shutdown that
	// must release this blocked recovery operation.
	recovery := quotaActionsRecovery(func(ctx context.Context) (*SuccessfulTestRecovery, error) {
		recoveryEntered <- ctx
		<-ctx.Done()
		return nil, ctx.Err()
	})
	actions := NewOpenAIQuotaActions(source, recovery, nil, func(string, ...any) {})
	clientCtx, cancelClient := context.WithCancel(context.Background())
	defer cancelClient()
	outcome := make(chan *OpenAIQuotaResetOutcome, 1)
	failure := make(chan error, 1)
	// Run reset independently until recovery signals entry, then join its result
	// through buffered channels after shutdown. This preserves accepted
	// consumption while testing ownership of the still-active follow-up.
	go func() { value, err := actions.Reset(clientCtx, 1); outcome <- value; failure <- err }()
	postCtx := <-recoveryEntered
	cancelClient()
	require.NoError(t, postCtx.Err())
	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	require.NoError(t, actions.StopContext(stopCtx))
	require.NoError(t, <-failure)
	result := <-outcome
	require.Equal(t, OpenAIQuotaResetWarningProviderRecoveryFailed, result.WarningCode)
	require.False(t, result.ProviderStateRecovered)
	require.EqualValues(t, 1, consumed.Load())
	_, err := actions.Reset(context.Background(), 1)
	require.ErrorIs(t, err, ErrOpenAIQuotaStopped)
	require.EqualValues(t, 1, consumed.Load())
}

// TestOpenAIQuotaActionsStopCancelsRefresh blocks a live quota query after
// signalling entry. It checks service shutdown cancels that admitted refresh
// and the request returns context.Canceled within the configured stop budget.
func TestOpenAIQuotaActionsStopCancelsRefresh(t *testing.T) {
	entered := make(chan struct{})
	// Signal active query entry before blocking on cancellation, so stopping the
	// service must cancel this reached dependency and join the action rather
	// than only preventing future refresh admission.
	source := quotaActionsSource{query: func(ctx context.Context) (*openai.OpenAIQuotaUsage, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	actions := NewOpenAIQuotaActions(source, nil, nil, func(string, ...any) {})
	finished := make(chan error, 1)
	// Dispatch refresh before waiting for query entry so stop cancels registered
	// active work. Buffered completion allows shutdown to wait for deferred
	// action completion before the assertion receives its returned error.
	go func() { _, err := actions.Refresh(context.Background(), 1); finished <- err }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, actions.StopContext(ctx))
	require.ErrorIs(t, <-finished, context.Canceled)
}
