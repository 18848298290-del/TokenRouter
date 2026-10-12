package provider

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// availabilityProviderStore filters candidates using the fixture providers' persistent configuration.
type availabilityProviderStore struct {
	providers      []ExecutionProvider
	providersByID  map[int64]*ExecutionProvider
	calls          int
	includeGrouped bool
	platforms      []string
}

func TestDiagnoseModelAvailabilityForPlatform_NoModel_AlwaysAvailable(t *testing.T) {
	repo := &availabilityProviderStore{providers: nil, providersByID: map[int64]*ExecutionProvider{}}
	svc := newAvailabilityForTest(repo, nil, false)

	diag := svc.DiagnoseGeneral(context.Background(), availabilityFixtureGroupID(), "", capability.PlatformOpenAI)

	require.True(t, diag.HasProvidersInPool, "空模型必须保守返回 HasProvidersInPool=true，让调用方继续走 503")
	require.True(t, diag.HasModelSupport, "空模型必须保守返回 HasModelSupport=true，让调用方继续走 503")
}

func TestDiagnoseModelAvailabilityForPlatform_EmptyPlatformKeepsEmptyGroupEmpty(t *testing.T) {
	repo := &availabilityProviderStore{providers: nil, providersByID: map[int64]*ExecutionProvider{}}
	svc := newAvailabilityForTest(repo, nil, false)

	diag := svc.DiagnoseGeneral(context.Background(), availabilityFixtureGroupID(), "gpt-5", "")

	require.False(t, diag.HasProvidersInPool)
	require.False(t, diag.HasModelSupport)
}

func TestDiagnoseModelAvailabilityForPlatform_NilReceiver(t *testing.T) {
	var svc *routing.ModelAvailability

	diag := svc.DiagnoseGeneral(context.Background(), availabilityFixtureGroupID(), "gpt-5", capability.PlatformOpenAI)

	require.True(t, diag.HasProvidersInPool)
	require.True(t, diag.HasModelSupport)
}

func TestDiagnoseModelAvailabilityForPlatform_NoProvidersInPool(t *testing.T) {
	repo := &availabilityProviderStore{providers: nil, providersByID: map[int64]*ExecutionProvider{}}
	svc := newAvailabilityForTest(repo, nil, false)

	diag := svc.DiagnoseGeneral(context.Background(), availabilityFixtureGroupID(), "gpt-5", capability.PlatformOpenAI)

	require.False(t, diag.HasProvidersInPool)
	require.False(t, diag.HasModelSupport, "没有提供商表示没有模型支持；调用方会走空池 503 分支")
}

func TestDiagnoseModelAvailabilityForPlatform_ExplicitMappingMatches(t *testing.T) {
	repo := &availabilityProviderStore{
		providers: []ExecutionProvider{
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 1,
					Platform:    capability.PlatformOpenAI,
					Status:      billing.StatusActive,
					Schedulable: true,
					Credentials: map[string]any{
						"model_mapping": map[string]any{"gpt-5.1-codex-mini": "gpt-5.1-codex-mini"},
					},
				},
			},
		},
		providersByID: map[int64]*ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}
	svc := newAvailabilityForTest(repo, nil, false)

	diag := svc.DiagnoseGeneral(context.Background(), availabilityFixtureGroupID(), "gpt-5.1-codex-mini", capability.PlatformOpenAI)

	require.True(t, diag.HasProvidersInPool)
	require.True(t, diag.HasModelSupport)
}

func TestDiagnoseModelAvailabilityAllowsUnknownModelForEmptyScope(t *testing.T) {
	repo := &availabilityProviderStore{
		providers: []ExecutionProvider{
			{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true} /* An empty allowlist permits models unknown to the catalogue. */},
		},
		providersByID: map[int64]*ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}
	svc := newAvailabilityForTest(repo, nil, false)

	diag := svc.DiagnoseGeneral(context.Background(), availabilityFixtureGroupID(), "gpt-5.1-codex-mini", capability.PlatformOpenAI)

	require.True(t, diag.HasModelSupport, "空白名单允许未知型号")
	diag = svc.DiagnoseGeneral(context.Background(), availabilityFixtureGroupID(), "gpt-5.6-sol", capability.PlatformOpenAI)
	require.True(t, diag.HasModelSupport)
}

