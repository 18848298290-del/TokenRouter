package selection

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	routingtestkit "github.com/TokenFlux/TokenRouter/internal/routing/testkit"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// groupAwareMockProviderRepo filters fixture records by platform and stored
// ProviderGroups membership.
type groupAwareMockProviderRepo struct {
	*mockProviderRepoForPlatform // full-read platform fixture
	// allProviders shares the source slice indexed by the embedded fixture.
	// Candidate query results retain referenced record data from that same fixture input.
	allProviders []gatewayprovider.
			ExecutionProvider
}

// TestAdvancedSchedulerCoreSelectsNonOpenAIGroupAndMarksResult supplies an
// advanced Gemini group and one eligible target to the generic core. The result
// is marked advanced, while separately prepared basic results retain basic
// feedback classification.
func TestAdvancedSchedulerCoreSelectsNonOpenAIGroupAndMarksResult(t *testing.T) {
	groupID := int64(42)
	group := &routing.Group{ID: groupID, SchedulerType: routing.GroupSchedulerTypeAdvanced}
	ctx := requeststate.WithGroup(context.Background(), group)
	service := newGenericSelectionForTest(GenericDependencies{Reads: Reads{}, Shared: Shared{}}, nil)

	core, scope := service.genericSelector()
	result, selected, err := core.TryAdvanced(ctx, &groupID, "session", scope.loads([]providerWithLoad{
		{
			provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 101, Platform: capability.PlatformGemini, Priority: 1, Schedulable: true, Status: billing.StatusActive}},
			loadInfo: &schedulercore.ProviderLoadInfo{ProviderID: 101, LoadRate: 0},
		},
	}))
	selection := scope.restore(result)

	require.NoError(t, err)
	require.True(t, selected)
	require.NotNil(t, selection)
	require.Equal(t, int64(101), selection.Provider.Record.ID)
	require.True(t, selection.AdvancedScheduler)

	basicCtx := requeststate.WithGroup(context.Background(), &routing.Group{ID: 43, SchedulerType: routing.GroupSchedulerTypeBasic})
	basicSelection, err := service.newSelectionResult(basicCtx, &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 102}}, true, func() {}, nil)
	require.NoError(t, err)
	require.False(t, basicSelection.AdvancedScheduler)
}

// ListSchedulableUngroupedByPlatform returns schedulable fixture records
// matching platform with no ProviderGroups entries. The fixture defines
// ungrouped status from that slice, and returned values retain referenced
// record data.
func (m *groupAwareMockProviderRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	var result []gatewayprovider.ExecutionProvider
	for _, acc := range m.allProviders {
		if acc.Record.Platform == platform && acc.View().IsSchedulable() && len(acc.Record.ProviderGroups) == 0 {
			result = append(result, acc)
		}
	}
	return result, nil
}

// ListSchedulableUngroupedByPlatforms returns schedulable fixture records in
// the supplied platform set with empty ProviderGroups. Selection tests use that
// fixture definition of ungrouped membership, while record references remain
// shared.
func (m *groupAwareMockProviderRepo) ListSchedulableUngroupedByPlatforms(ctx context.Context, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
	platformSet := make(map[string]bool, len(platforms))
	for _, p := range platforms {
		platformSet[p] = true
	}
	var result []gatewayprovider.ExecutionProvider
	for _, acc := range m.allProviders {
		if platformSet[acc.Record.Platform] && acc.View().IsSchedulable() && len(acc.Record.ProviderGroups) == 0 {
			result = append(result, acc)
		}
	}
	return result, nil
}

// ListSchedulableByGroupIDAndPlatform returns schedulable fixture members
// matching one requested platform.
func (m *groupAwareMockProviderRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	var result []gatewayprovider.ExecutionProvider
	for _, acc := range m.allProviders {
		if acc.Record.Platform == platform && acc.View().IsSchedulable() && providerBelongsToGroup(acc, groupID) {
			result = append(result, acc)
		}
	}
	return result, nil
}

// ListSchedulableByGroupIDAndPlatforms returns schedulable fixture members
// across the supplied platform set.
func (m *groupAwareMockProviderRepo) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
	platformSet := make(map[string]bool, len(platforms))
	for _, p := range platforms {
		platformSet[p] = true
	}
	var result []gatewayprovider.ExecutionProvider
	for _, acc := range m.allProviders {
		if platformSet[acc.Record.Platform] && acc.View().IsSchedulable() && providerBelongsToGroup(acc, groupID) {
			result = append(result, acc)
		}
	}
	return result, nil
}

// providerBelongsToGroup reports whether the fixture's ProviderGroups contains
// the supplied group identifier.
func providerBelongsToGroup(acc gatewayprovider.ExecutionProvider, groupID int64) bool {
	for _, ag := range acc.Record.ProviderGroups {
		if ag.GroupID == groupID {
			return true
		}
	}
	return false
}

// newGroupAwareMockRepo indexes fixture targets for full reads while retaining
// their candidate source slice. The repository and allProviders share that
// slice, so fixture setup updates remain visible to both query paths.
func newGroupAwareMockRepo(providers []gatewayprovider.ExecutionProvider) *groupAwareMockProviderRepo {
	byID := make(map[int64]*gatewayprovider.ExecutionProvider, len(providers))
	for i := range providers {
		byID[providers[i].Record.ID] = &providers[i]
	}
	return &groupAwareMockProviderRepo{
		mockProviderRepoForPlatform: &mockProviderRepoForPlatform{
			providers:     providers,
			providersByID: byID,
		},
		allProviders: providers,
	}
}

