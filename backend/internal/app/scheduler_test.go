package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// TestProviderRuntimeBlockBindingSharesRecoveryFence checks that recovery and execution observe the same scheduling-block generation.
func TestProviderRuntimeBlockBindingSharesRecoveryFence(t *testing.T) {
	state := provider.NewRuntimeBlockState(time.Now)
	gateway := provideOpenAIResponseHealth(nil, state, nil, nil).Runtime
	tokens := provideOpenAITokens(nil, nil, nil, nil, state)
	value := &gatewayprovider.ExecutionProvider{Record: provider.Record{LoadLocation: time.LoadLocation, ID: 71, Platform: provider.PlatformOpenAI}}
	tokens.Block(gatewayprovider.ExecutionRecord(value), time.Now().Add(time.Minute), "装配合同")
	fence := state.ManagedRecoveryFence(value.Record.ID)
	require.NotZero(t, fence)
	require.Equal(t, fence, gateway.ManagedRecoveryFence(value.Record.ID))
	require.True(t, state.Blocked(value.Record.ID, func() string { return "" }))
	require.True(t, gateway.ClearProviderSchedulingBlockIfFence(value.Record.ID, fence))
	require.False(t, state.Blocked(value.Record.ID, func() string { return "" }))
	require.Greater(t, state.ManagedRecoveryFence(value.Record.ID), fence)
}

// TestSchedulerSharedStateBindsLegacyConsumers checks shared feedback and compatible parameter readers.
func TestSchedulerSharedStateBindsLegacyConsumers(t *testing.T) {
	state := provideSchedulerSharedState(nil, nil)
	snapshot := gatewayCompatibilitySnapshot(state)
	require.Equal(t, int64(0), snapshot.ReadTotal)
	other := provideSchedulerSharedState(nil, nil)
	require.NotSame(t, state.Settings, other.Settings)
	require.NotSame(t, state.Sticky, other.Sticky)
	// Reports through the bound execution entry affect only that application instance.
	selection.NewGeneric(selection.GenericDependencies{Shared: provideSelectionShared(nil, nil, nil, nil, state)}, selection.DefaultOptions()).ReportAdvancedProviderScheduleResult(&gatewayprovider.SelectionResult{AdvancedScheduler: true}, 51, false, nil)
	observed, _, _ := state.Feedback.Snapshot(51)
	require.Greater(t, observed, 0.0)
	untouched, _, _ := other.Feedback.Snapshot(51)
	require.Zero(t, untouched)
}

// TestGatewayBackgroundTasksUseApplicationOwner checks task draining and rejection after shutdown for both execution paths.
func TestGatewayBackgroundTasksUseApplicationOwner(t *testing.T) {
	for _, name := range []string{"messages", "openai"} {
		t.Run(name, func(t *testing.T) {
			tasks := lifecycle.NewTasks()
			openai := provideOpenAITextExecutor(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, provideAnthropicPromptCache(), selection.NewCompatible(selection.CompatibleDependencies{}, selection.DefaultOptions()), nil, tasks)
			run := gatewayCommitEffects(nil, nil, nil, nil, tasks, nil).Funds.Background
			if name == "openai" {
				run = openai.CodexUsage.Go
			}
			started := make(chan struct{})
			release := make(chan struct{})
			require.True(t, run("contract", func() { close(started); <-release }))
			<-started
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			err := tasks.Stop(ctx)
			close(release)
			require.ErrorContains(t, err, "contract=1")
			finish, done := context.WithTimeout(context.Background(), time.Second)
			defer done()
			require.NoError(t, tasks.Stop(finish))
			require.False(t, run("late", func() { t.Error("停止后不得执行任务") }))
		})
	}
}
