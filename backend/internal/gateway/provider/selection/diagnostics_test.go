package selection

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

type advancedSchedulerDiagnosticSourceStub struct {
	provider  *gatewayprovider.ExecutionProvider
	group     *routing.Group
	providers []gatewayprovider.ExecutionProvider
	pool      []gatewayprovider.ExecutionProvider
}

type advancedSchedulerDiagnosticConcurrencyCache struct {
	schedulercore.ConcurrencyCache
	requests [][]schedulercore.ProviderWithConcurrency
}

// diagnosticWindowCache returns fixture usage in batch reads and errors on individual reads.
type diagnosticWindowCache struct {
	billing.WindowCostCache
	costs map[int64]float64
}

type diagnosticWindowSource struct{}

func (c *advancedSchedulerDiagnosticConcurrencyCache) GetProvidersLoadBatch(_ context.Context, providers []schedulercore.ProviderWithConcurrency) (map[int64]*schedulercore.ProviderLoadInfo, error) {
	c.requests = append(c.requests, append([]schedulercore.ProviderWithConcurrency(nil), providers...))
	result := make(map[int64]*schedulercore.ProviderLoadInfo, len(providers))
	for _, provider := range providers {
		result[provider.ID] = &schedulercore.ProviderLoadInfo{ProviderID: provider.ID}
	}
	return result, nil
}

func (s *advancedSchedulerDiagnosticSourceStub) GetProvider(_ context.Context, _ int64) (*gatewayprovider.ExecutionProvider, error) {
	return s.provider, nil
}

func (s *advancedSchedulerDiagnosticSourceStub) GetGroup(_ context.Context, _ int64) (*routing.Group, error) {
	return s.group, nil
}

func (s *advancedSchedulerDiagnosticSourceStub) ListProvidersForSchedulerScoreFilter(ctx context.Context, _, _, _, _ string, groupID int64, _ string) ([]gatewayprovider.ExecutionProvider, error) {
	if groupID <= 0 && s.group != nil {
		groupID = s.group.ID
	}
	for i := range s.providers {
		prepareSelectionFixtureProvider(ctx, &s.providers[i], &groupID)
	}
	return s.providers, nil
}

func (s *advancedSchedulerDiagnosticSourceStub) ListSchedulableProvidersForAdvancedSchedulerScore(ctx context.Context, groupID *int64, _ string) ([]gatewayprovider.ExecutionProvider, error) {
	for i := range s.pool {
		prepareSelectionFixtureProvider(ctx, &s.pool[i], groupID)
	}
	return s.pool, nil
}

func advancedSchedulerDiagnosticBool(value bool) *bool {
	return &value
}

func advancedSchedulerDiagnosticInt(value int) *int {
	return &value
}

func advancedSchedulerDiagnosticFloat(value float64) *float64 {
	return &value
}

func findAdvancedSchedulerDiagnosticMetric(metrics []policy.AdvancedSchedulerScoreDiagnosticMetric, key string) *policy.AdvancedSchedulerScoreDiagnosticMetric {
	for index := range metrics {
		if metrics[index].Key == key {
			return &metrics[index]
		}
	}
	return nil
}

func findAdvancedSchedulerDiagnosticSetting(settings []policy.AdvancedSchedulerScoreDiagnosticSetting, key string) *policy.AdvancedSchedulerScoreDiagnosticSetting {
	for index := range settings {
		if settings[index].Key == key {
			return &settings[index]
		}
	}
	return nil
}

func findAdvancedSchedulerDiagnosticPolicy(signals []policy.AdvancedSchedulerScoreDiagnosticPolicySignal, key string) *policy.AdvancedSchedulerScoreDiagnosticPolicySignal {
	for index := range signals {
		if signals[index].Key == key {
			return &signals[index]
		}
	}
	return nil
}