func TestDiagnoseModelAvailabilityForPlatform_WildcardMappingMatches(t *testing.T) {
	repo := &availabilityProviderStore{
		providers: []ExecutionProvider{
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 1,
					Platform:    capability.PlatformOpenAI,
					Status:      billing.StatusActive,
					Schedulable: true,
					Credentials: map[string]any{
						"model_mapping": map[string]any{"*": "gpt-5"},
					},
				},
			},
		},
		providersByID: map[int64]*ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}
	svc := newAvailabilityForTest(repo, nil, false)

	diag := svc.DiagnoseGeneral(context.Background(), availabilityFixtureGroupID(), "gpt-5.1-codex-mini", capability.PlatformOpenAI)

	require.True(t, diag.HasModelSupport, "通配符映射必须把请求模型视为可服务")
}

func TestDiagnoseModelAvailabilityForPlatform_NoMatchingModel_ReturnsNotFoundSignal(t *testing.T) {
	groupID := int64(42)
	repo := &availabilityProviderStore{
		providers: []ExecutionProvider{
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 1,
					Platform:    capability.PlatformOpenAI,
					Status:      billing.StatusActive,
					Schedulable: true,
					ProviderGroups: []providercore.GroupMembership{
						{GroupID: groupID},
					},
					Credentials: map[string]any{"model_whitelist": []string{"gpt-5"}},
				},
			},
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 2,
					Platform:    capability.PlatformOpenAI,
					Status:      billing.StatusActive,
					Schedulable: true,
					ProviderGroups: []providercore.GroupMembership{
						{GroupID: groupID},
					},
					Credentials: map[string]any{"model_whitelist": []string{"gpt-5-mini"}},
				},
			},
		},
		providersByID: map[int64]*ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}
	svc := newAvailabilityForTest(repo, nil, false)

	diag := svc.DiagnoseGeneral(context.Background(), &groupID, "gpt-5.1-codex-mini", capability.PlatformOpenAI)

	require.True(t, diag.HasProvidersInPool, "分组内存在 OpenAI 提供商")
	require.False(t, diag.HasModelSupport, "没有提供商白名单允许该模型时 handler 应返回 404")
}

func TestDiagnoseModelAvailabilityForPlatform_RateLimitedSupportingProviderRemainsConfigured(t *testing.T) {
	groupID := int64(42)
	cooldownUntil := time.Now().Add(time.Hour)
	repo := &availabilityProviderStore{
		providers: []ExecutionProvider{
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 1,
					Platform:               capability.PlatformAnthropic,
					Status:                 billing.StatusActive,
					Schedulable:            true,
					RateLimitResetAt:       &cooldownUntil,
					OverloadUntil:          &cooldownUntil,
					TempUnschedulableUntil: &cooldownUntil,
					ProviderGroups:         []providercore.GroupMembership{{GroupID: groupID}},
					Credentials: map[string]any{
						"model_mapping": map[string]any{"claude-opus-4-8": "claude-opus-4-8"},
					},
				},
			},
		},
		providersByID: map[int64]*ExecutionProvider{},
	}
	require.False(t, repo.providers[0].View().IsSchedulable(), "test provider must be excluded from normal scheduling while cooling down")
	svc := newAvailabilityForTest(repo, nil, false)

	// Diagnosis uses persistent configuration so temporary cooldowns do not hide model support.

	diag := svc.DiagnoseGeneral(context.Background(), &groupID, "claude-opus-4-8", capability.PlatformAnthropic)

	require.True(t, diag.HasProvidersInPool)
	require.True(t, diag.HasModelSupport, "a configured model remains supported while every matching provider is temporarily cooling down")
}