// TestGroupIsolation_UngroupedKey_ShouldNotScheduleGroupedProviders supplies
// grouped targets but omits the request group. Forced-platform selection
// returns no target, protecting the requirement for an admitted group before
// candidate lookup.
func TestGroupIsolation_UngroupedKey_ShouldNotScheduleGroupedProviders(t *testing.T) {
	ctx := context.Background()

	providers := []gatewayprovider.ExecutionProvider{
		{Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Priority: 1, Status: billing.StatusActive, Schedulable: true,
			ProviderGroups: []providercore.GroupMembership{{GroupID: 100}},
		}},
		{Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Priority: 2, Status: billing.StatusActive, Schedulable: true,
			ProviderGroups: []providercore.GroupMembership{{GroupID: 200}},
		}},
	}
	repo := newGroupAwareMockRepo(providers)
	cache := &mockGatewayCacheForPlatform{}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, nil, "", "", nil, capability.PlatformOpenAI)
	require.Error(t, err, "无分组 Key 不应调度到已分组提供商")
	require.Nil(t, acc)
}

// TestGroupIsolation_GroupedKey_ShouldNotScheduleUngroupedProviders requests a
// positive group against fixtures with no memberships. Selection returns an
// error and nil provider, protecting group membership during forced-platform
// choice.
func TestGroupIsolation_GroupedKey_ShouldNotScheduleUngroupedProviders(t *testing.T) {
	ctx := context.Background()
	groupID := int64(100)

	providers := []gatewayprovider.ExecutionProvider{
		{Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Priority: 1, Status: billing.StatusActive, Schedulable: true,
			ProviderGroups: nil,
		}},
		{Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Priority: 2, Status: billing.StatusActive, Schedulable: true,
			ProviderGroups: []providercore.GroupMembership{},
		}},
	}
	repo := newGroupAwareMockRepo(providers)
	cache := &mockGatewayCacheForPlatform{}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, &groupID, "", "", nil, capability.PlatformOpenAI)
	require.Error(t, err, "有分组 Key 不应调度到未分组提供商")
	require.Nil(t, acc)
}

// TestGroupIsolation_UngroupedKey_RejectsUngroupedProviders mixes grouped and
// ungrouped records while omitting the request group. The request is rejected,
// protecting admission even when otherwise eligible ungrouped candidates exist.
func TestGroupIsolation_UngroupedKey_RejectsUngroupedProviders(t *testing.T) {
	ctx := context.Background()

	providers := []gatewayprovider.ExecutionProvider{
		{Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Priority: 1, Status: billing.StatusActive, Schedulable: true,
			ProviderGroups: []providercore.GroupMembership{{GroupID: 100}},
		}}, // Grouped provider; ineligible without a request group.
		{Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Priority: 2, Status: billing.StatusActive, Schedulable: true,
			ProviderGroups: nil,
		}}, // Ungrouped providers cannot serve a request lacking a group either.
		{Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 3, Platform: capability.PlatformOpenAI, Priority: 3, Status: billing.StatusActive, Schedulable: true,
			ProviderGroups: []providercore.GroupMembership{{GroupID: 200}},
		}}, // Grouped provider; ineligible without a request group.
	}
	repo := newGroupAwareMockRepo(providers)
	cache := &mockGatewayCacheForPlatform{}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, nil, "", "", nil, capability.PlatformOpenAI)
	require.Error(t, err, "请求必须明确绑定分组")
	require.Nil(t, acc)
}

// TestGroupIsolation_GroupedKey_ShouldOnlyScheduleMatchingGroupProviders mixes
// another group, an ungrouped target, and one requested-group member. Selection
// chooses the matching member, protecting candidate isolation across stored
// memberships.
func TestGroupIsolation_GroupedKey_ShouldOnlyScheduleMatchingGroupProviders(t *testing.T) {
	ctx := context.Background()
	groupID := int64(100)

	providers := []gatewayprovider.ExecutionProvider{
		{Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Priority: 1, Status: billing.StatusActive, Schedulable: true,
			ProviderGroups: nil,
		}}, // Ungrouped; excluded.
		{Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Priority: 2, Status: billing.StatusActive, Schedulable: true,
			ProviderGroups: []providercore.GroupMembership{{GroupID: 200}},
		}}, // Member of group 200; excluded.
		{Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 3, Platform: capability.PlatformOpenAI, Priority: 3, Status: billing.StatusActive, Schedulable: true,
			ProviderGroups: []providercore.GroupMembership{{GroupID: 100}},
		}}, // Member of group 100; eligible.
	}
	repo := newGroupAwareMockRepo(providers)
	cache := &mockGatewayCacheForPlatform{}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, &groupID, "", "", nil, capability.PlatformOpenAI)
	require.NoError(t, err, "应成功调度分组内提供商")
	require.NotNil(t, acc)
	require.Equal(t, int64(3), acc.Record.ID, "应选中分组 100 内的提供商 ID=3")
}