func TestAdvancedSchedulerScoreDiagnosticService_UsesActualFormulaAndSafeDTO(t *testing.T) {
	group := &routing.Group{
		ID:   301,
		Name: "advanced",

		SchedulerType: routing.GroupSchedulerTypeAdvanced,
		AdvancedSchedulerOverrides: routing.GroupAdvancedSchedulerOverrides{
			StickyWeightedEnabled:  advancedSchedulerDiagnosticBool(true),
			LBTopK:                 advancedSchedulerDiagnosticInt(2),
			WeightPriority:         advancedSchedulerDiagnosticFloat(2),
			WeightLoad:             advancedSchedulerDiagnosticFloat(1),
			WeightQueue:            advancedSchedulerDiagnosticFloat(0),
			WeightErrorRate:        advancedSchedulerDiagnosticFloat(0),
			WeightTTFT:             advancedSchedulerDiagnosticFloat(0),
			WeightReset:            advancedSchedulerDiagnosticFloat(0),
			WeightQuotaHeadroom:    advancedSchedulerDiagnosticFloat(0),
			WeightPreviousResponse: advancedSchedulerDiagnosticFloat(0),
			WeightSessionSticky:    advancedSchedulerDiagnosticFloat(3),
		},
	}
	target := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 101,
			Name:           "target",
			Platform:       capability.PlatformGemini,
			Type:           capability.ProviderTypeOAuth,
			Status:         billing.StatusActive,
			Schedulable:    true,
			Priority:       1,
			Credentials:    map[string]any{"model_whitelist": []string{"*"}, "access_token": "secret-token"},
			ProviderGroups: []providercore.GroupMembership{{GroupID: group.ID, Group: (*accessview.GroupConfig)(group)}},
		},
	}
	other := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 102,
			Name:        "other",
			Platform:    capability.PlatformGemini,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Schedulable: true,
			Priority:    2,
		},
	}
	source := &advancedSchedulerDiagnosticSourceStub{
		provider:  target,
		group:     group,
		providers: []gatewayprovider.ExecutionProvider{*target, other},
		pool:      []gatewayprovider.ExecutionProvider{*target, other},
	}

	diagnostics := withDiagnosticParameters(newDiagnosticsForTest(source, nil))

	result, err := diagnostics.GetDetail(context.Background(), target.Record.ID, policy.AdvancedSchedulerScoreDiagnosticRequest{
		GroupID:          group.ID,
		StickyProviderID: target.Record.ID,
	})
	require.NoError(t, err)
	require.NotNil(t, result.Detail)
	require.True(t, result.Detail.Eligible)
	require.NotNil(t, result.Detail.Score)
	require.InDelta(t, 2.5, result.Detail.Score.BaseScore, 0.000001)
	require.InDelta(t, 3, result.Detail.Score.StickyBonus, 0.000001)
	require.InDelta(t, 5.5, result.Detail.Score.FinalScore, 0.000001)
	require.Equal(t, "top_k_weighted", result.Detail.Score.SelectionMode)
	require.NotNil(t, result.Detail.Score.SelectionWeight)
	require.InDelta(t, 6, *result.Detail.Score.SelectionWeight, 0.000001)
	require.NotNil(t, result.Detail.Score.SelectionProbability)
	require.InDelta(t, 6.0/7.0, *result.Detail.Score.SelectionProbability, 0.000001)
	require.Contains(t, result.Detail.Score.Formula, "2.0000×1.0000")

	loadMetric := findAdvancedSchedulerDiagnosticMetric(result.Detail.Metrics, "load")
	require.NotNil(t, loadMetric)
	require.True(t, loadMetric.Neutral)
	require.False(t, loadMetric.Available)
	require.InDelta(t, 0.5, loadMetric.NormalizedValue, 0.000001)

	errorMetric := findAdvancedSchedulerDiagnosticMetric(result.Detail.Metrics, "error_rate")
	require.NotNil(t, errorMetric)
	require.True(t, errorMetric.Neutral)
	require.Equal(t, "0%（未观测）", errorMetric.RawValue)
	require.InDelta(t, 1.0, errorMetric.NormalizedValue, 0.000001)
	require.Contains(t, errorMetric.Normalization, "错误率按 0% 计算")

	prioritySetting := findAdvancedSchedulerDiagnosticSetting(result.Detail.EffectiveSettings, "weight_priority")
	require.NotNil(t, prioritySetting)
	require.Equal(t, "group_override", prioritySetting.Source)
	require.Equal(t, "2.0000", prioritySetting.Value)

	payload, marshalErr := json.Marshal(result)
	require.NoError(t, marshalErr)
	require.NotContains(t, string(payload), "credentials")
	require.NotContains(t, string(payload), "secret-token")
	require.NotContains(t, strings.ToLower(string(payload)), "access_token")
}