func TestOpenAIDiagnoseModelAvailabilityForPlatform_RateLimitedSupportingProviderRemainsConfigured(t *testing.T) {
	groupID := int64(43)
	cooldownUntil := time.Now().Add(time.Hour)
	repo := &availabilityProviderStore{
		providers: []ExecutionProvider{
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 2,
					Platform:               capability.PlatformOpenAI,
					Status:                 billing.StatusActive,
					Schedulable:            true,
					RateLimitResetAt:       &cooldownUntil,
					OverloadUntil:          &cooldownUntil,
					TempUnschedulableUntil: &cooldownUntil,
					ProviderGroups:         []providercore.GroupMembership{{GroupID: groupID}},
					Credentials: map[string]any{
						"model_mapping": map[string]any{"claude-opus-4-8": "claude-opus-4-8"},
					},
				},
			},
		},
		providersByID: map[int64]*ExecutionProvider{},
	}
	require.False(t, repo.providers[0].View().IsSchedulable(), "test provider must be excluded from normal scheduling while cooling down")
	svc := newAvailabilityForTest(repo, nil, true)

	// Diagnosis uses persistent configuration so temporary cooldowns do not hide model support.

	diag := svc.DiagnoseCompatible(context.Background(), &groupID, "claude-opus-4-8", capability.PlatformOpenAI)

	require.True(t, diag.HasProvidersInPool)
	require.True(t, diag.HasModelSupport, "OpenAI-compatible diagnosis must keep transiently limited supporting providers in the configured pool")
}

func TestDiagnoseModelAvailabilityForPlatform_WrongPlatformFiltersOut(t *testing.T) {
	// The group contains Anthropic providers while the request targets OpenAI.
	// Platform filtering leaves HasProvidersInPool false, allowing the caller to retain its 503 response.
	repo := &availabilityProviderStore{
		providers: []ExecutionProvider{
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 1,
					Platform:    capability.PlatformAnthropic,
					Status:      billing.StatusActive,
					Schedulable: true,
					Credentials: map[string]any{"model_mapping": map[string]any{"claude-sonnet-4-5": "claude-sonnet-4-5"}},
				},
			},
		},
		providersByID: map[int64]*ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}
	svc := newAvailabilityForTest(repo, nil, false)

	diag := svc.DiagnoseGeneral(context.Background(), availabilityFixtureGroupID(), "gpt-5", capability.PlatformOpenAI)

	require.False(t, diag.HasProvidersInPool, "OpenAI 路由不能把 Anthropic 提供商算进提供商池")
	require.False(t, diag.HasModelSupport)
}

func TestOpenAIGatewayDiagnoseModelAvailabilityForPlatform_GrokPlatformFiltersOpenAIProviders(t *testing.T) {
	repo := &availabilityProviderStore{
		providers: []ExecutionProvider{
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 1,
					Platform:    capability.PlatformOpenAI,
					Status:      billing.StatusActive,
					Schedulable: true,
					Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5": "gpt-5"}},
				},
			},
		},
		providersByID: map[int64]*ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}
	svc := newAvailabilityForTest(repo, nil, true)

	diag := svc.DiagnoseCompatible(context.Background(), availabilityFixtureGroupID(), "grok-4.3", capability.PlatformGrok)

	require.False(t, diag.HasProvidersInPool, "Grok 诊断不能把 OpenAI 提供商算进提供商池")
	require.False(t, diag.HasModelSupport)
}

func (m *availabilityProviderStore) ListModelAvailabilityCandidates(_ context.Context, groupID *int64, platforms []string, includeGrouped bool) ([]providercore.Record, error) {
	m.calls++
	m.includeGrouped = includeGrouped
	m.platforms = append([]string(nil), platforms...)
	platformSet := make(map[string]struct{}, len(platforms))
	for _, platform := range platforms {
		platformSet[platform] = struct{}{}
	}
	result := make([]providercore.Record, 0, len(m.providers))
	for _, value := range m.providers {
		if _, ok := platformSet[value.Record.Platform]; !ok || value.Record.Status != billing.StatusActive || !value.Record.Schedulable {
			continue
		}
		if groupID != nil {
			inGroup := slices.Contains(value.Record.GroupIDs, *groupID)
			for _, group := range value.Record.ProviderGroups {
				if group.GroupID == *groupID {
					inGroup = true
					break
				}
			}
			if !inGroup {
				continue
			}
		} else if !includeGrouped && (len(value.Record.ProviderGroups) > 0 || len(value.Record.GroupIDs) > 0) {
			continue
		}
		result = append(result, *ExecutionRecord(&value))
	}
	return result, nil
}

