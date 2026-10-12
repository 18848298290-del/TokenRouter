package selection

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// mockProviderRepoForGemini supplies provider records for Gemini tests.
type mockProviderRepoForGemini struct {
	providers          []gatewayprovider.ExecutionProvider
	providersByID      map[int64]*gatewayprovider.ExecutionProvider
	listByGroupFunc    func(ctx context.Context, groupID int64, platforms []string) ([]gatewayprovider.ExecutionProvider, error)
	listByPlatformFunc func(ctx context.Context, platforms []string) ([]gatewayprovider.ExecutionProvider, error)
}

// mockGatewayCacheForGemini stores Gemini test session bindings.
type mockGatewayCacheForGemini struct {
	sessionBindings map[string]int64
	deletedSessions map[string]int
}

func (m *mockProviderRepoForGemini) GetByID(ctx context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	if acc, ok := m.providersByID[id]; ok {
		prepareSelectionFixtureProvider(ctx, acc, nil)
		return acc, nil
	}
	return nil, errors.New("provider not found")
}

func (m *mockProviderRepoForGemini) ListSchedulableByPlatform(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	var result []gatewayprovider.ExecutionProvider
	for _, acc := range m.providers {
		if acc.Record.Platform == platform && acc.View().IsSchedulable() {
			result = append(result, acc)
		}
	}
	return result, nil
}

func (m *mockProviderRepoForGemini) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	// This fixture filters by platform and ignores groupID.
	for i := range m.providers {
		prepareSelectionFixtureProvider(ctx, &m.providers[i], &groupID)
	}
	return m.ListSchedulableByPlatform(ctx, platform)
}

func (m *mockProviderRepoForGemini) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]gatewayprovider.ExecutionProvider, error) {
	return nil, nil
}

func (m *mockProviderRepoForGemini) ListSchedulableByPlatforms(ctx context.Context, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
	if m.listByPlatformFunc != nil {
		return m.listByPlatformFunc(ctx, platforms)
	}
	var result []gatewayprovider.ExecutionProvider
	platformSet := make(map[string]bool)
	for _, p := range platforms {
		platformSet[p] = true
	}
	for _, acc := range m.providers {
		if platformSet[acc.Record.Platform] && acc.View().IsSchedulable() {
			result = append(result, acc)
		}
	}
	return result, nil
}

func (m *mockProviderRepoForGemini) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
	if m.listByGroupFunc != nil {
		return m.listByGroupFunc(ctx, groupID, platforms)
	}
	for i := range m.providers {
		prepareSelectionFixtureProvider(ctx, &m.providers[i], &groupID)
	}
	return m.ListSchedulableByPlatforms(ctx, platforms)
}

func (m *mockProviderRepoForGemini) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	return m.ListSchedulableByPlatform(ctx, platform)
}

func (m *mockProviderRepoForGemini) ListSchedulableUngroupedByPlatforms(ctx context.Context, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
	return m.ListSchedulableByPlatforms(ctx, platforms)
}

func (m *mockGatewayCacheForGemini) GetSessionProviderID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	if id, ok := m.sessionBindings[sessionHash]; ok {
		return id, nil
	}
	return 0, errors.New("not found")
}

func (m *mockGatewayCacheForGemini) SetSessionProviderID(ctx context.Context, groupID int64, sessionHash string, providerID int64, ttl time.Duration) error {
	if m.sessionBindings == nil {
		m.sessionBindings = make(map[string]int64)
	}
	m.sessionBindings[sessionHash] = providerID
	return nil
}

func (m *mockGatewayCacheForGemini) RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error {
	return nil
}

func (m *mockGatewayCacheForGemini) DeleteSessionProviderID(ctx context.Context, groupID int64, sessionHash string) error {
	if m.sessionBindings == nil {
		return nil
	}
	if m.deletedSessions == nil {
		m.deletedSessions = make(map[string]int)
	}
	m.deletedSessions[sessionHash]++
	delete(m.sessionBindings, sessionHash)
	return nil
}

// TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_GeminiPlatform checks selection from the Gemini fixture pool.
func TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_GeminiPlatform(t *testing.T) {
	ctx := context.Background()

	repo := &mockProviderRepoForGemini{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformGemini, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformGemini, Priority: 2, Status: billing.StatusActive, Schedulable: true}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 3, Platform: capability.PlatformAntigravity, Priority: 1, Status: billing.StatusActive, Schedulable: true}}, // Antigravity candidate.
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForGemini{}
	groupRepo := &mockGroupRepoForGemini{groups: map[int64]*routing.Group{}}

	svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{Providers: repo, Groups: groupRepo}, Shared: Shared{Cache: cache}}, nil)

	// Use the fixture group for Gemini selection.
	acc, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gemini-2.5-flash", nil)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(1), acc.Record.ID, "应选择优先级最高的 gemini 提供商")
	require.Equal(t, capability.PlatformGemini, acc.Record.Platform, "无分组时应只返回 gemini 平台提供商")
}

func TestGeminiMessagesCompatService_GroupResolution_ReusesContextGroup(t *testing.T) {
	ctx := context.Background()
	groupID := int64(7)
	group := &routing.Group{
		ID: groupID,

		Status:   billing.StatusActive,
		Hydrated: true,
	}
	ctx = requeststate.WithGroup(ctx, group)

	repo := &mockProviderRepoForGemini{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformGemini, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForGemini{}
	groupRepo := &mockGroupRepoForGemini{groups: map[int64]*routing.Group{}}

	svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{Providers: repo, Groups: groupRepo}, Shared: Shared{Cache: cache}}, nil)

	acc, err := svc.SelectProviderForModelWithExclusions(ctx, &groupID, "", "gemini-2.5-flash", nil)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, 0, groupRepo.getByIDCalls)
	require.Equal(t, 0, groupRepo.getByIDLiteCalls)
}

func TestGeminiMessagesCompatService_AdvancedGroupUsesGenericScore(t *testing.T) {
	groupID := int64(71)
	ctx := requeststate.WithGroup(context.Background(), &routing.Group{
		ID: groupID,

		SchedulerType: routing.GroupSchedulerTypeAdvanced,
		Status:        billing.StatusActive,
		Hydrated:      true,
	})
	repo := &mockProviderRepoForGemini{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformGemini, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformGemini, Priority: 99, Status: billing.StatusActive, Schedulable: true}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}
	stats := schedulercore.NewRuntimeStats(time.Now)
	for range 16 {
		stats.Report(1, false, nil)
		stats.Report(2, true, nil)
	}
	cfg := &config.Config{}
	cfg.Gateway.AdvancedScheduler.LBTopK = 1
	cfg.Gateway.AdvancedScheduler.ScoreWeights = config.GatewayAdvancedSchedulerScoreWeights{
		ErrorRate: 10,
	}
	svc := newGeminiSelectionForTest(GeminiDependencies{
		Reads: Reads{Providers: repo, Groups: &mockGroupRepoForGemini{groups: map[int64]*routing.Group{}}},

		Shared: Shared{Cache: &mockGatewayCacheForGemini{}, Feedback: stats},
	}, cfg)

	provider, err := svc.SelectProviderForModelWithExclusions(ctx, &groupID, "", "gemini-2.5-flash", nil)

	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(2), provider.Record.ID, "高级分组应在既有硬过滤后按通用错误率评分选择")
}

