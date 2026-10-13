package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
)

// Health assembly returns one core, recovery service, and platform observer
// graph for later callers. A counter fixture records reset IDs, making it
// visible that construction performs no reset and both returned access paths
// call the same configured counter.

// healthRuntimeCounter records forbidden-counter resets for shared health graph
// assertions.
type healthRuntimeCounter struct {
	resets []int64 // recorded reset identities
}

// IncrementOpenAI403Count returns a fixed single observation for this counter
// fixture.
func (*healthRuntimeCounter) IncrementOpenAI403Count(context.Context, int64, int) (int64, error) {
	return 1, nil
}

// ResetOpenAI403Count appends the requested provider ID for later assertions.
// The fixture is used synchronously and ignores context, allowing both health
// access paths to record into the same slice.
func (p *healthRuntimeCounter) ResetOpenAI403Count(_ context.Context, id int64) error {
	p.resets = append(p.resets, id)
	return nil
}

// TestProviderHealthRuntimePublishesOneNativeGraph constructs health and
// recovery without issuing counter calls. It checks all returned observer
// references share the same components, then verifies resets through both
// access paths reach the configured counter.
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
