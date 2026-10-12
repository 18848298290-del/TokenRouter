package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
)

type healthRuntimeCounter struct{ resets []int64 }

func (*healthRuntimeCounter) IncrementOpenAI403Count(context.Context, int64, int) (int64, error) {
	return 1, nil
}

func (p *healthRuntimeCounter) ResetOpenAI403Count(_ context.Context, id int64) error {
	p.resets = append(p.resets, id)
	return nil
}

// TestProviderHealthRuntimePublishesOneNativeGraph checks shared health instances and deferred store access.
func TestProviderHealthRuntimePublishesOneNativeGraph(t *testing.T) {
	cfg := &config.Config{}
	counter := &healthRuntimeCounter{}
	runtime := provideProviderHealthRuntime(nil, nil, cfg, nil, nil, counter, nil, provideProviderRuntimeState())
	observer := provideUpstreamHealth(runtime)
	require.Empty(t, counter.resets)
	require.Same(t, runtime.Health, observer.Core)
	require.Same(t, runtime.Recovery, provideProviderRecovery(runtime))
	require.Same(t, runtime.Observer, observer)
	require.Same(t, runtime.Observer.Limits, observer.Limits)
	require.Same(t, runtime.Observer.Team, observer.Team)
	require.Same(t, runtime.Health, runtime.Observer.Limits.Health)
	require.Same(t, runtime.Health, runtime.Observer.Models.Health)
	observer.Core.ResetForbiddenCounter(t.Context(), 7)
	runtime.Health.ResetForbiddenCounter(t.Context(), 9)
	require.Equal(t, []int64{7, 9}, counter.resets)
}
