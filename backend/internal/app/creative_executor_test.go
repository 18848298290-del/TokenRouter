package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/creative"
	creativeprovider "github.com/TokenFlux/TokenRouter/internal/creative/provider"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// creativeExecutionGroupProbe 提供分组读取函数，记录装配期间的读取次数。
type creativeExecutionGroupProbe struct{ reads int }

type creativePolicyGroups struct{ group *routing.Group }

type creativePolicyProviders struct {
	selection.Providers
	value *gatewayprovider.ExecutionProvider
}

// 价格存储为空，验证分组策略不依赖价格配置关联。
type creativeNoPrices struct {
	routing.PricingConfigRepository
}

func (p *creativeExecutionGroupProbe) GetByIDLite(context.Context, int64) (*routing.Group, error) {
	p.reads++
	return &routing.Group{ID: 12, Status: "active", AllowedProtocols: []capability.ProtocolID{capability.ProtocolGeminiGenerateContent}, ResponsesImagePolicy: "inherit"}, nil
}

func TestCreativeExecutorNativeAssemblyPreservesPrepareReads(t *testing.T) {
	groups := &creativeExecutionGroupProbe{}
	cfg := &config.Config{}
	cfg.Creative.ExecuteTimeoutSeconds = 17
	executor := provideCreativeExecutor(cfg, groups, nil, nil, nil, nil)
	require.Zero(t, groups.reads)
	require.Equal(t, 17*time.Second, executor.Timeout)
	_, err := executor.Prepare(context.Background(), creative.CreativeRun{GroupID: 12, Model: "gemini-3.1-flash-image", Operation: creative.CreativeOperationGenerate})
	require.ErrorContains(t, err, "no compatible creative provider available")
	require.Equal(t, 1, groups.reads, "一次读取完整策略用于全部候选")
	require.Equal(t, 5*time.Minute, provideCreativeExecutor(nil, nil, nil, nil, nil, nil).Timeout)
}

func (s creativePolicyGroups) GetByID(context.Context, int64) (*routing.Group, error) {
	return routing.CloneGroup(s.group), nil
}

func (s creativePolicyGroups) GetByIDLite(ctx context.Context, id int64) (*routing.Group, error) {
	return s.GetByID(ctx, id)
}

func (s creativePolicyProviders) GetByID(context.Context, int64) (*gatewayprovider.ExecutionProvider, error) {
	return s.value, nil
}

func (s creativePolicyProviders) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]gatewayprovider.ExecutionProvider, error) {
	return []gatewayprovider.ExecutionProvider{*s.value}, nil
}

func (creativeNoPrices) ListAll(context.Context) ([]routing.PricingConfig, error) {
	return nil, nil
}

func (creativeNoPrices) GetGroupPlatforms(context.Context, []int64) (map[int64]string, error) {
	return nil, nil
}

func TestCreativeExecutorForwardsModelAllowedByGroupScheduler(t *testing.T) {
	ctx := context.Background()
	group := &routing.Group{ID: 12, Status: "active", AllowedProtocols: []capability.ProtocolID{capability.ProtocolImagesGenerations}, RoutingPolicy: routing.GroupRoutingPolicy{
		Enabled: true, RestrictModels: true, RestrictionModelSource: routing.BillingModelSourceUpstream,
		ModelMapping:  map[string]string{"gpt-image-1": "gpt-image-2"},
		AllowedModels: []string{"gpt-image-2"},
	}}
	groups := creativePolicyGroups{group}
	policies := routing.NewPricingConfigService(creativeNoPrices{}, nil, routing.PricingConfigOptions{ReadGroup: groups.GetByIDLite})
	providers := creativePolicyProviders{value: gatewayprovider.NewExecutionProvider(&provider.Record{
		ID: 55, Platform: creative.PlatformOpenAI, Type: "apikey", Status: "active", Schedulable: true,
		GroupIDs: []int64{group.ID}, Credentials: map[string]any{"model_whitelist": []string{"gpt-image-1", "gpt-image-2"}},
	})}
	choices := selection.NewCompatible(selection.CompatibleDependencies{
		Reads: selection.Reads{Providers: providers, Groups: groups}, Shared: selection.Shared{GroupPolicies: policies},
	}, selection.DefaultOptions())
	executor := provideCreativeExecutor(nil, groups, &gatewayprovider.CreativeTargets{}, nil, choices, nil)
	run := creative.CreativeRun{GroupID: group.ID, Model: "gpt-image-1", Operation: creative.CreativeOperationGenerate, ImageSize: "1K"}
	prepared, err := executor.Prepare(ctx, run)
	require.NoError(t, err)
	t.Cleanup(prepared.ReleaseFunc)
	require.Equal(t, "gpt-image-2", prepared.UpstreamModel)
	body, _, err := creativeprovider.BuildCreativeOpenAIRequestBody(run, creative.CreativeRunPayload{Prompt: "cat"}, prepared.UpstreamModel)
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", gjson.GetBytes(body, "model").String())

	// 模型目录和执行数据各自持有策略的深拷贝，修改副本后持久化分组的数据保持不变。
	view := creativeGroupView(group, "en")
	executionGroup, err := executor.Group(ctx, group.ID)
	require.NoError(t, err)
	view.RoutingPolicy.ModelMapping["gpt-image-1"] = "changed"
	executionGroup.RoutingPolicy.AllowedModels[0] = "changed"
	require.Equal(t, "gpt-image-2", group.RoutingPolicy.ModelMapping["gpt-image-1"])
	require.Equal(t, []string{"gpt-image-2"}, group.RoutingPolicy.AllowedModels)
}

// TestCreativeExecutorKeepsPersistedGroupWhenClientRestricted 检查已保存的任务遇到客户端限制时仍使用原分组。
func TestCreativeExecutorKeepsPersistedGroupWhenClientRestricted(t *testing.T) {
	fallback := int64(99)
	group := &routing.Group{
		ID: 12, Status: "active", ClaudeCodeOnly: true, FallbackGroupID: &fallback,
		AllowedProtocols: []capability.ProtocolID{capability.ProtocolImagesGenerations},
	}
	executor := provideCreativeExecutor(nil, creativePolicyGroups{group}, nil, nil, nil, nil)
	selected := false
	executor.OpenAI = func(context.Context, creative.CreativeRun) (*creative.Selection, error) {
		selected = true
		return nil, nil
	}
	_, err := executor.Prepare(context.Background(), creative.CreativeRun{
		GroupID: group.ID, Platform: creative.PlatformOpenAI,
		Model: "gpt-image-2", Operation: creative.CreativeOperationGenerate,
	})
	require.ErrorContains(t, err, "no compatible creative provider")
	require.False(t, selected)
	require.True(t, creativeGroupView(group, "en").ClaudeCodeOnly)
}