// TestGroupIsolation_RequiresExplicitGroup uses a permissive platform fixture
// containing both membership kinds. A nil group still returns no target,
// protecting the selector's admission gate independently of repository
// filtering.
func TestGroupIsolation_RequiresExplicitGroup(t *testing.T) {
	ctx := context.Background()

	providers := []gatewayprovider.ExecutionProvider{
		{Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Priority: 2, Status: billing.StatusActive, Schedulable: true,
			ProviderGroups: []providercore.GroupMembership{{GroupID: 100}},
		}}, // Grouped.
		{Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Priority: 1, Status: billing.StatusActive, Schedulable: true,
			ProviderGroups: nil,
		}}, // Ungrouped.
	}

	// The basic mock returns all platform matches without group filtering.
	byID := make(map[int64]*gatewayprovider.ExecutionProvider, len(providers))
	for i := range providers {
		byID[providers[i].Record.ID] = &providers[i]
	}
	repo := &mockProviderRepoForPlatform{
		providers:     providers,
		providersByID: byID,
	}
	cache := &mockGatewayCacheForPlatform{}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, &config.Config{})

	acc, err := svc.selectProviderForModelWithPlatform(ctx, nil, "", "", nil, capability.PlatformOpenAI)
	require.Error(t, err, "请求必须明确绑定分组")
	require.Nil(t, acc)
}

// TestGroupIsolation_RejectsImplicitGroupedProvider supplies a provider whose
// stored membership names a group but leaves the request group nil. Selection
// rejects it, protecting admission from inferring group authority from
// candidate records.
func TestGroupIsolation_RejectsImplicitGroupedProvider(t *testing.T) {
	ctx := context.Background()

	providers := []gatewayprovider.ExecutionProvider{
		{Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Priority: 1, Status: billing.StatusActive, Schedulable: true,
			ProviderGroups: []providercore.GroupMembership{{GroupID: 100}},
		}},
	}

	byID := make(map[int64]*gatewayprovider.ExecutionProvider, len(providers))
	for i := range providers {
		byID[providers[i].Record.ID] = &providers[i]
	}
	repo := &mockProviderRepoForPlatform{
		providers:     providers,
		providersByID: byID,
	}
	cache := &mockGatewayCacheForPlatform{}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, &config.Config{})

	acc, err := svc.selectProviderForModelWithPlatform(ctx, nil, "", "", nil, capability.PlatformOpenAI)
	require.Error(t, err, "请求必须明确绑定分组")
	require.Nil(t, acc)
}

// ptr returns a pointer to the supplied value for fixture fields.
func ptr[T any](v T) *T {
	return &v
}

// TestGatewayService_SelectProviderForModelWithPlatform_Anthropic supplies
// Anthropic and Antigravity candidates under a forced Anthropic platform. Basic
// slot-free selection returns the highest-priority Anthropic target, protecting
// platform filtering before ordering.
func TestGatewayService_SelectProviderForModelWithPlatform_Anthropic(t *testing.T) {
	ctx := context.Background()

	repo := &mockProviderRepoForPlatform{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAnthropic, Priority: 2, Status: billing.StatusActive, Schedulable: true}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 3, Platform: capability.PlatformAntigravity, Priority: 1, Status: billing.StatusActive, Schedulable: true}}, // Excluded by platform.
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForPlatform{}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "", "claude-3-5-sonnet-20241022", nil, capability.PlatformAnthropic)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(1), acc.Record.ID, "应选择优先级最高的 anthropic 提供商")
	require.Equal(t, capability.PlatformAnthropic, acc.Record.Platform, "应只返回 anthropic 平台提供商")
}

// TestGatewayService_SelectProviderForModelWithPlatform_Antigravity supplies
// multiple platforms while forcing Antigravity for the request. Selection
// returns its eligible member, protecting forced-platform filtering within the
// requested group.
func TestGatewayService_SelectProviderForModelWithPlatform_Antigravity(t *testing.T) {
	ctx := context.Background()

	repo := &mockProviderRepoForPlatform{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true}}, // Excluded by platform.
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAntigravity, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForPlatform{}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "", "claude-sonnet-4-5", nil, capability.PlatformAntigravity)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(2), acc.Record.ID)
	require.Equal(t, capability.PlatformAntigravity, acc.Record.Platform, "应只返回 antigravity 平台提供商")
}

// TestGatewayService_SelectProviderForModelWithPlatform_PriorityAndLastUsed
// supplies equal-priority Anthropic targets with different past use timestamps.
// Selection chooses the older target, protecting recency ordering after the
// priority tie.
func TestGatewayService_SelectProviderForModelWithPlatform_PriorityAndLastUsed(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	repo := &mockProviderRepoForPlatform{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true, LastUsedAt: ptr(now.Add(-1 * time.Hour))}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true, LastUsedAt: ptr(now.Add(-2 * time.Hour))}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForPlatform{}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "", "claude-3-5-sonnet-20241022", nil, capability.PlatformAnthropic)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(2), acc.Record.ID, "同优先级应选择最久未用的提供商")
}

// TestGatewayService_SelectProviderForModelWithPlatform_GeminiOAuthPreference
// supplies equal-priority Gemini API Key and OAuth records. The never-used
// OAuth target wins, protecting basic authentication preference on the
// forced-platform path.
func TestGatewayService_SelectProviderForModelWithPlatform_GeminiOAuthPreference(t *testing.T) {
	ctx := context.Background()

	repo := &mockProviderRepoForPlatform{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformGemini, Priority: 1, Status: billing.StatusActive, Schedulable: true, Type: capability.ProviderTypeAPIKey}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformGemini, Priority: 1, Status: billing.StatusActive, Schedulable: true, Type: capability.ProviderTypeOAuth}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForPlatform{}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "", "gemini-2.5-pro", nil, capability.PlatformGemini)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(2), acc.Record.ID, "同优先级且未使用时应优先选择OAuth提供商")
}