func TestAdvancedSchedulerScoreDiagnosticService_UsesProcessConfigBeforeFallback(t *testing.T) {
	group := &routing.Group{ID: 501, Name: "advanced", SchedulerType: routing.GroupSchedulerTypeAdvanced}
	target := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 5011,
			Name:           "target",
			Platform:       capability.PlatformGemini,
			Status:         billing.StatusActive,
			Schedulable:    true,
			Priority:       1,
			ProviderGroups: []providercore.GroupMembership{{GroupID: group.ID, Group: (*accessview.GroupConfig)(group)}},
		},
	}
	other := gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 5012, Name: "other", Platform: capability.PlatformGemini, Status: billing.StatusActive, Schedulable: true, Priority: 2}}
	source := &advancedSchedulerDiagnosticSourceStub{
		provider:  target,
		group:     group,
		providers: []gatewayprovider.ExecutionProvider{*target, other},
		pool:      []gatewayprovider.ExecutionProvider{*target, other},
	}
	cfg := &config.Config{
		Gateway: config.GatewayConfig{AdvancedScheduler: config.GatewayAdvancedSchedulerConfig{
			LBTopK: 1,
			ScoreWeights: config.GatewayAdvancedSchedulerScoreWeights{
				Priority: 4,
			},
		}},
	}

	diagnostics := withDiagnosticParameters(newDiagnosticsForTest(source, nil), cfg)

	result, err := diagnostics.GetDetail(context.Background(), target.Record.ID, policy.AdvancedSchedulerScoreDiagnosticRequest{GroupID: group.ID})
	require.NoError(t, err)
	require.NotNil(t, result.Detail)
	require.NotNil(t, result.Detail.Score)
	require.InDelta(t, 4, result.Detail.Score.BaseScore, 0.000001)
	require.Equal(t, 1, result.Detail.CandidatePool.TopK)
	prioritySetting := findAdvancedSchedulerDiagnosticSetting(result.Detail.EffectiveSettings, "weight_priority")
	require.NotNil(t, prioritySetting)
	require.Equal(t, "process_default", prioritySetting.Source)
}

func TestAdvancedSchedulerScoreDiagnosticService_HardStickyForcesProviderOutsideTopK(t *testing.T) {
	group := &routing.Group{
		ID:   701,
		Name: "advanced",

		SchedulerType: routing.GroupSchedulerTypeAdvanced,
		AdvancedSchedulerOverrides: routing.GroupAdvancedSchedulerOverrides{
			StickyWeightedEnabled: advancedSchedulerDiagnosticBool(false),
			LBTopK:                advancedSchedulerDiagnosticInt(1),
			WeightPriority:        advancedSchedulerDiagnosticFloat(1),
			WeightLoad:            advancedSchedulerDiagnosticFloat(0),
			WeightQueue:           advancedSchedulerDiagnosticFloat(0),
			WeightErrorRate:       advancedSchedulerDiagnosticFloat(0),
			WeightTTFT:            advancedSchedulerDiagnosticFloat(0),
			WeightReset:           advancedSchedulerDiagnosticFloat(0),
			WeightQuotaHeadroom:   advancedSchedulerDiagnosticFloat(0),
		},
	}
	target := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 7011, Name: "sticky", Platform: capability.PlatformGemini, Status: billing.StatusActive,
			Schedulable: true, Priority: 100, ProviderGroups: []providercore.GroupMembership{{GroupID: group.ID, Group: (*accessview.GroupConfig)(group)}},
		},
	}
	best := gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 7012, Name: "best", Platform: capability.PlatformGemini, Status: billing.StatusActive, Schedulable: true, Priority: 1}}
	source := &advancedSchedulerDiagnosticSourceStub{
		provider: target, group: group, providers: []gatewayprovider.ExecutionProvider{*target, best}, pool: []gatewayprovider.ExecutionProvider{best, *target},
	}
	diagnostics := withDiagnosticParameters(newDiagnosticsForTest(source, nil))

	result, err := diagnostics.GetDetail(context.Background(), target.Record.ID, policy.AdvancedSchedulerScoreDiagnosticRequest{
		GroupID: group.ID, StickyProviderID: target.Record.ID,
	})

	require.NoError(t, err)
	require.True(t, result.Detail.Eligible)
	require.NotNil(t, result.Detail.Score)
	require.False(t, result.Detail.Score.InTopK)
	require.Equal(t, "sticky_forced_first", result.Detail.Score.SelectionMode)
	require.NotNil(t, result.Detail.Score.SelectionProbability)
	require.Equal(t, 1.0, *result.Detail.Score.SelectionProbability)
	signal := findAdvancedSchedulerDiagnosticPolicy(result.Detail.PolicySignals, "session_sticky")
	require.NotNil(t, signal)
	require.Equal(t, "forced_first", signal.State)
}

