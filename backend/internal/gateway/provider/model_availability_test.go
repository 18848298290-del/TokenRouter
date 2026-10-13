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

// availabilityProviderStore filters test records by persistent configuration
// for model diagnosis.
type availabilityProviderStore struct {
	// providers supplies source targets for persistent candidate filtering. Keep
	// these values stable during reads, which return ExecutionRecord copies and
	// retain their transient windows for model diagnosis.
	providers []ExecutionProvider
	// fixture target lookup references
	providersByID  map[int64]*ExecutionProvider
	calls          int      // observed persistent candidate queries
	includeGrouped bool     // last ungrouped selection flag
	platforms      []string // copied queried platform names
}

// TestDiagnoseModelAvailabilityForPlatform_NoModel_AlwaysAvailable diagnoses
// an empty pool with blank model input. Both support flags return true to
// retain the conservative unknown-model diagnosis.
func TestDiagnoseModelAvailabilityForPlatform_NoModel_AlwaysAvailable(t *testing.T) {
	repo := &availabilityProviderStore{providers: nil, providersByID: map[int64]*ExecutionProvider{}}
	svc := newAvailabilityForTest(repo, nil, false)

	diag := svc.DiagnoseGeneral(context.Background(), availabilityFixtureGroupID(), "", capability.PlatformOpenAI)

	require.True(t, diag.HasProvidersInPool, "空模型必须保守返回 HasProvidersInPool=true，让调用方继续走 503")
	require.True(t, diag.HasModelSupport, "空模型必须保守返回 HasModelSupport=true，让调用方继续走 503")
}

// TestDiagnoseModelAvailabilityForPlatform_EmptyPlatformKeepsEmptyGroupEmpty
// queries a nonblank model and explicit fixture group with empty platform and
// no providers. Both configured-pool and model-support flags remain false.
func TestDiagnoseModelAvailabilityForPlatform_EmptyPlatformKeepsEmptyGroupEmpty(t *testing.T) {
	repo := &availabilityProviderStore{providers: nil, providersByID: map[int64]*ExecutionProvider{}}
	svc := newAvailabilityForTest(repo, nil, false)

	diag := svc.DiagnoseGeneral(context.Background(), availabilityFixtureGroupID(), "gpt-5", "")

	require.False(t, diag.HasProvidersInPool)
	require.False(t, diag.HasModelSupport)
}

// TestDiagnoseModelAvailabilityForPlatform_NilReceiver calls diagnosis on a
// nil availability service with a model and group. Both flags return true for
// that unavailable diagnostic dependency.
func TestDiagnoseModelAvailabilityForPlatform_NilReceiver(t *testing.T) {
	var svc *routing.ModelAvailability

	diag := svc.DiagnoseGeneral(context.Background(), availabilityFixtureGroupID(), "gpt-5", capability.PlatformOpenAI)

	require.True(t, diag.HasProvidersInPool)
	require.True(t, diag.HasModelSupport)
}

// TestDiagnoseModelAvailabilityForPlatform_NoProvidersInPool queries a
// nonblank GPT model and explicit group against an empty OpenAI provider
// fixture. Neither configured pool presence nor model support is reported.
func TestDiagnoseModelAvailabilityForPlatform_NoProvidersInPool(t *testing.T) {
	repo := &availabilityProviderStore{providers: nil, providersByID: map[int64]*ExecutionProvider{}}
	svc := newAvailabilityForTest(repo, nil, false)

	diag := svc.DiagnoseGeneral(context.Background(), availabilityFixtureGroupID(), "gpt-5", capability.PlatformOpenAI)

	require.False(t, diag.HasProvidersInPool)
	require.False(t, diag.HasModelSupport, "没有提供商表示没有模型支持；调用方会走空池 503 分支")
}

// TestDiagnoseModelAvailabilityForPlatform_ExplicitMappingMatches supplies one
// active provider with an explicit self mapping. Diagnosis reports both
// configured pool presence and support for that requested model.
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