// TestGatewayService_SelectProviderForModelWithPlatform_NoAvailableProviders
// supplies an empty candidate pool under a forced Anthropic platform. Selection
// returns nil with ErrNoAvailableProviders, protecting typed exhaustion
// handling for this slot-free model entry.
func TestGatewayService_SelectProviderForModelWithPlatform_NoAvailableProviders(t *testing.T) {
	ctx := context.Background()

	repo := &mockProviderRepoForPlatform{
		providers:     []gatewayprovider.ExecutionProvider{},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}

	cache := &mockGatewayCacheForPlatform{}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "", "claude-3-5-sonnet-20241022", nil, capability.PlatformAnthropic)
	require.Error(t, err)
	require.Nil(t, acc)
	require.ErrorIs(t, err, schedulercore.ErrNoAvailableProviders)
}

// TestGatewayService_SelectProviderForModelWithPlatform_AllExcluded excludes
// every configured Anthropic fixture target from the current request. Selection
// returns no provider, protecting request exclusions before priority or sticky
// choice.
func TestGatewayService_SelectProviderForModelWithPlatform_AllExcluded(t *testing.T) {
	ctx := context.Background()

	repo := &mockProviderRepoForPlatform{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForPlatform{}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

	excludedIDs := map[int64]struct{}{1: {}, 2: {}}
	acc, err := svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "", "claude-3-5-sonnet-20241022", excludedIDs, capability.PlatformAnthropic)
	require.Error(t, err)
	require.Nil(t, acc)
}

// TestGatewayService_SelectProviderForModelWithPlatform_Schedulability varies
// active cooldowns, overload, status, and manual scheduling flags. Selection
// skips currently unavailable targets and admits expired overloads, protecting
// record-state gates before ordering.
func TestGatewayService_SelectProviderForModelWithPlatform_Schedulability(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	tests := []struct {
		name       string                              // named scheduling-state scenario
		providers  []gatewayprovider.ExecutionProvider // configured candidate fixture pool
		expectedID int64                               // expected selected provider identifier
	}{
		{
			name: "过载提供商被跳过",
			providers: []gatewayprovider.ExecutionProvider{
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true, OverloadUntil: ptr(now.Add(1 * time.Hour))}},
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAnthropic, Priority: 2, Status: billing.StatusActive, Schedulable: true}},
			},
			expectedID: 2,
		},
		{
			name: "限流提供商被跳过",
			providers: []gatewayprovider.ExecutionProvider{
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true, RateLimitResetAt: ptr(now.Add(1 * time.Hour))}},
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAnthropic, Priority: 2, Status: billing.StatusActive, Schedulable: true}},
			},
			expectedID: 2,
		},
		{
			name: "非active提供商被跳过",
			providers: []gatewayprovider.ExecutionProvider{
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Priority: 1, Status: "error", Schedulable: true}},
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAnthropic, Priority: 2, Status: billing.StatusActive, Schedulable: true}},
			},
			expectedID: 2,
		},
		{
			name: "schedulable=false被跳过",
			providers: []gatewayprovider.ExecutionProvider{
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: false}},
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAnthropic, Priority: 2, Status: billing.StatusActive, Schedulable: true}},
			},
			expectedID: 2,
		},
		{
			name: "过期的过载提供商可调度",
			providers: []gatewayprovider.ExecutionProvider{
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true, OverloadUntil: ptr(now.Add(-1 * time.Hour))}},
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAnthropic, Priority: 2, Status: billing.StatusActive, Schedulable: true}},
			},
			expectedID: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &mockProviderRepoForPlatform{
				providers:     tt.providers,
				providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
			}
			for i := range repo.providers {
				repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
			}

			cache := &mockGatewayCacheForPlatform{}

			svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

			acc, err := svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "", "claude-3-5-sonnet-20241022", nil, capability.PlatformAnthropic)
			require.NoError(t, err)
			require.NotNil(t, acc)
			require.Equal(t, tt.expectedID, acc.Record.ID)
		})
	}
}

