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
		// 清除本测试安装的默认 UA 读取器，使同进程的其他测试使用各自的装配。
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