func TestAdvancedSchedulerScoreDiagnosticService_SubscriptionPriorityUsesSubscriptionPool(t *testing.T) {
	group := &routing.Group{
		ID:   801,
		Name: "advanced",

		SchedulerType: routing.GroupSchedulerTypeAdvanced,
		AdvancedSchedulerOverrides: routing.GroupAdvancedSchedulerOverrides{
			SubscriptionPriorityEnabled: advancedSchedulerDiagnosticBool(true),
		},
	}
	target := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 8011, Name: "regular", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
			Status: billing.StatusActive, Schedulable: true, ProviderGroups: []providercore.GroupMembership{{GroupID: group.ID, Group: (*accessview.GroupConfig)(group)}},
		},
	}
	subscription := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 8012, Name: "subscription", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth,
			Status: billing.StatusActive, Schedulable: true, Credentials: map[string]any{"model_whitelist": []string{"*"}, "plan_type": "plus"},
		},
	}
	source := &advancedSchedulerDiagnosticSourceStub{
		provider: target, group: group, providers: []gatewayprovider.ExecutionProvider{*target, subscription}, pool: []gatewayprovider.ExecutionProvider{*target, subscription},
	}
	diagnostics := withDiagnosticParameters(newDiagnosticsForTest(source, nil))

	result, err := diagnostics.GetDetail(context.Background(), target.Record.ID, policy.AdvancedSchedulerScoreDiagnosticRequest{GroupID: group.ID})

	require.NoError(t, err)
	require.False(t, result.Detail.Eligible)
	require.Contains(t, result.Detail.HardFilterReasons, "subscription_priority_deferred")
	require.Equal(t, 2, result.Detail.CandidatePool.TotalCandidates)
	require.Equal(t, 1, result.Detail.CandidatePool.EligibleCandidates)
	require.Equal(t, 1, result.Detail.CandidatePool.ExclusionReasons["subscription_priority_deferred"])
	require.Equal(t, subscription.Record.ID, result.Detail.CandidatePool.Candidates[0].ID)
	signal := findAdvancedSchedulerDiagnosticPolicy(result.Detail.PolicySignals, "subscription_priority")
	require.NotNil(t, signal)
	require.Equal(t, "active_pool", signal.State)
}

func TestAdvancedSchedulerScoreDiagnosticService_CountsMoreThanOneThousandExcludedProviders(t *testing.T) {
	group := &routing.Group{ID: 901, Name: "advanced", SchedulerType: routing.GroupSchedulerTypeAdvanced}
	target := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 9011, Name: "target", Platform: capability.PlatformGemini, Status: billing.StatusActive, Schedulable: true,
			ProviderGroups: []providercore.GroupMembership{{GroupID: group.ID, Group: (*accessview.GroupConfig)(group)}},
		},
	}
	allProviders := make([]gatewayprovider.ExecutionProvider, 0, 1002)
	allProviders = append(allProviders, *target)
	for index := range 1001 {
		allProviders = append(allProviders, gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: int64(9100 + index), Platform: capability.PlatformGemini, Status: billing.StatusDisabled}})
	}
	source := &advancedSchedulerDiagnosticSourceStub{provider: target, group: group, providers: allProviders, pool: []gatewayprovider.ExecutionProvider{*target}}
	diagnostics := withDiagnosticParameters(newDiagnosticsForTest(source, nil))

	result, err := diagnostics.GetDetail(context.Background(), target.Record.ID, policy.AdvancedSchedulerScoreDiagnosticRequest{GroupID: group.ID})

	require.NoError(t, err)
	require.Equal(t, 1002, result.Detail.CandidatePool.TotalCandidates)
	require.Equal(t, 1001, result.Detail.CandidatePool.ExcludedCandidates)
	require.Equal(t, 1001, result.Detail.CandidatePool.ExclusionReasons["provider_inactive"])
}