// TestDiagnoseModelAvailabilityAllowsUnknownModelForEmptyScope uses an active
// provider with an empty whitelist. Diagnosis retains support for both tested
// models without catalogue membership restricting them.
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

// TestDiagnoseModelAvailabilityForPlatform_WildcardMappingMatches supplies an
// active provider with a catch-all mapping. Diagnosis reports support for the
// requested model through that configured target.
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

// TestDiagnoseModelAvailabilityForPlatform_NoMatchingModel_ReturnsNotFoundSignal
// uses two group providers whose nonempty whitelists exclude the requested
// model. The pool exists while model support is false for rejection handling.
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

// TestDiagnoseModelAvailabilityForPlatform_RateLimitedSupportingProviderRemainsConfigured
// supplies an Anthropic provider cooling through rate, overload, and temporary
// windows. Ordinary scheduling rejects it while persistent diagnosis still
// reports its configured model support.
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

// TestOpenAIDiagnoseModelAvailabilityForPlatform_RateLimitedSupportingProviderRemainsConfigured
// supplies a compatible OpenAI provider with future health windows and model
// mapping. Scheduling rejects the record while compatible diagnosis retains
// configured pool/model support.
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

// TestDiagnoseModelAvailabilityForPlatform_WrongPlatformFiltersOut queries a
// GPT model under OpenAI platform against an Anthropic-only group fixture.
// Platform filtering leaves both configured-pool and model-support
// observations false.
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

// TestOpenAIGatewayDiagnoseModelAvailabilityForPlatform_GrokPlatformFiltersOpenAIProviders
// queries a Grok model and platform against an active OpenAI-only group
// fixture. Compatible diagnosis excludes the other platform from both
// configured-pool and model-support flags.
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

// ListModelAvailabilityCandidates records query shape and returns copied
// eligible fixture records for the selected group/platform set. Persistent
// active status and scheduling switch qualify; transient cooldowns are
// ignored.
//
// Group membership accepts either GroupIDs or ProviderGroups, while ungrouped
// selection excludes both forms when includeGrouped is false. ExecutionRecord
// supplies supported-container copying before the record enters the result
// slice. Calls are sequential in these tests, and the fixture does not model
// database ordering, transaction isolation, or distributed publication.
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

// availabilityFixtureGroupID returns separately allocated storage for the test
// group ID 71.
func availabilityFixtureGroupID() *int64 { id := int64(71); return &id }

// newAvailabilityForTest assigns group 71 to ungrouped fixtures and fills
// missing credential type with API Key. It then binds persistent diagnosis
// with the optional group-policy service and compatible flag.
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

// TestModelAvailabilityUsesExplicitGroupAcrossPlatforms mixes same-group
// Claude/GPT providers with another group's Gemini provider. Diagnosis
// aggregates only admitted membership, avoids reads without group ID, and
// obeys explicit or forced platform filtering.
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

// TestModelAvailabilityChecksProtocolWithoutTreatingCooldownAsMissingModel
// combines a cooling provider with native-only Messages policy before
// permitting conversion. Pool presence remains while protocol denial removes
// support, then allowed conversion restores support despite cooldown.
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

// TestAvailableRequestModelsFromProvidersUsesQoderProviderSite builds CN and
// Global Qoder providers separately and together. Rejection-model candidates
// follow each site's defaults and their mixed union.
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

// TestAvailableRequestModelsFromProvidersFiltersConfiguredQoderModels supplies
// site-specific whitelist and mapping overrides across CN/Global providers.
// Candidate lists retain supported site names, reject invalid CN aliases, and
// combine valid configured names across sites.
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

// rejectedModelsForContract adapts supplied records to routing model-rejection
// candidate sources.
func rejectedModelsForContract(values []providercore.Record, platform string) []string {
	sources := make([]routing.ModelRejectionSource, len(values))
	for i := range values {
		sources[i] = ModelRejectionProvider(&values[i])
	}
	return routing.AvailableModelsForRejection(sources, platform)
}