// TestGatewayService_SelectProviderForModelWithPlatform_StickySession varies
// bound provider platform, exclusions, and status under a forced platform.
// Eligible affinity wins, while unusable bindings advance to an eligible target
// in that platform.
func TestGatewayService_SelectProviderForModelWithPlatform_StickySession(t *testing.T) {
	ctx := context.Background()

	t.Run("粘性会话命中-同平台", func(t *testing.T) {
		repo := &mockProviderRepoForPlatform{
			providers: []gatewayprovider.ExecutionProvider{
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Priority: 2, Status: billing.StatusActive, Schedulable: true}},
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
			},
			providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
		}
		for i := range repo.providers {
			repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
		}

		cache := &mockGatewayCacheForPlatform{
			sessionBindings: map[string]int64{"session-123": 1},
		}

		svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

		acc, err := svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "session-123", "claude-3-5-sonnet-20241022", nil, capability.PlatformAnthropic)
		require.NoError(t, err)
		require.NotNil(t, acc)
		require.Equal(t, int64(1), acc.Record.ID, "应返回粘性会话绑定的提供商")
	})

	t.Run("粘性会话不匹配平台-降级选择", func(t *testing.T) {
		repo := &mockProviderRepoForPlatform{
			providers: []gatewayprovider.ExecutionProvider{
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAntigravity, Priority: 2, Status: billing.StatusActive, Schedulable: true}}, // The bound provider belongs to a different platform.
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
			},
			providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
		}
		for i := range repo.providers {
			repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
		}

		cache := &mockGatewayCacheForPlatform{
			sessionBindings: map[string]int64{"session-123": 1}, // Bind an Antigravity provider.
		}

		svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

		// An Anthropic request cannot reuse the Antigravity binding under a forced platform.
		acc, err := svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "session-123", "claude-3-5-sonnet-20241022", nil, capability.PlatformAnthropic)
		require.NoError(t, err)
		require.NotNil(t, acc)
		require.Equal(t, int64(2), acc.Record.ID, "粘性会话提供商平台不匹配，应降级选择同平台提供商")
		require.Equal(t, capability.PlatformAnthropic, acc.Record.Platform)
	})

	t.Run("粘性会话提供商被排除-降级选择", func(t *testing.T) {
		repo := &mockProviderRepoForPlatform{
			providers: []gatewayprovider.ExecutionProvider{
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Priority: 2, Status: billing.StatusActive, Schedulable: true}},
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
			},
			providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
		}
		for i := range repo.providers {
			repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
		}

		cache := &mockGatewayCacheForPlatform{
			sessionBindings: map[string]int64{"session-123": 1},
		}

		svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

		excludedIDs := map[int64]struct{}{1: {}}
		acc, err := svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "session-123", "claude-3-5-sonnet-20241022", excludedIDs, capability.PlatformAnthropic)
		require.NoError(t, err)
		require.NotNil(t, acc)
		require.Equal(t, int64(2), acc.Record.ID, "粘性会话提供商被排除，应选择其他提供商")
	})

	t.Run("粘性会话提供商不可调度-降级选择", func(t *testing.T) {
		repo := &mockProviderRepoForPlatform{
			providers: []gatewayprovider.ExecutionProvider{
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Priority: 2, Status: "error", Schedulable: true}},
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
			},
			providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
		}
		for i := range repo.providers {
			repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
		}

		cache := &mockGatewayCacheForPlatform{
			sessionBindings: map[string]int64{"session-123": 1},
		}

		svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

		acc, err := svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "session-123", "claude-3-5-sonnet-20241022", nil, capability.PlatformAnthropic)
		require.NoError(t, err)
		require.NotNil(t, acc)
		require.Equal(t, int64(2), acc.Record.ID, "粘性会话提供商不可调度，应选择其他提供商")
	})
}

// TestGatewayService_SelectProviderForModelWithPlatform_RoutedStickySessionClears
// binds a disabled target included in the group model-routing list. Selection
// deletes that binding and chooses the usable preferred member, protecting
// affinity cleanup within routing shortcuts.
func TestGatewayService_SelectProviderForModelWithPlatform_RoutedStickySessionClears(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10)
	requestedModel := "claude-3-5-sonnet-20241022"

	repo := &mockProviderRepoForPlatform{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Priority: 2, Status: billing.StatusDisabled, Schedulable: true}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForPlatform{
		sessionBindings: map[string]int64{"session-123": 1},
	}

	groupRepo := &mockGroupRepoForGateway{
		groups: map[int64]*routing.Group{
			groupID: {
				ID:   groupID,
				Name: "route-group",

				Status:              billing.StatusActive,
				Hydrated:            true,
				ModelRoutingEnabled: true,
				ModelRouting: map[string][]int64{
					requestedModel: {1, 2},
				},
			},
		},
	}

	svc := newGenericSelectionForTest(GenericDependencies{
		Reads: Reads{
			Groups:    groupRepo,
			Providers: repo,
		},
		Shared: Shared{Cache: cache},
	}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, &groupID, "session-123", requestedModel, nil, capability.PlatformAnthropic)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(2), acc.Record.ID)
	require.Equal(t, 1, cache.deletedSessions["session-123"])
	require.Equal(t, int64(2), cache.sessionBindings["session-123"])
}

// TestGatewayService_SelectProviderForModelWithPlatform_RoutedStickySessionHit
// gives the bound target membership in the model-preferred provider list.
// The returned ID matches that member, which also has the highest priority in
// this fixture's eligible pool.
func TestGatewayService_SelectProviderForModelWithPlatform_RoutedStickySessionHit(t *testing.T) {
	ctx := context.Background()
	groupID := int64(11)
	requestedModel := "claude-3-5-sonnet-20241022"

	repo := &mockProviderRepoForPlatform{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAnthropic, Priority: 2, Status: billing.StatusActive, Schedulable: true}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForPlatform{
		sessionBindings: map[string]int64{"session-456": 1},
	}

	groupRepo := &mockGroupRepoForGateway{
		groups: map[int64]*routing.Group{
			groupID: {
				ID:   groupID,
				Name: "route-group-hit",

				Status:              billing.StatusActive,
				Hydrated:            true,
				ModelRoutingEnabled: true,
				ModelRouting: map[string][]int64{
					requestedModel: {1, 2},
				},
			},
		},
	}

	svc := newGenericSelectionForTest(GenericDependencies{
		Reads: Reads{
			Groups:    groupRepo,
			Providers: repo,
		},
		Shared: Shared{Cache: cache},
	}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, &groupID, "session-456", requestedModel, nil, capability.PlatformAnthropic)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(1), acc.Record.ID)
}

// TestGatewayService_SelectProviderForModelWithPlatform_RoutedFallbackToNormal
// lists a missing provider as the model preference. Selection falls back to the
// group's normal eligible pool, protecting unavailable preference handling.
func TestGatewayService_SelectProviderForModelWithPlatform_RoutedFallbackToNormal(t *testing.T) {
	ctx := context.Background()
	groupID := int64(12)
	requestedModel := "claude-3-5-sonnet-20241022"

	repo := &mockProviderRepoForPlatform{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAnthropic, Priority: 2, Status: billing.StatusActive, Schedulable: true}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForPlatform{}

	groupRepo := &mockGroupRepoForGateway{
		groups: map[int64]*routing.Group{
			groupID: {
				ID:   groupID,
				Name: "route-fallback",

				Status:              billing.StatusActive,
				Hydrated:            true,
				ModelRoutingEnabled: true,
				ModelRouting: map[string][]int64{
					requestedModel: {99},
				},
			},
		},
	}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo, Groups: groupRepo}, Shared: Shared{Cache: cache}}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, &groupID, "", requestedModel, nil, capability.PlatformAnthropic)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(1), acc.Record.ID)
}