func availabilityFixtureGroupID() *int64 { id := int64(71); return &id }

// newAvailabilityForTest configures group membership and model scopes for diagnosis tests.
func newAvailabilityForTest(repo *availabilityProviderStore, policies *routing.PricingConfigService, compatible bool) *routing.ModelAvailability {
	for i := range repo.providers {
		value := &repo.providers[i].Record
		if len(value.GroupIDs) == 0 && len(value.ProviderGroups) == 0 {
			value.GroupIDs = []int64{*availabilityFixtureGroupID()}
		}
		if value.Type == "" {
			value.Type = capability.ProviderTypeAPIKey
		}
	}
	return NewModelAvailability(repo, policies, compatible)
}

// TestModelAvailabilityUsesExplicitGroupAcrossPlatforms checks group membership and aggregation across platforms.
func TestModelAvailabilityUsesExplicitGroupAcrossPlatforms(t *testing.T) {
	groupID := int64(71)
	otherGroup := int64(72)

	for _, compatible := range []bool{false, true} {
		repo := &availabilityProviderStore{providers: []ExecutionProvider{
			{Record: providercore.Record{ID: 1, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, GroupIDs: []int64{groupID}, Credentials: map[string]any{"model_whitelist": []string{"claude-test"}}}},
			{Record: providercore.Record{ID: 2, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, GroupIDs: []int64{groupID}, Credentials: map[string]any{"model_whitelist": []string{"gpt-test"}}}},
			{Record: providercore.Record{ID: 3, Platform: capability.PlatformGemini, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, GroupIDs: []int64{otherGroup}, Credentials: map[string]any{"model_whitelist": []string{"gemini-test"}}}},
		}}
		service := NewModelAvailability(repo, nil, compatible)
		for _, model := range []string{"claude-test", "gpt-test"} {
			result := service.DiagnoseGeneral(context.Background(), &groupID, model, "")
			require.True(t, result.HasModelSupport)
			require.ElementsMatch(t, capability.ProviderPlatforms(), repo.platforms)
			require.False(t, repo.includeGrouped)
		}
		require.False(t, service.DiagnoseGeneral(context.Background(), &groupID, "gemini-test", "").HasModelSupport)
		forced := service.DiagnoseGeneral(context.Background(), &groupID, "claude-test", capability.PlatformOpenAI)
		require.True(t, forced.HasProvidersInPool)
		require.False(t, forced.HasModelSupport)
		calls := repo.calls
		require.Equal(t, routing.ModelAvailabilityDiagnosis{}, service.DiagnoseGeneral(context.Background(), nil, "gpt-test", ""))
		require.Equal(t, routing.ModelAvailabilityDiagnosis{}, service.DiagnoseCompatible(context.Background(), nil, "gpt-test", ""))
		require.Equal(t, routing.ModelAvailabilityDiagnosis{}, service.DiagnoseCompatibleRouting(context.Background(), nil, "gpt-test", ""))
		require.Equal(t, calls, repo.calls, "缺少分组时不应读取候选池")
		ctx := apikey.WithForcePlatform(context.Background(), capability.PlatformGemini)
		require.False(t, service.DiagnoseCompatibleRouting(ctx, &groupID, "gpt-test", "").HasProvidersInPool)
		require.Equal(t, []string{capability.PlatformGemini}, repo.platforms)
	}
}