func TestAdvancedSchedulerScoreDiagnosticService_StableSortsLargeCandidatePool(t *testing.T) {
	group := &routing.Group{
		ID: 1001, Name: "advanced", SchedulerType: routing.GroupSchedulerTypeAdvanced,
		AdvancedSchedulerOverrides: routing.GroupAdvancedSchedulerOverrides{
			WeightPriority:      advancedSchedulerDiagnosticFloat(1),
			WeightLoad:          advancedSchedulerDiagnosticFloat(0),
			WeightQueue:         advancedSchedulerDiagnosticFloat(0),
			WeightErrorRate:     advancedSchedulerDiagnosticFloat(0),
			WeightTTFT:          advancedSchedulerDiagnosticFloat(0),
			WeightReset:         advancedSchedulerDiagnosticFloat(0),
			WeightQuotaHeadroom: advancedSchedulerDiagnosticFloat(0),
		},
	}
	providers := make([]gatewayprovider.ExecutionProvider, 0, 1101)
	for index := 1100; index >= 0; index-- {
		providers = append(providers, gatewayprovider.ExecutionProvider{
			Record: providercore.Record{
				Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: int64(10000 + index), Platform: capability.PlatformGemini, Status: billing.StatusActive,
				Schedulable: true, Priority: index % 7,
			},
		})
	}
	target := &providers[len(providers)-1]
	target.Record.ProviderGroups = []providercore.GroupMembership{{GroupID: group.ID, Group: (*accessview.GroupConfig)(group)}}
	source := &advancedSchedulerDiagnosticSourceStub{provider: target, group: group, providers: providers, pool: providers}
	diagnostics := withDiagnosticParameters(newDiagnosticsForTest(source, nil))

	result, err := diagnostics.GetDetail(context.Background(), target.Record.ID, policy.AdvancedSchedulerScoreDiagnosticRequest{GroupID: group.ID})

	require.NoError(t, err)
	require.Len(t, result.Detail.CandidatePool.Candidates, 1101)
	for index := 1; index < len(result.Detail.CandidatePool.Candidates); index++ {
		previous := result.Detail.CandidatePool.Candidates[index-1]
		current := result.Detail.CandidatePool.Candidates[index]
		require.True(t, previous.Priority < current.Priority ||
			(previous.Priority == current.Priority && previous.ID < current.ID),
			"candidate order must follow score comparator at index %d", index)
	}
}

func TestAdvancedSchedulerScoreDiagnosticService_LoadUsesEffectiveLoadFactor(t *testing.T) {
	cache := &advancedSchedulerDiagnosticConcurrencyCache{}
	diagnostics := withDiagnosticParameters(newDiagnosticsForTest(nil, schedulercore.NewConcurrencyService(cache, schedulercore.Diagnostics{
		Logf:  logging.LegacyPrintf,
		Event: logging.Event,
	},
	)))
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1101, Concurrency: 2, LoadFactor: advancedSchedulerDiagnosticInt(7)}}

	core, scope := diagnostics.diagnosticCore()
	core.LoadMap(context.Background(), scope.providers([]*gatewayprovider.ExecutionProvider{provider}))

	require.Len(t, cache.requests, 1)
	require.Equal(t, []schedulercore.ProviderWithConcurrency{{ID: provider.Record.ID, MaxConcurrency: 7}}, cache.requests[0])
}

func TestAdvancedSchedulerScoreDiagnosticService_FiltersModelRuntimeBlock(t *testing.T) {
	group := &routing.Group{ID: 1201, SchedulerType: routing.GroupSchedulerTypeAdvanced}
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 12011, GroupIDs: []int64{1201}, Type: capability.ProviderTypeAPIKey, Platform: capability.PlatformGemini, Status: billing.StatusActive, Schedulable: true,
			Extra: map[string]any{"model_rate_limits": map[string]any{
				"gemini-3-pro": map[string]any{"rate_limit_reset_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
			}},
		},
	}
	diagnostics := withDiagnosticParameters(newDiagnosticsForTest(nil, nil))

	core, scope := diagnostics.diagnosticCore()
	reason := core.HardFilterReason(
		context.Background(), scope.provider(provider), scope.group(group),
		policy.AdvancedSchedulerScoreDiagnosticRequest{GroupID: group.ID, RequestedModel: "gemini-3-pro"}, time.Now(),
	)

	require.Equal(t, "model_runtime_blocked", reason)
}