// TestGatewayService_SelectProviderForModelWithPlatform_NoModelSupport supplies
// a narrower Anthropic identity list than the requested model. Selection
// returns a typed group-model rejection with available names, protecting
// configured-support diagnosis.
func TestGatewayService_SelectProviderForModelWithPlatform_NoModelSupport(t *testing.T) {
	ctx := context.Background()

	repo := &mockProviderRepoForPlatform{
		providers: []gatewayprovider.ExecutionProvider{
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 1,
					Platform:    capability.PlatformAnthropic,
					Priority:    1,
					Status:      billing.StatusActive,
					Schedulable: true,
					Credentials: map[string]any{"model_mapping": map[string]any{"claude-3-5-haiku-20241022": "claude-3-5-haiku-20241022"}},
				},
			},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForPlatform{}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "", "claude-3-5-sonnet-20241022", nil, capability.PlatformAnthropic)
	require.Error(t, err)
	require.Nil(t, acc)
	var modelErr *routing.GroupModelUnsupportedError
	require.True(t, errors.As(err, &modelErr))
	require.Equal(t, capability.PlatformAnthropic, modelErr.Platform)
	require.Equal(t, "claude-3-5-sonnet-20241022", modelErr.RequestedModel)
	require.Equal(t, []string{"claude-3-5-haiku-20241022"}, modelErr.AvailableModels)
	require.Contains(t, err.Error(), `The current group does not support the requested model "claude-3-5-sonnet-20241022"`)
	require.Contains(t, err.Error(), "Available models: claude-3-5-haiku-20241022")
}

// TestGatewayService_SelectProviderForModelWithPlatform_ModelRateLimitedNotGroupUnsupported
// cools down a supporting provider beside one lacking the model. Exhaustion
// remains ErrNoAvailableProviders, protecting the distinction between current
// capacity and configured model absence.
func TestGatewayService_SelectProviderForModelWithPlatform_ModelRateLimitedNotGroupUnsupported(t *testing.T) {
	ctx := context.Background()
	resetAt := time.Now().Add(10 * time.Minute).Format(time.RFC3339)

	repo := &mockProviderRepoForPlatform{
		providers: []gatewayprovider.ExecutionProvider{
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 1,
					Platform:    capability.PlatformAnthropic,
					Priority:    1,
					Status:      billing.StatusActive,
					Schedulable: true,
					Credentials: map[string]any{"model_mapping": map[string]any{"claude-3-5-sonnet-20241022": "claude-3-5-sonnet-20241022"}},
					Extra: map[string]any{
						"model_rate_limits": map[string]any{
							"claude-3-5-sonnet-20241022": map[string]any{"rate_limit_reset_at": resetAt},
						},
					},
				},
			},
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 2,
					Platform:    capability.PlatformAnthropic,
					Priority:    2,
					Status:      billing.StatusActive,
					Schedulable: true,
					Credentials: map[string]any{"model_mapping": map[string]any{"claude-3-5-haiku-20241022": "claude-3-5-haiku-20241022"}},
				},
			},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	svc := newGenericSelectionForTest(GenericDependencies{
		Reads:  Reads{Providers: repo},
		Shared: Shared{Cache: &mockGatewayCacheForPlatform{}},
	}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "", "claude-3-5-sonnet-20241022", nil, capability.PlatformAnthropic)
	require.Error(t, err)
	require.Nil(t, acc)
	var modelErr *routing.GroupModelUnsupportedError
	require.False(t, errors.As(err, &modelErr))
	require.ErrorIs(t, err, schedulercore.ErrNoAvailableProviders)
}

// TestGatewayService_SelectProviderForModelWithPlatform_GeminiPreferOAuth
// supplies equal-priority Gemini targets with different authentication kinds.
// The never-used OAuth record is selected, protecting this alternate
// forced-platform entry's tie preference.
func TestGatewayService_SelectProviderForModelWithPlatform_GeminiPreferOAuth(t *testing.T) {
	ctx := context.Background()

	repo := &mockProviderRepoForPlatform{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformGemini, Priority: 1, Status: billing.StatusActive, Schedulable: true, Type: capability.ProviderTypeAPIKey}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformGemini, Priority: 1, Status: billing.StatusActive, Schedulable: true, Type: capability.ProviderTypeOAuth}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForPlatform{}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "", "gemini-2.5-pro", nil, capability.PlatformGemini)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(2), acc.Record.ID)
}

