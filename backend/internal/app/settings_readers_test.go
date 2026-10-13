package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/search"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Gateway execution readers must retain the same settings instances used for
// post-save publication. The scenario assembles them without loading storage,
// publishes provider scheduling thresholds, and checks execution reads the
// changed value through the shared cache while process-global User-Agent
// callbacks are cleared afterward.

// TestSettingsReadersSharePublishedState assembles domain readers and execution
// settings over one Store. It checks every bound instance is shared,
// construction performs no source read, and published provider thresholds reach
// execution without another read.
func TestSettingsReadersSharePublishedState(t *testing.T) {
	repo := &readerStoreProbe{}
	store := settings.New(repo)
	gatewayRuntime := provideGatewaySettings(store)
	provider := provideProviderSettings(store)
	quota := provideQuotaSettings(store)
	routing := provideRoutingSettings(store)
	moderation := provideModerationSettings(store)
	searchRuntime := search.NewConfigService(store, nil, nil, search.NewRegistry())
	readers := provideGatewayRuntimeReaders(store, gatewayRuntime, provider, quota, routing, moderation, searchRuntime, nil)
	t.Cleanup(func() {
		// User-Agent resolvers are process-global callbacks installed by assembly.
		// Clear both after this scenario so later tests can bind their own settings instances independently.
		openai.SetCodexCanonicalUserAgentResolver(nil)
		antigravity.SetUserAgentVersionResolver(nil)
	})
	require.Zero(t, repo.reads)
	require.Same(t, gatewayRuntime, readers.Gateway)
	require.Same(t, provider, readers.Provider)
	require.Same(t, quota, readers.Quota)
	require.Same(t, routing, readers.Routing)
	require.Same(t, moderation, readers.Moderation)
	require.Same(t, searchRuntime, readers.Search)
	require.Same(t, store, readers.Scheduler)
	provider.ApplySchedulingThresholds(map[string]int{"openai": 73})
	require.Equal(t, 73, readers.Provider.GetProviderSchedulingThresholds(context.Background())["openai"])
	require.Zero(t, repo.reads, "提交后发布必须命中执行消费者共用的缓存")
}
