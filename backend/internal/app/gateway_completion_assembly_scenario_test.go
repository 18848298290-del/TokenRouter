package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Completion and text-attempt assembly must agree on recorder instances while
// maintaining distinct Messages and OpenAI rate caches. A counting rate
// repository changes its value between reads, making cache separation visible
// alongside shared price resolution and prompt-cache reuse.

// completionRateScopeFixture counts rate reads and returns the current fixture
// multiplier.
type completionRateScopeFixture struct {
	// UserGroupRateRepository supplies unused methods; this fixture implements
	// the rate lookup exercised by both independently cached completion chains.
	billing.UserGroupRateRepository
	calls int     // completed rate lookups
	value float64 // current rate multiplier
}

// GetByUserAndGroup counts a read and returns a pointer to the current
// multiplier copy. User and group identifiers are ignored so the scenario can
// change one source value between recorder-cache reads.
func (r *completionRateScopeFixture) GetByUserAndGroup(context.Context, int64, int64) (*float64, error) {
	r.calls++
	value := r.value
	return &value, nil
}

// TestCompletionRuntimeOwnsIsolatedRatesAndSharedRecorders assembles recorders
// and platform-attempt bindings before any rate read. It checks shared recorder
// and price-resolver instances, reused prompt-cache storage, and separate rate
// caches that observe different repository values.
func TestCompletionRuntimeOwnsIsolatedRatesAndSharedRecorders(t *testing.T) {
	cfg := &config.Config{}
	repo := &completionRateScopeFixture{value: 2}
	rates := provideGatewayBillingRates(repo, cfg)
	health := &providerHealthRuntime{Health: provider.NewHealthService(nil, nil, provider.HealthOptions{})}
	tasks := lifecycle.NewTasks()
	recorders := ProvideGatewayCompletionRecorders(nil, rates, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, health, nil, tasks, cfg)
	require.Zero(t, repo.calls)
	prices := &billing.PriceResolver{}
	forward := messageAttemptBindings(nil, nil, nil, nil, nil, nil, nil, recorders, &messageHTTPBindings{}, nil, nil, nil, cfg, nil, nil, nil, nil, prices)
	require.Same(t, prices, forward.Pricing.Resolver)
	openai := provideOpenAIAttemptBindings(nil, nil, nil, nil, nil, nil, recorders, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	cache := provideAnthropicPromptCache()
	text := openAITextExecution(cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, cache, func() time.Duration { return time.Hour }, nil)

	require.Same(t, recorders.Forward, forward.Recorder)
	require.Same(t, recorders.OpenAI, openai.Recorder)
	// Bind a digest and look it up through a longer chain to check the
	// text bindings retain the prompt cache instance supplied during construction.
	bindings := text.PromptCache
	bindings.Bind(5, 7, "s:a-u:b", "cache-key", "", time.Hour)
	require.Same(t, bindings, text.PromptCache)
	cacheKey, _ := text.PromptCache.Find(5, 7, "s:a-u:b-a:c")
	require.Equal(t, "cache-key", cacheKey)
	require.NotSame(t, recorders.Forward, recorders.OpenAI)
	require.NotSame(t, rates.Forward, rates.OpenAI)
	require.Zero(t, repo.calls)
	require.Equal(t, 2.0, rates.Forward.Resolve(t.Context(), 7, 9, 1))
	repo.value = 3
	require.Equal(t, 3.0, rates.OpenAI.Resolve(t.Context(), 7, 9, 1))
	require.Equal(t, 2.0, rates.Forward.Resolve(t.Context(), 7, 9, 1))
	require.Equal(t, 2, repo.calls)
	require.Same(t, recorders.Forward, forward.Recorder)
	require.Same(t, recorders.OpenAI, openai.Recorder)
	require.NoError(t, tasks.Stop(t.Context()))
}