// TestModelAvailabilityChecksProtocolWithoutTreatingCooldownAsMissingModel checks protocol eligibility while ignoring temporary cooldowns.
func TestModelAvailabilityChecksProtocolWithoutTreatingCooldownAsMissingModel(t *testing.T) {
	group := &routing.Group{ID: 71, Hydrated: true, Status: routing.StatusActive, ProtocolFallbacks: map[protocol.ProtocolID][]protocol.ProtocolID{protocol.ProtocolAnthropicMessages: {}}}
	until := time.Now().Add(time.Hour)
	repo := &availabilityProviderStore{providers: []ExecutionProvider{{Record: providercore.Record{ID: 2, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, GroupIDs: []int64{group.ID}, RateLimitResetAt: &until, Credentials: map[string]any{"model_whitelist": []string{"gpt-test"}}}}}}
	service := NewModelAvailability(repo, nil, true)
	ctx := requeststate.WithClientProtocol(requeststate.WithGroup(context.Background(), group), protocol.ProtocolAnthropicMessages)
	denied := service.DiagnoseCompatible(ctx, &group.ID, "gpt-test", "")
	require.True(t, denied.HasProvidersInPool)
	require.False(t, denied.HasModelSupport)
	group.ProtocolFallbacks = nil
	ctx = requeststate.WithClientProtocol(requeststate.WithGroup(context.Background(), group), protocol.ProtocolAnthropicMessages)
	require.True(t, service.DiagnoseCompatible(ctx, &group.ID, "gpt-test", "").HasModelSupport)
}

func TestAvailableRequestModelsFromProvidersUsesQoderProviderSite(t *testing.T) {
	newProvider := func(id int64, site string) providercore.Record {
		return providercore.Record{
			ID:          id,
			Platform:    capability.PlatformQoder,
			Status:      providercore.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"site": site},
		}
	}

	cnModels := rejectedModelsForContract([]providercore.Record{newProvider(1, "cn")}, capability.PlatformQoder)
	require.ElementsMatch(t, qoder.DefaultRequestModelIDsForSite(qoder.SiteCN), cnModels)
	require.NotContains(t, cnModels, "claude-opus-4-6")

	globalModels := rejectedModelsForContract([]providercore.Record{newProvider(2, "global")}, capability.PlatformQoder)
	require.ElementsMatch(t, qoder.DefaultRequestModelIDsForSite(qoder.SiteGlobal), globalModels)
	require.NotContains(t, globalModels, "minimax-m2.7")

	mixedModels := rejectedModelsForContract([]providercore.Record{newProvider(3, "global"), newProvider(4, "cn")}, capability.PlatformQoder)
	require.ElementsMatch(t, qoder.DefaultRequestModelIDs(), mixedModels)
}

func TestAvailableRequestModelsFromProvidersFiltersConfiguredQoderModels(t *testing.T) {
	newProvider := func(id int64, site string, credentials map[string]any) providercore.Record {
		credentials["site"] = site
		return providercore.Record{
			ID:          id,
			Platform:    capability.PlatformQoder,
			Status:      providercore.StatusActive,
			Schedulable: true,
			Credentials: credentials,
		}
	}

	cnWhitelist := newProvider(11, "cn", map[string]any{
		"model_whitelist": []any{"claude-opus-4-6", "qwen3.6-flash"},
	})
	cnModels := rejectedModelsForContract([]providercore.Record{cnWhitelist}, capability.PlatformQoder)
	require.Equal(t, []string{"qwen3.6-flash"}, cnModels)

	cnMappingOverride := newProvider(12, "cn", map[string]any{
		"model_mapping": map[string]any{"claude-opus-4-6": "ultimate"},
	})
	overrideModels := rejectedModelsForContract([]providercore.Record{cnMappingOverride}, capability.PlatformQoder)
	require.ElementsMatch(t, qoder.DefaultRequestModelIDsForSite(qoder.SiteCN), overrideModels)
	require.NotContains(t, overrideModels, "claude-opus-4-6", "显式映射不能突破站点能力")

	globalWhitelist := newProvider(13, "global", map[string]any{
		"model_whitelist": []any{"claude-opus-4-6", "qwen3.6-flash"},
	})
	mixedModels := rejectedModelsForContract([]providercore.Record{cnWhitelist, globalWhitelist}, capability.PlatformQoder)
	require.ElementsMatch(t, []string{"claude-opus-4-6", "qwen3.6-flash"}, mixedModels)
}

// rejectedModelsForContract applies routing's model rejection rules to fixture records.
func rejectedModelsForContract(values []providercore.Record, platform string) []string {
	sources := make([]routing.ModelRejectionSource, len(values))
	for i := range values {
		sources[i] = ModelRejectionProvider(&values[i])
	}
	return routing.AvailableModelsForRejection(sources, platform)
}