// TestGatewayService_SelectProviderForModelWithPlatform_GeminiAPIKeyModelMappingFilter
// gives API Key targets different configured models. Selection chooses the
// supporting target or reports the complete available-model list, protecting
// mapped-model candidate filtering.
func TestGatewayService_SelectProviderForModelWithPlatform_GeminiAPIKeyModelMappingFilter(t *testing.T) {
	ctx := context.Background()

	repo := &mockProviderRepoForPlatform{
		providers: []gatewayprovider.ExecutionProvider{
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 1,
					Platform:    capability.PlatformGemini,
					Type:        capability.ProviderTypeAPIKey,
					Priority:    1,
					Status:      billing.StatusActive,
					Schedulable: true,
					Credentials: map[string]any{"model_mapping": map[string]any{"gemini-2.5-pro": "gemini-2.5-pro"}},
				},
			},
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 2,
					Platform:    capability.PlatformGemini,
					Type:        capability.ProviderTypeAPIKey,
					Priority:    2,
					Status:      billing.StatusActive,
					Schedulable: true,
					Credentials: map[string]any{"model_mapping": map[string]any{"gemini-2.5-flash": "gemini-2.5-flash"}},
				},
			},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForPlatform{}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "", "gemini-2.5-flash", nil, capability.PlatformGemini)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(2), acc.Record.ID, "应过滤不支持请求模型的 APIKey 提供商")

	acc, err = svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "", "gemini-3-pro-preview", nil, capability.PlatformGemini)
	require.Error(t, err)
	require.Nil(t, acc)
	var modelErr *routing.GroupModelUnsupportedError
	require.True(t, errors.As(err, &modelErr))
	require.Equal(t, capability.PlatformGemini, modelErr.Platform)
	require.Equal(t, "gemini-3-pro-preview", modelErr.RequestedModel)
	require.Equal(t, []string{"gemini-2.5-flash", "gemini-2.5-pro"}, modelErr.AvailableModels)
	require.Contains(t, err.Error(), `The current group does not support the requested model "gemini-3-pro-preview"`)
	require.Contains(t, err.Error(), "Available models: gemini-2.5-flash, gemini-2.5-pro")
}

// TestGatewayService_SelectProviderForModelWithPlatform_StickyInGroup binds a
// provider with the requested stored membership. The returned ID matches the
// bound member, which also wins priority order in this eligible fixture pool.
func TestGatewayService_SelectProviderForModelWithPlatform_StickyInGroup(t *testing.T) {
	ctx := context.Background()
	groupID := int64(50)

	repo := &mockProviderRepoForPlatform{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true, ProviderGroups: []providercore.GroupMembership{{GroupID: groupID}}}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAnthropic, Priority: 2, Status: billing.StatusActive, Schedulable: true, ProviderGroups: []providercore.GroupMembership{{GroupID: groupID}}}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForPlatform{
		sessionBindings: map[string]int64{"session-group": 1},
	}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, &groupID, "session-group", "", nil, capability.PlatformAnthropic)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(1), acc.Record.ID)
}

// TestGatewayService_SelectProviderForModelWithPlatform_StickyModelMismatchFallback
// binds a provider whose configured scope lacks the requested model. Selection
// chooses the supporting backup, protecting request-model eligibility before
// sticky reuse.
func TestGatewayService_SelectProviderForModelWithPlatform_StickyModelMismatchFallback(t *testing.T) {
	ctx := context.Background()

	repo := &mockProviderRepoForPlatform{
		providers: []gatewayprovider.ExecutionProvider{
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 1,
					Platform:    capability.PlatformAnthropic,
					Priority:    1,
					Status:      billing.StatusActive,
					Schedulable: true,
					Credentials: map[string]any{"model_mapping": map[string]any{"claude-3-5-haiku-20241022": "claude-3-5-haiku-20241022"}},
				},
			},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAnthropic, Priority: 2, Status: billing.StatusActive, Schedulable: true}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForPlatform{
		sessionBindings: map[string]int64{"session-miss": 1},
	}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "session-miss", "claude-3-5-sonnet-20241022", nil, capability.PlatformAnthropic)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(2), acc.Record.ID)
}

// TestGatewayService_SelectProviderForModelWithPlatform_PreferNeverUsed
// supplies equal-priority targets with present and missing use timestamps. The
// never-used target wins, protecting the nil-recency preference of basic
// ordering.
func TestGatewayService_SelectProviderForModelWithPlatform_PreferNeverUsed(t *testing.T) {
	ctx := context.Background()
	lastUsed := time.Now().Add(-1 * time.Hour)

	repo := &mockProviderRepoForPlatform{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true, LastUsedAt: &lastUsed}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAnthropic, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForPlatform{}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "", "claude-3-5-sonnet-20241022", nil, capability.PlatformAnthropic)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(2), acc.Record.ID)
}

// TestGatewayService_SelectProviderForModelWithPlatform_NoProviders requests an
// empty model from an empty forced-platform pool. Selection still returns
// ErrNoAvailableProviders and no target, protecting exhaustion when model
// filtering is skipped.
func TestGatewayService_SelectProviderForModelWithPlatform_NoProviders(t *testing.T) {
	ctx := context.Background()
	repo := &mockProviderRepoForPlatform{
		providers:     []gatewayprovider.ExecutionProvider{},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}

	cache := &mockGatewayCacheForPlatform{}

	svc := newGenericSelectionForTest(GenericDependencies{Reads: Reads{Providers: repo}, Shared: Shared{Cache: cache}}, testConfig())

	acc, err := svc.selectProviderForModelWithPlatform(ctx, selectionFixtureGroupID(ctx), "", "", nil, capability.PlatformAnthropic)
	require.Error(t, err)
	require.Nil(t, acc)
	require.ErrorIs(t, err, schedulercore.ErrNoAvailableProviders)
}

