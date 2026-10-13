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

// Creative preparation reads a persisted group before choosing an eligible
// image provider. These scenarios exercise deferred group reads, mapped
// upstream models, and client restrictions, then inspect request bodies and
// copied policies to check the assembled executor's behavior.

// creativeExecutionGroupProbe counts group reads performed while preparing
// creative tasks.
type creativeExecutionGroupProbe struct {
	reads int // completed group reads
}

// creativePolicyGroups supplies a stored group for creative policy scenarios.
type creativePolicyGroups struct {
	group *routing.Group // configured policy group
}

// creativePolicyProviders supplies one execution provider to the image
// scheduler.
type creativePolicyProviders struct {
	// Providers supplies unused methods; the fixture implements the two reads
	// needed by these image-selection scenarios and leaves other calls unconfigured.
	selection.Providers
	value *gatewayprovider.ExecutionProvider // configured execution provider
}

// creativeNoPrices supplies empty pricing data for group-policy execution
// scenarios.
type creativeNoPrices struct {
	// PricingConfigRepository supplies unused methods; the fixture implements
	// empty configuration and platform reads for the pricing-service initialization.
	routing.PricingConfigRepository
}

// GetByIDLite counts the lookup and returns an active Gemini image group.
func (p *creativeExecutionGroupProbe) GetByIDLite(context.Context, int64) (*routing.Group, error) {
	p.reads++
	return &routing.Group{ID: 12, Status: "active", AllowedProtocols: []capability.ProtocolID{capability.ProtocolGeminiGenerateContent}, ResponsesImagePolicy: "inherit"}, nil
}

// TestCreativeExecutorNativeAssemblyPreservesPrepareReads assembles an executor
// before preparing a Gemini task. It checks deferred group lookup, the
// configured execution timeout, the unavailable-provider error, and the
// default timeout of five minutes.
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

// GetByID returns a cloned fixture group for creative policy reads.
func (s creativePolicyGroups) GetByID(context.Context, int64) (*routing.Group, error) {
	return routing.CloneGroup(s.group), nil
}

// GetByIDLite delegates to the fixture's cloned full-group read.
func (s creativePolicyGroups) GetByIDLite(ctx context.Context, id int64) (*routing.Group, error) {
	return s.GetByID(ctx, id)
}

// GetByID returns the configured execution-provider pointer for scheduler
// lookup.
func (s creativePolicyProviders) GetByID(context.Context, int64) (*gatewayprovider.ExecutionProvider, error) {
	return s.value, nil
}

// ListSchedulableByGroupIDAndPlatform returns a value copy of the fixture
// provider.
func (s creativePolicyProviders) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]gatewayprovider.ExecutionProvider, error) {
	return []gatewayprovider.ExecutionProvider{*s.value}, nil
}

// ListAll returns no pricing configurations for this group-policy fixture.
func (creativeNoPrices) ListAll(context.Context) ([]routing.PricingConfig, error) {
	return nil, nil
}

// GetGroupPlatforms returns no pricing-platform overrides for the requested
// groups.
func (creativeNoPrices) GetGroupPlatforms(context.Context, []int64) (map[int64]string, error) {
	return nil, nil
}

// TestCreativeExecutorForwardsModelAllowedByGroupScheduler prepares an image
// task whose group maps the client model. It checks the selected upstream model
// and request body, then verifies copied catalog and execution policies leave
// the stored group unchanged.
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

	// Mutate different policy collections in the catalog and execution views to
	// check both copies leave the repository group's mapping and allowlist intact.
	view := creativeGroupView(group, "en")
	executionGroup, err := executor.Group(ctx, group.ID)
	require.NoError(t, err)
	view.RoutingPolicy.ModelMapping["gpt-image-1"] = "changed"
	executionGroup.RoutingPolicy.AllowedModels[0] = "changed"
	require.Equal(t, "gpt-image-2", group.RoutingPolicy.ModelMapping["gpt-image-1"])
	require.Equal(t, []string{"gpt-image-2"}, group.RoutingPolicy.AllowedModels)
}

// TestCreativeExecutorKeepsPersistedGroupWhenClientRestricted prepares a task
// in a Claude-Code-only group with a fallback configured. Preparation must
// reject the task before selection, and its catalog view must retain the
// restriction.
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