func TestAdvancedSchedulerScoreDiagnosticService_EscapedStickyUsesRegularWindowCostGate(t *testing.T) {
	group := &routing.Group{
		ID: 1301, SchedulerType: routing.GroupSchedulerTypeAdvanced,
		AdvancedSchedulerOverrides: routing.GroupAdvancedSchedulerOverrides{
			StickyWeightedEnabled: advancedSchedulerDiagnosticBool(false),
		},
	}
	target := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 13011, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true,
			Extra:          map[string]any{"window_cost_limit": 10.0, "window_cost_sticky_reserve": 5.0},
			ProviderGroups: []providercore.GroupMembership{{GroupID: group.ID, Group: (*accessview.GroupConfig)(group)}},
		},
	}
	other := gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 13012, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true}}
	source := &advancedSchedulerDiagnosticSourceStub{
		provider: target, group: group, providers: []gatewayprovider.ExecutionProvider{*target, other}, pool: []gatewayprovider.ExecutionProvider{*target, other},
	}
	cfg := &config.Config{Gateway: config.GatewayConfig{AdvancedScheduler: config.GatewayAdvancedSchedulerConfig{
		StickyEscapeEnabled: true, StickyEscapeTTFTMs: 15000, StickyEscapeErrorRate: 0.55,
	}}}

	feedback := schedulercore.NewRuntimeStats(time.Now)
	for range 4 {
		feedback.Report(target.Record.ID, false, nil)
	}
	diagnostics := withDiagnosticParameters(newDiagnosticsForTest(source, nil))
	diagnostics.feedback = feedback
	diagnostics.schedulerParameters = schedulercore.NewParameters(schedulercore.NewSettingsRuntime(schedulercore.Diagnostics{}), nil, diagnosticParameterDefaults(cfg))
	window := billing.NewWindowCostGuard(&diagnosticWindowCache{costs: map[int64]float64{target.Record.ID: 11}}, diagnosticWindowSource{}, billing.WindowCostGuardOptions{Now: time.Now, Stats: &billing.WindowCostMetrics{}, Log: func(string, ...any) {}, Debug: func(string, ...any) {}})
	diagnostics.gatewayService = NewGeneric(GenericDependencies{Window: window, WindowPrefetchAvailable: true}, DefaultOptions())

	result, err := diagnostics.GetDetail(context.Background(), target.Record.ID, policy.AdvancedSchedulerScoreDiagnosticRequest{
		GroupID: group.ID, StickyProviderID: target.Record.ID,
	})

	require.NoError(t, err)
	require.False(t, result.Detail.Eligible)
	require.Contains(t, result.Detail.HardFilterReasons, "window_cost_exceeded")
	signal := findAdvancedSchedulerDiagnosticPolicy(result.Detail.PolicySignals, "session_sticky")
	require.NotNil(t, signal)
	require.Equal(t, "escaped", signal.State)
}

// newDiagnosticsForTest creates a read-only diagnostic service for tests.
func newDiagnosticsForTest(source DiagnosticSource, concurrency *schedulercore.ConcurrencyService) *Diagnostics {
	return NewDiagnostics(source, Shared{Concurrency: concurrency}, nil, nil)
}

func withDiagnosticParameters(value *Diagnostics, configs ...*config.Config) *Diagnostics {
	var cfg *config.Config
	if len(configs) > 0 {
		cfg = configs[0]
	}
	value.schedulerParameters = schedulercore.NewParameters(schedulercore.NewSettingsRuntime(schedulercore.Diagnostics{}), nil, diagnosticParameterDefaults(cfg))
	return value
}

func (c *diagnosticWindowCache) GetWindowCostBatch(_ context.Context, ids []int64) (map[int64]float64, error) {
	out := make(map[int64]float64)
	for _, id := range ids {
		if value, ok := c.costs[id]; ok {
			out[id] = value
		}
	}
	return out, nil
}

// providers converts fixture candidates to diagnostic records and preserves nil input.
func (s *diagnosticScope) providers(values []*gatewayprovider.ExecutionProvider) []*schedulercore.DiagnosticProvider {
	if values == nil {
		return nil
	}
	out := make([]*schedulercore.DiagnosticProvider, len(values))
	for i, v := range values {
		out[i] = s.provider(v)
	}
	return out
}

func (diagnosticWindowSource) GetWindow(context.Context, int64, time.Time) (*billing.WindowCostStats, error) {
	return &billing.WindowCostStats{}, nil
}

func (diagnosticWindowSource) GetWindows(context.Context, []int64, time.Time) (map[int64]*billing.WindowCostStats, error) {
	return map[int64]*billing.WindowCostStats{}, nil
}