// TestLegacySchedulers_FilterUpstreamRestrictedProvidersInEveryShortcut
// supplies allowed and blocked provider mappings under sticky and
// model-preferred shortcuts. Both forced-platform and mixed-pool paths choose
// the allowed final identity, protecting upstream group restriction across
// those routes.
func TestLegacySchedulers_FilterUpstreamRestrictedProvidersInEveryShortcut(t *testing.T) {
	testCases := []struct {
		name                string // named shortcut scenario
		mixed               bool   // mixed-pool selection path flag
		modelRoutingEnabled bool   // preferred-provider routing switch
		sessionHash         string // fixture sticky-session hash
	}{
		{name: "单平台普通粘性", sessionHash: "sticky"},
		{name: "单平台路由粘性", modelRoutingEnabled: true, sessionHash: "sticky"},
		{name: "单平台路由候选", modelRoutingEnabled: true},
		{name: "混合调度普通粘性", mixed: true, sessionHash: "sticky"},
		{name: "混合调度路由粘性", mixed: true, modelRoutingEnabled: true, sessionHash: "sticky"},
		{name: "混合调度路由候选", mixed: true, modelRoutingEnabled: true},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			groupID := int64(4211)
			pricingConfig := routingtestkit.Configuration{
				ID:                 77,
				Status:             billing.StatusActive,
				RestrictModels:     true,
				BillingModelSource: routing.BillingModelSourceUpstream,
				ModelMapping:       map[string]string{"client-alias": "group-model"},
				ModelPricing: []routing.ModelPricingEntry{{
					Models: []string{"allowed-upstream"},
				}},
			}
			providers := []gatewayprovider.ExecutionProvider{
				{
					Record: providercore.Record{
						LoadLocation: time.LoadLocation, ID: 1,
						Platform:    capability.PlatformAnthropic,
						Priority:    1,
						Status:      billing.StatusActive,
						Schedulable: true,
						Concurrency: 5,
						ProviderGroups: []providercore.GroupMembership{{
							ProviderID: 1,
							GroupID:    groupID,
						}},
						Credentials: map[string]any{"model_mapping": map[string]any{"group-model": "blocked-upstream"}},
					},
				},
				{
					Record: providercore.Record{
						LoadLocation: time.LoadLocation, ID: 2,
						Platform:    capability.PlatformAnthropic,
						Priority:    2,
						Status:      billing.StatusActive,
						Schedulable: true,
						Concurrency: 5,
						ProviderGroups: []providercore.GroupMembership{{
							ProviderID: 2,
							GroupID:    groupID,
						}},
						Credentials: map[string]any{"model_mapping": map[string]any{"group-model": "allowed-upstream"}},
					},
				},
			}
			providerRepo := &mockProviderRepoForPlatform{providers: providers, providersByID: map[int64]*gatewayprovider.ExecutionProvider{}}
			for i := range providerRepo.providers {
				providerRepo.providersByID[providerRepo.providers[i].Record.ID] = &providerRepo.providers[i]
			}
			group := &routing.Group{
				ID: groupID,

				Status:              billing.StatusActive,
				Hydrated:            true,
				ModelRoutingEnabled: tt.modelRoutingEnabled,
			}
			if tt.modelRoutingEnabled {
				group.ModelRouting = map[string][]int64{"group-model": {1, 2}}
			}
			cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sticky": 1}}
			svc := newGenericSelectionForTest(GenericDependencies{
				Reads: Reads{
					Providers: providerRepo,

					Groups: &mockGroupRepoForGateway{groups: map[int64]*routing.Group{groupID: group}},
				},
				Shared: Shared{
					GroupPolicies: routingtestkit.PricingConfig(groupID,
						capability.PlatformAnthropic, pricingConfig),
					Cache: cache,
				},
			}, testConfig())

			ctx := svc.withGroupContext(context.Background(), group)

			var (
				selected *gatewayprovider.ExecutionProvider
				err      error
			)
			if tt.mixed {
				selected, err = svc.selectProviderWithMixedScheduling(ctx, &groupID, tt.sessionHash, "client-alias", nil, capability.PlatformAnthropic)
			} else {
				selected, err = svc.selectProviderForModelWithPlatform(ctx, &groupID, tt.sessionHash, "client-alias", nil, capability.PlatformAnthropic)
			}

			require.NoError(t, err)
			require.NotNil(t, selected)
			require.Equal(t, int64(2), selected.Record.ID)
		})
	}
}

// selectProviderForModelWithPlatform invokes the shared generic core with a
// forced platform in tests. It restores the execution target from this
// invocation's lookup and propagates the slot-free selection error.
func (s *Generic) selectProviderForModelWithPlatform(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, platform string) (*gatewayprovider.ExecutionProvider, error) {
	core, scope := s.genericSelector()
	selected, err := core.SelectPlatform(ctx, groupID, sessionHash, requestedModel, excludedIDs, platform)
	return scope.oldProvider(selected), err
}

// selectProviderWithMixedScheduling invokes the generic core with an empty
// platform filter in tests. The nativePlatform compatibility input is unused,
// allowing eligible members from all group platforms to participate.
func (s *Generic) selectProviderWithMixedScheduling(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, nativePlatform string) (*gatewayprovider.ExecutionProvider, error) {
	core, scope := s.genericSelector()
	selected, err := core.SelectPlatform(ctx, groupID, sessionHash, requestedModel, excludedIDs, "")
	return scope.oldProvider(selected), err
}

// loads converts fixture provider/load pairs through this invocation's target
// lookup. Nil input remains nil; load pointers stay shared and each provider
// receives the same association used when the core returns its selection.
func (g *projectionScope) loads(values []providerWithLoad) []schedulercore.FlowLoad {
	if values == nil {
		return nil
	}
	out := make([]schedulercore.FlowLoad, len(values))
	for i, a := range values {
		out[i] = schedulercore.FlowLoad{Provider: g.provider(a.provider), LoadInfo: a.loadInfo}
	}
	return out
}