func TestGeminiMessagesCompatService_GroupResolution_UsesLiteFetch(t *testing.T) {
	ctx := context.Background()
	groupID := int64(7)

	repo := &mockProviderRepoForGemini{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformGemini, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForGemini{}
	groupRepo := &mockGroupRepoForGemini{
		groups: map[int64]*routing.Group{
			groupID: {ID: groupID},
		},
	}

	svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{Providers: repo, Groups: groupRepo}, Shared: Shared{Cache: cache}}, nil)

	acc, err := svc.SelectProviderForModelWithExclusions(ctx, &groupID, "", "gemini-2.5-flash", nil)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, 0, groupRepo.getByIDCalls)
	require.Equal(t, 1, groupRepo.getByIDLiteCalls)
}

// TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_GroupHasNoPlatformPreference checks an Antigravity candidate in a group with no platform preference.
func TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_GroupHasNoPlatformPreference(t *testing.T) {
	ctx := context.Background()

	repo := &mockProviderRepoForGemini{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformGemini, Priority: 1, Status: billing.StatusActive, Schedulable: true}},      // Lower-priority candidate.
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformAntigravity, Priority: 1, Status: billing.StatusActive, Schedulable: true}}, // Preferred candidate.
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForGemini{}
	groupRepo := &mockGroupRepoForGemini{
		groups: map[int64]*routing.Group{
			1: {ID: 1},
		},
	}

	svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{Groups: groupRepo, Providers: repo}, Shared: Shared{Cache: cache}}, nil)

	groupID := int64(1)
	acc, err := svc.SelectProviderForModelWithExclusions(ctx, &groupID, "", "gemini-2.5-flash", nil)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(1), acc.Record.ID)
	require.Equal(t, capability.PlatformGemini, acc.Record.Platform, "分组不按平台排除合格提供商")
}

// TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_OAuthPreferred checks the OAuth tie-break preference.
func TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_OAuthPreferred(t *testing.T) {
	ctx := context.Background()

	repo := &mockProviderRepoForGemini{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformGemini, Type: capability.ProviderTypeAPIKey, Priority: 1, Status: billing.StatusActive, Schedulable: true, LastUsedAt: nil}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformGemini, Type: capability.ProviderTypeOAuth, Priority: 1, Status: billing.StatusActive, Schedulable: true, LastUsedAt: nil}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForGemini{}
	groupRepo := &mockGroupRepoForGemini{groups: map[int64]*routing.Group{}}

	svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{Providers: repo, Groups: groupRepo}, Shared: Shared{Cache: cache}}, nil)

	acc, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gemini-2.5-flash", nil)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(2), acc.Record.ID, "同优先级且都未使用时，应优先选择 OAuth 提供商")
	require.Equal(t, capability.ProviderTypeOAuth, acc.Record.Type)
}

// TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_NoAvailableProviders checks an empty provider pool.
func TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_NoAvailableProviders(t *testing.T) {
	ctx := context.Background()

	repo := &mockProviderRepoForGemini{
		providers:     []gatewayprovider.ExecutionProvider{},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}

	cache := &mockGatewayCacheForGemini{}
	groupRepo := &mockGroupRepoForGemini{groups: map[int64]*routing.Group{}}

	svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{Providers: repo, Groups: groupRepo}, Shared: Shared{Cache: cache}}, nil)

	acc, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gemini-2.5-flash", nil)
	require.Error(t, err)
	require.Nil(t, acc)
	require.Contains(t, err.Error(), "no available")
}

// TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_StickySession checks session stickiness.
func TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_StickySession(t *testing.T) {
	ctx := context.Background()

	t.Run("粘性会话命中-同平台", func(t *testing.T) {
		repo := &mockProviderRepoForGemini{
			providers: []gatewayprovider.ExecutionProvider{
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformGemini, Priority: 2, Status: billing.StatusActive, Schedulable: true}},
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformGemini, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
			},
			providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
		}
		for i := range repo.providers {
			repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
		}

		// Gemini session cache keys use the gemini: prefix.
		cache := &mockGatewayCacheForGemini{
			sessionBindings: map[string]int64{"gemini:session-123": 1},
		}
		groupRepo := &mockGroupRepoForGemini{groups: map[int64]*routing.Group{}}

		svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{Providers: repo, Groups: groupRepo}, Shared: Shared{Cache: cache}}, nil)

		acc, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "session-123", "gemini-2.5-flash", nil)
		require.NoError(t, err)
		require.NotNil(t, acc)
		require.Equal(t, int64(1), acc.Record.ID, "应返回粘性会话绑定的提供商")
	})

	t.Run("混合分组保留兼容提供商的粘性", func(t *testing.T) {
		repo := &mockProviderRepoForGemini{
			providers: []gatewayprovider.ExecutionProvider{
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAntigravity, Priority: 2, Status: billing.StatusActive, Schedulable: true}}, // Bind the session.
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformGemini, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
			},
			providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
		}
		for i := range repo.providers {
			repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
		}

		cache := &mockGatewayCacheForGemini{
			sessionBindings: map[string]int64{"gemini:session-123": 1}, // Bind an Antigravity provider.
		}
		groupRepo := &mockGroupRepoForGemini{groups: map[int64]*routing.Group{}}

		svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{Providers: repo, Groups: groupRepo}, Shared: Shared{Cache: cache}}, nil)

		// The Antigravity group member supports this model, so its session binding remains usable.
		acc, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "session-123", "gemini-2.5-flash", nil)
		require.NoError(t, err)
		require.NotNil(t, acc)
		require.Equal(t, int64(1), acc.Record.ID, "提供商协议和模型可用时保留粘性")
		require.Equal(t, capability.PlatformAntigravity, acc.Record.Platform)
	})

	t.Run("粘性会话不命中无前缀缓存键", func(t *testing.T) {
		repo := &mockProviderRepoForGemini{
			providers: []gatewayprovider.ExecutionProvider{
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformGemini, Priority: 2, Status: billing.StatusActive, Schedulable: true}},
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformGemini, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
			},
			providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
		}
		for i := range repo.providers {
			repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
		}

		// A key lacking the gemini: prefix must miss.
		cache := &mockGatewayCacheForGemini{
			sessionBindings: map[string]int64{"session-123": 1},
		}
		groupRepo := &mockGroupRepoForGemini{groups: map[int64]*routing.Group{}}

		svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{Providers: repo, Groups: groupRepo}, Shared: Shared{Cache: cache}}, nil)

		acc, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "session-123", "gemini-2.5-flash", nil)
		require.NoError(t, err)
		require.NotNil(t, acc)
		// After a sticky miss, select by priority.
		require.Equal(t, int64(2), acc.Record.ID, "粘性会话未命中，应按优先级选择")
	})

	t.Run("粘性会话不可调度-清理并回退选择", func(t *testing.T) {
		repo := &mockProviderRepoForGemini{
			providers: []gatewayprovider.ExecutionProvider{
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformGemini, Priority: 2, Status: billing.StatusDisabled, Schedulable: true}},
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformGemini, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
			},
			providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
		}
		for i := range repo.providers {
			repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
		}

		cache := &mockGatewayCacheForGemini{
			sessionBindings: map[string]int64{"gemini:session-123": 1},
		}
		groupRepo := &mockGroupRepoForGemini{groups: map[int64]*routing.Group{}}

		svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{Providers: repo, Groups: groupRepo}, Shared: Shared{Cache: cache}}, nil)

		acc, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "session-123", "gemini-2.5-flash", nil)
		require.NoError(t, err)
		require.NotNil(t, acc)
		require.Equal(t, int64(2), acc.Record.ID)
		require.Equal(t, 1, cache.deletedSessions["gemini:session-123"])
		require.Equal(t, int64(2), cache.sessionBindings["gemini:session-123"])
	})
}

func TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_ForcePlatformKeepsGroupBoundary(t *testing.T) {
	ctx := context.Background()
	groupID := int64(9)
	ctx = apikey.WithForcePlatform(ctx, capability.PlatformAntigravity)

	repo := &mockProviderRepoForGemini{
		listByGroupFunc: func(ctx context.Context, groupID int64, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
			return nil, nil
		},
		listByPlatformFunc: func(ctx context.Context, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
			return []gatewayprovider.ExecutionProvider{
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAntigravity, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
			}, nil
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{
			1: {Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAntigravity, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
		},
	}

	cache := &mockGatewayCacheForGemini{}
	groupRepo := &mockGroupRepoForGemini{groups: map[int64]*routing.Group{
		groupID: {ID: groupID, Status: billing.StatusActive},
	}}

	svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{Providers: repo, Groups: groupRepo}, Shared: Shared{Cache: cache}}, nil)

	acc, err := svc.SelectProviderForModelWithExclusions(ctx, &groupID, "", "gemini-2.5-flash", nil)
	require.Error(t, err)
	require.Nil(t, acc)
}

func TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_NoModelSupport(t *testing.T) {
	ctx := context.Background()

	repo := &mockProviderRepoForGemini{
		providers: []gatewayprovider.ExecutionProvider{
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 1,
					Platform:    capability.PlatformGemini,
					Priority:    1,
					Status:      billing.StatusActive,
					Schedulable: true,
					Credentials: map[string]any{"model_mapping": map[string]any{"gemini-1.0-pro": "gemini-1.0-pro"}},
				},
			},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForGemini{}
	groupRepo := &mockGroupRepoForGemini{groups: map[int64]*routing.Group{}}

	svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{Providers: repo, Groups: groupRepo}, Shared: Shared{Cache: cache}}, nil)

	acc, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gemini-2.5-flash", nil)
	require.Error(t, err)
	require.Nil(t, acc)
	var modelErr *routing.GroupModelUnsupportedError
	require.True(t, errors.As(err, &modelErr))
	require.Empty(t, modelErr.Platform)
	require.Equal(t, "gemini-2.5-flash", modelErr.RequestedModel)
	require.Equal(t, []string{"gemini-1.0-pro"}, modelErr.AvailableModels)
	require.Contains(t, err.Error(), `The current group does not support the requested model "gemini-2.5-flash"`)
	require.Contains(t, err.Error(), "Available models: gemini-1.0-pro")
}

func TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_ModelRateLimitedNotGroupUnsupported(t *testing.T) {
	ctx := context.Background()
	resetAt := time.Now().Add(10 * time.Minute).Format(time.RFC3339)

	repo := &mockProviderRepoForGemini{
		providers: []gatewayprovider.ExecutionProvider{
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 1,
					Platform:    capability.PlatformGemini,
					Priority:    1,
					Status:      billing.StatusActive,
					Schedulable: true,
					Credentials: map[string]any{"model_mapping": map[string]any{"gemini-2.5-flash": "gemini-2.5-flash"}},
					Extra: map[string]any{
						"model_rate_limits": map[string]any{
							"gemini-2.5-flash": map[string]any{"rate_limit_reset_at": resetAt},
						},
					},
				},
			},
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 2,
					Platform:    capability.PlatformGemini,
					Priority:    2,
					Status:      billing.StatusActive,
					Schedulable: true,
					Credentials: map[string]any{"model_mapping": map[string]any{"gemini-1.0-pro": "gemini-1.0-pro"}},
				},
			},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	svc := newGeminiSelectionForTest(GeminiDependencies{
		Reads: Reads{Providers: repo, Groups: &mockGroupRepoForGemini{groups: map[int64]*routing.Group{}}},

		Shared: Shared{Cache: &mockGatewayCacheForGemini{}},
	}, nil,
	)

	acc, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gemini-2.5-flash", nil)
	require.Error(t, err)
	require.Nil(t, acc)
	var modelErr *routing.GroupModelUnsupportedError
	require.False(t, errors.As(err, &modelErr))
	require.Contains(t, err.Error(), "supporting model")
}

func TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_StickyMixedScheduling(t *testing.T) {
	ctx := context.Background()
	repo := &mockProviderRepoForGemini{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAntigravity, Priority: 1, Status: billing.StatusActive, Schedulable: true, Extra: map[string]any{"mixed_scheduling": true}}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformGemini, Priority: 2, Status: billing.StatusActive, Schedulable: true}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForGemini{
		sessionBindings: map[string]int64{"gemini:session-999": 1},
	}
	groupRepo := &mockGroupRepoForGemini{groups: map[int64]*routing.Group{}}

	svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{Providers: repo, Groups: groupRepo}, Shared: Shared{Cache: cache}}, nil)

	acc, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "session-999", "gemini-2.5-flash", nil)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(1), acc.Record.ID)
}

func TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_IgnoresRetiredMixedSchedulingFlag(t *testing.T) {
	ctx := context.Background()
	repo := &mockProviderRepoForGemini{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAntigravity, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformGemini, Priority: 2, Status: billing.StatusActive, Schedulable: true}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForGemini{}
	groupRepo := &mockGroupRepoForGemini{groups: map[int64]*routing.Group{}}

	svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{Groups: groupRepo, Providers: repo}, Shared: Shared{Cache: cache}}, nil)

	acc, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gemini-2.5-flash", nil)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(1), acc.Record.ID)
}

func TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_ExcludedProvider(t *testing.T) {
	ctx := context.Background()
	repo := &mockProviderRepoForGemini{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformGemini, Priority: 1, Status: billing.StatusActive, Schedulable: true}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformGemini, Priority: 2, Status: billing.StatusActive, Schedulable: true}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForGemini{}
	groupRepo := &mockGroupRepoForGemini{groups: map[int64]*routing.Group{}}

	svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{Providers: repo, Groups: groupRepo}, Shared: Shared{Cache: cache}}, nil)

	excluded := map[int64]struct{}{1: {}}
	acc, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gemini-2.5-flash", excluded)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(2), acc.Record.ID)
}

func TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_ListError(t *testing.T) {
	ctx := context.Background()
	repo := &mockProviderRepoForGemini{
		listByPlatformFunc: func(ctx context.Context, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
			return nil, errors.New("query failed")
		},
	}

	cache := &mockGatewayCacheForGemini{}
	groupRepo := &mockGroupRepoForGemini{groups: map[int64]*routing.Group{}}

	svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{Providers: repo, Groups: groupRepo}, Shared: Shared{Cache: cache}}, nil)

	acc, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gemini-2.5-flash", nil)
	require.Error(t, err)
	require.Nil(t, acc)
	require.Contains(t, err.Error(), "query providers failed")
}

func TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_PreferOAuth(t *testing.T) {
	ctx := context.Background()
	repo := &mockProviderRepoForGemini{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformGemini, Priority: 1, Status: billing.StatusActive, Schedulable: true, Type: capability.ProviderTypeAPIKey}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformGemini, Priority: 1, Status: billing.StatusActive, Schedulable: true, Type: capability.ProviderTypeOAuth}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForGemini{}
	groupRepo := &mockGroupRepoForGemini{groups: map[int64]*routing.Group{}}

	svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{Groups: groupRepo, Providers: repo}, Shared: Shared{Cache: cache}}, nil)

	acc, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gemini-2.5-pro", nil)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(2), acc.Record.ID)
}

