package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

type quotaActionsSource struct {
	query func(context.Context) (*openai.OpenAIQuotaUsage, error)
	reset func(context.Context) (*openai.OpenAIQuotaResetResult, error)
}

type quotaActionsRecovery func(context.Context) (*SuccessfulTestRecovery, error)

func (s quotaActionsSource) QueryUsage(ctx context.Context, _ int64) (*openai.OpenAIQuotaUsage, error) {
	return s.query(ctx)
}

func (s quotaActionsSource) ResetCredit(ctx context.Context, _ int64) (*openai.OpenAIQuotaResetResult, error) {
	return s.reset(ctx)
}

func (quotaActionsSource) CacheResetCreditsSnapshot(context.Context, int64, *openai.OpenAIRateLimitResetCredits) error {
	return nil
}

func (quotaActionsSource) CachePostResetSnapshot(context.Context, int64, *openai.OpenAIQuotaUsage) error {
	return nil
}

func (f quotaActionsRecovery) RecoverProviderState(ctx context.Context, _ int64, _ ProviderRecoveryOptions) (*SuccessfulTestRecovery, error) {
	return f(ctx)
}

// TestOpenAIQuotaActionsOwnPostConsumptionLifecycle checks that recovery survives caller cancellation but
// stops on shutdown.
func TestOpenAIQuotaActionsOwnPostConsumptionLifecycle(t *testing.T) {
	recoveryEntered := make(chan context.Context, 1)
	var consumed atomic.Int32
	source := quotaActionsSource{reset: func(context.Context) (*openai.OpenAIQuotaResetResult, error) {
		consumed.Add(1)
		return &openai.OpenAIQuotaResetResult{Code: "fixture-consumed"}, nil
	}}
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

// TestOpenAIQuotaActionsStopCancelsRefresh checks that shutdown cancels an active quota query.
func TestOpenAIQuotaActionsStopCancelsRefresh(t *testing.T) {
	entered := make(chan struct{})
	source := quotaActionsSource{query: func(ctx context.Context) (*openai.OpenAIQuotaUsage, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	actions := NewOpenAIQuotaActions(source, nil, nil, func(string, ...any) {})
	finished := make(chan error, 1)
	go func() { _, err := actions.Refresh(context.Background(), 1); finished <- err }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, actions.StopContext(ctx))
	require.ErrorIs(t, <-finished, context.Canceled)
}