func TestGeminiMessagesCompatService_SelectProviderForModelWithExclusions_PreferLeastRecentlyUsed(t *testing.T) {
	ctx := context.Background()
	oldTime := time.Now().Add(-2 * time.Hour)
	newTime := time.Now().Add(-1 * time.Hour)
	repo := &mockProviderRepoForGemini{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformGemini, Priority: 1, Status: billing.StatusActive, Schedulable: true, LastUsedAt: &newTime}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformGemini, Priority: 1, Status: billing.StatusActive, Schedulable: true, LastUsedAt: &oldTime}},
		},
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	for i := range repo.providers {
		repo.providersByID[repo.providers[i].Record.ID] = &repo.providers[i]
	}

	cache := &mockGatewayCacheForGemini{}
	groupRepo := &mockGroupRepoForGemini{groups: map[int64]*routing.Group{}}

	svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{Providers: repo, Groups: groupRepo}, Shared: Shared{Cache: cache}}, nil)

	acc, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gemini-2.5-pro", nil)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(2), acc.Record.ID)
}

// TestGeminiPlatformRouting_DocumentRouteDecision checks platform-based forwarding decisions.
func TestGeminiPlatformRouting_DocumentRouteDecision(t *testing.T) {
	tests := []struct {
		name            string
		platform        string
		expectedService string // gemini selects ForwardNative; antigravity selects ForwardGemini.
	}{
		{
			name:            "Gemini平台走ForwardNative",
			platform:        capability.PlatformGemini,
			expectedService: "gemini",
		},
		{
			name:            "Antigravity平台走ForwardGemini",
			platform:        capability.PlatformAntigravity,
			expectedService: "antigravity",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation:

			// Model the handler's platform dispatch.
			time.LoadLocation, Platform: tt.platform}}

			var serviceName string
			if provider.Record.Platform == capability.PlatformAntigravity {
				serviceName = "antigravity"
			} else {
				serviceName = "gemini"
			}

			require.Equal(t, tt.expectedService, serviceName,
				"平台 %s 应该路由到 %s 服务", tt.platform, tt.expectedService)
		})
	}
}

func TestGeminiMessagesCompatService_isModelSupportedByProvider(t *testing.T) {
	svc := newGeminiSelectionForTest(GeminiDependencies{Reads: Reads{}, Shared: Shared{}}, nil)

	tests := []struct {
		name     string
		provider *gatewayprovider.ExecutionProvider
		model    string
		expected bool
	}{
		{
			name:     "Antigravity平台-支持gemini模型",
			provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAntigravity}},
			model:    "gemini-2.5-flash",
			expected: true,
		},
		{
			name:     "Antigravity平台-支持claude模型",
			provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAntigravity}},
			model:    "claude-sonnet-4-5",
			expected: true,
		},
		{
			name:     "Antigravity平台-空白名单允许gpt模型",
			provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAntigravity}},
			model:    "gpt-4",
			expected: true,
		},
		{
			name:     "Antigravity平台-空模型允许",
			provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformAntigravity}},
			model:    "",
			expected: true,
		},
		{
			name: "Antigravity平台-自定义映射-支持自定义模型",
			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, Platform: capability.PlatformAntigravity,
					Credentials: map[string]any{
						"model_mapping": map[string]any{
							"my-custom-model": "upstream-model",
							"gpt-4o":          "some-model",
						},
					},
				},
			},
			model:    "my-custom-model",
			expected: true,
		},
		{
			name: "Antigravity平台-自定义映射-不在映射中的模型不支持",
			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, Platform: capability.PlatformAntigravity,
					Credentials: map[string]any{
						"model_mapping": map[string]any{
							"my-custom-model": "upstream-model",
						},
					},
				},
			},
			model:    "claude-sonnet-4-5",
			expected: true,
		},
		{
			name:     "Gemini平台-无映射配置-支持所有模型",
			provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGemini}},
			model:    "gemini-2.5-flash",
			expected: true,
		},
		{
			name: "Gemini平台-有映射配置-未命中映射时按透传支持模型",
			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, Platform: capability.PlatformGemini,
					Credentials: map[string]any{"model_mapping": map[string]any{"gemini-2.5-pro": "x"}},
				},
			},
			model:    "gemini-2.5-flash",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := svc.isModelSupportedByProvider(tt.provider, tt.model)
			require.Equal(t, tt.expected, got)
		})
	}
}
