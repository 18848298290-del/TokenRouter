package selection

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache/codec"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

var _ Groups = (*mockGroupRepoForGemini)(nil)

type grokFreeQuotaUsageRepoStub struct {
	usage.UsageLogRepository

	mu      sync.Mutex
	stats   map[int64]*usage.ProviderStats
	err     error
	calls   int
	lastIDs []int64
	start   time.Time
}

// mockProviderRepoForPlatform supplies records for platform selection tests.
type mockProviderRepoForPlatform struct {
	providers        []gatewayprovider.ExecutionProvider
	providersByID    map[int64]*gatewayprovider.ExecutionProvider
	listPlatformFunc func(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error)
	getByIDCalls     int
}

// mockGatewayCacheForPlatform stores platform test session bindings and deletions.
type mockGatewayCacheForPlatform struct {
	sessionBindings map[string]int64
	deletedSessions map[string]int
}

type mockGroupRepoForGateway struct {
	groups           map[int64]*routing.Group
	getByIDCalls     int
	getByIDLiteCalls int
}

type mockConcurrencyCache struct {
	acquireProviderCalls int
	loadBatchCalls       int
	acquireResults       map[int64]bool
	loadBatchErr         error
	loadMap              map[int64]*schedulercore.ProviderLoadInfo
	waitCounts           map[int64]int
	skipDefaultLoad      bool
}

// mockGroupRepoForGemini supplies groups and counts full and lightweight reads.
type mockGroupRepoForGemini struct {
	groups           map[int64]*routing.Group
	getByIDCalls     int
	getByIDLiteCalls int
}

type selectionFixtureGroups struct{}

type mixedGroupProviders struct {
	Providers
	values       []gatewayprovider.ExecutionProvider
	groupQueries []int64
}

type openAISnapshotCacheStub struct {
	schedulercore.SnapshotCache
	snapshotProviders []*gatewayprovider.ExecutionProvider
	providersByID     map[int64]*gatewayprovider.ExecutionProvider
}

type schedulerTestOpenAIProviderRepo struct {
	gatewayprovider.ExecutionProviderStore

	providers []gatewayprovider.
			ExecutionProvider
}

type schedulerGroupAwareOpenAIProviderRepo struct {
	schedulerTestOpenAIProviderRepo
}

type schedulerTestConcurrencyCache struct {
	schedulercore.ConcurrencyCache
	loadBatchErr    error
	loadMap         map[int64]*schedulercore.ProviderLoadInfo
	acquireResults  map[int64]bool
	waitCounts      map[int64]int
	skipDefaultLoad bool
	acquiredIDs     *[]int64
	releasedIDs     *[]int64
}

type schedulerTestGatewayCache struct {
	sessionBindings map[string]int64
	deletedSessions map[string]int
}

type advancedSchedulerSettingRepoStub struct {
	values map[string]string
}

type thresholdSelectionProviderRepoStub struct {
	gatewaytestkit.HealthStoreRecorder

	providers []gatewayprovider.ExecutionProvider
}

// selectionProviderFixture implements the reads used by selection tests; unexpected embedded-interface calls fail.
type selectionProviderFixture struct {
	Providers
	providers []gatewayprovider.ExecutionProvider
}

type responseCacheFixture struct {
	stickyCacheFixture
	session.GatewayCache
}

type selectionConcurrencyFixture struct {
	schedulercore.ConcurrencyCache
	acquireResults  map[int64]bool
	waitCounts      map[int64]int
	loadBatchErr    error
	loadMap         map[int64]*schedulercore.ProviderLoadInfo
	skipDefaultLoad bool
}

// hydrationProviderSource passes source read errors to provider hydration.
type hydrationProviderSource struct {
	schedulercore.SnapshotProviderSource
	source Providers
}

// snapshotHydrationCache supplies lightweight and full records to SnapshotService.
type snapshotHydrationCache struct {
	schedulercore.SnapshotCache
	snapshot  []*gatewayprovider.ExecutionProvider
	providers map[int64]*gatewayprovider.ExecutionProvider
}

// stickyCacheFixture implements sticky lookups, writes, and cache misses for tests.
type stickyCacheFixture struct {
	sessionBindings map[string]int64
	deletedSessions map[string]int
}

func (m *mockProviderRepoForPlatform) availabilityRecords(_ context.Context, groupID *int64, platforms []string, includeGrouped bool) ([]gatewayprovider.ExecutionProvider, error) {
	platformSet := make(map[string]struct{}, len(platforms))
	for _, platform := range platforms {
		platformSet[platform] = struct{}{}
	}
	result := make([]gatewayprovider.ExecutionProvider, 0, len(m.providers))
	for _, acc := range m.providers {
		if _, ok := platformSet[acc.Record.Platform]; !ok || acc.Record.Status != billing.StatusActive || !acc.Record.Schedulable {
			continue
		}
		if groupID != nil {
			inGroup := false
			for _, providerGroup := range acc.Record.ProviderGroups {
				if providerGroup.GroupID == *groupID {
					inGroup = true
					break
				}
			}
			if !inGroup {
				continue
			}
		} else if !includeGrouped && (len(acc.Record.ProviderGroups) > 0 || len(acc.Record.GroupIDs) > 0) {
			continue
		}
		result = append(result, acc)
	}
	return result, nil
}

// isProviderRequestCompatible reduces the compatibility result to a boolean for assertions.
func (s *compatiblePicker) isProviderRequestCompatible(ctx context.Context, provider *gatewayprovider.ExecutionProvider, req schedulercore.PlatformSelectionInput) bool {
	compatible, _ := s.isProviderRequestCompatibleReason(ctx, provider, req)
	return compatible
}

func diagnosticParameterDefaults(cfg *config.Config) schedulercore.ParameterDefaults {
	defaults := schedulercore.DefaultParameters()
	if cfg == nil {
		return defaults
	}
	value := cfg.Gateway.AdvancedScheduler
	if value.LBTopK > 0 {
		defaults.TopK = value.LBTopK
	}
	weights := value.ScoreWeights
	defaults.Weights = policy.ScoreWeights{Priority: weights.Priority, Load: weights.Load, Queue: weights.Queue, ErrorRate: weights.ErrorRate, TTFT: weights.TTFT, Reset: weights.Reset, QuotaHeadroom: weights.QuotaHeadroom, Previous: weights.PreviousResponse, SessionSticky: weights.SessionSticky}
	defaults.Runtime.EwmaErrorRateAlpha = value.EWMAErrorRateAlpha
	defaults.Runtime.EwmaTTFTAlpha = value.EWMATTFTAlpha
	defaults.Runtime.StickyEscape = policy.NormalizeStickyEscape(policy.StickyEscapeConfig{Enabled: value.StickyEscapeEnabled, TtftMs: float64(value.StickyEscapeTTFTMs), ErrorRate: value.StickyEscapeErrorRate})
	return defaults
}

func (r *grokFreeQuotaUsageRepoStub) GetProviderWindowStatsBatch(_ context.Context, providerIDs []int64, start time.Time) (map[int64]*usage.ProviderStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.lastIDs = append([]int64(nil), providerIDs...)
	r.start = start
	if r.err != nil {
		return nil, r.err
	}
	result := make(map[int64]*usage.ProviderStats, len(providerIDs))
	for _, providerID := range providerIDs {
		if stats := r.stats[providerID]; stats != nil {
			copyStats := *stats
			result[providerID] = &copyStats
		}
	}
	return result, nil
}

func newGrokFreeQuotaTestGate(cfg *config.Config, reader usage.UsageLogRepository, background func(string, func()) bool) *providercore.FreeQuotaGate {
	return providercore.NewFreeQuotaGate(func() providercore.FreeQuotaOptions {
		v := cfg.Gateway.Grok
		return providercore.FreeQuotaOptions{Enabled: v.FreeQuotaSoftGateEnabled, TokenLimit: v.FreeQuotaTokenLimit, Percent: v.FreeQuotaSoftGatePercent, WindowHours: v.FreeQuotaWindowHours, CacheSeconds: v.FreeQuotaStatsCacheSeconds}
	}, func(ctx context.Context, ids []int64, start time.Time) (map[int64]int64, error) {
		return usage.ReadProviderTokenWindow(ctx, reader, ids, start)
	}, background, time.Now, nil, nil)
}

func healthyGrokOAuthGatewayTestProvider(id int64, token string) *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: id,
			Name:        "grok",
			Platform:    capability.PlatformGrok,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":  token,
				"refresh_token": "refresh-token",
				"expires_at":    time.Now().Add(2 * providercore.GrokTokenRefreshSkew).UTC().Format(time.RFC3339),
				"base_url":      xai.DefaultCLIBaseURL,
			},
		},
	}
}

// freeQuotaFactoryForTest creates independent gate caches and waits for accepted refresh tasks during cleanup.
func freeQuotaFactoryForTest(t *testing.T, cfg *config.Config, source usage.UsageLogRepository) func() *providercore.FreeQuotaGate {
	var tasks sync.WaitGroup
	t.Cleanup(tasks.Wait)
	background := func(_ string, work func()) bool {
		tasks.Go(func() { ; work() })
		return true
	}
	return func() *providercore.FreeQuotaGate { return newGrokFreeQuotaTestGate(cfg, source, background) }
}

// testConfig returns a zero-valued process configuration for tests.
func testConfig() *config.Config {
	return &config.Config{}
}

func (m *mockProviderRepoForPlatform) GetByID(ctx context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	m.getByIDCalls++
	if acc, ok := m.providersByID[id]; ok {
		prepareSelectionFixtureProvider(ctx, acc, nil)
		return acc, nil
	}
	return nil, errors.New("provider not found")
}

func (m *mockProviderRepoForPlatform) ListSchedulableByPlatform(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	if m.listPlatformFunc != nil {
		return m.listPlatformFunc(ctx, platform)
	}
	var result []gatewayprovider.ExecutionProvider
	for _, acc := range m.providers {
		if acc.Record.Platform == platform && acc.View().IsSchedulable() {
			result = append(result, acc)
		}
	}
	return result, nil
}

func (m *mockProviderRepoForPlatform) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	for i := range m.providers {
		prepareSelectionFixtureProvider(ctx, &m.providers[i], &groupID)
	}
	return m.ListSchedulableByPlatform(ctx, platform)
}

func (m *mockProviderRepoForPlatform) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]gatewayprovider.ExecutionProvider, error) {
	return nil, nil
}

func (m *mockProviderRepoForPlatform) ListSchedulableByPlatforms(ctx context.Context, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
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

func (m *mockProviderRepoForPlatform) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
	for i := range m.providers {
		prepareSelectionFixtureProvider(ctx, &m.providers[i], &groupID)
	}
	return m.ListSchedulableByPlatforms(ctx, platforms)
}

func (m *mockProviderRepoForPlatform) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	return m.ListSchedulableByPlatform(ctx, platform)
}

func (m *mockProviderRepoForPlatform) ListSchedulableUngroupedByPlatforms(ctx context.Context, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
	return m.ListSchedulableByPlatforms(ctx, platforms)
}

func (m *mockGatewayCacheForPlatform) GetSessionProviderID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	if id, ok := m.sessionBindings[sessionHash]; ok {
		return id, nil
	}
	return 0, errors.New("not found")
}

func (m *mockGatewayCacheForPlatform) SetSessionProviderID(ctx context.Context, groupID int64, sessionHash string, providerID int64, ttl time.Duration) error {
	if m.sessionBindings == nil {
		m.sessionBindings = make(map[string]int64)
	}
	m.sessionBindings[sessionHash] = providerID
	return nil
}

func (m *mockGatewayCacheForPlatform) RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error {
	return nil
}

func (m *mockGatewayCacheForPlatform) DeleteSessionProviderID(ctx context.Context, groupID int64, sessionHash string) error {
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

func (m *mockGroupRepoForGateway) GetByID(ctx context.Context, id int64) (*routing.Group, error) {
	m.getByIDCalls++
	if g, ok := m.groups[id]; ok {
		return g, nil
	}
	return nil, routing.ErrGroupNotFound
}

func (m *mockGroupRepoForGateway) GetByIDLite(ctx context.Context, id int64) (*routing.Group, error) {
	m.getByIDLiteCalls++
	if g, ok := m.groups[id]; ok {
		return g, nil
	}
	return nil, routing.ErrGroupNotFound
}

func (m *mockConcurrencyCache) AcquireProviderSlot(ctx context.Context, providerID int64, maxConcurrency int, requestID string) (bool, error) {
	m.acquireProviderCalls++
	if m.acquireResults != nil {
		if result, ok := m.acquireResults[providerID]; ok {
			return result, nil
		}
	}
	return true, nil
}

func (m *mockConcurrencyCache) ReleaseProviderSlot(ctx context.Context, providerID int64, requestID string) error {
	return nil
}

func (m *mockConcurrencyCache) GetProviderConcurrency(ctx context.Context, providerID int64) (int, error) {
	return 0, nil
}

func (m *mockConcurrencyCache) GetProviderConcurrencyBatch(ctx context.Context, providerIDs []int64) (map[int64]int, error) {
	result := make(map[int64]int, len(providerIDs))
	for _, providerID := range providerIDs {
		result[providerID] = 0
	}
	return result, nil
}

func (m *mockConcurrencyCache) IncrementProviderWaitCount(ctx context.Context, providerID int64, maxWait int) (bool, error) {
	return true, nil
}

func (m *mockConcurrencyCache) DecrementProviderWaitCount(ctx context.Context, providerID int64) error {
	return nil
}

func (m *mockConcurrencyCache) GetProviderWaitingCount(ctx context.Context, providerID int64) (int, error) {
	if m.waitCounts != nil {
		if count, ok := m.waitCounts[providerID]; ok {
			return count, nil
		}
	}
	return 0, nil
}

func (m *mockConcurrencyCache) AcquireUserSlot(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
	return true, nil
}

func (m *mockConcurrencyCache) ReleaseUserSlot(ctx context.Context, userID int64, requestID string) error {
	return nil
}

func (m *mockConcurrencyCache) GetUserConcurrency(ctx context.Context, userID int64) (int, error) {
	return 0, nil
}

func (m *mockConcurrencyCache) IncrementWaitCount(ctx context.Context, userID int64, maxWait int) (bool, error) {
	return true, nil
}

func (m *mockConcurrencyCache) DecrementWaitCount(ctx context.Context, userID int64) error {
	return nil
}

func (m *mockConcurrencyCache) GetProvidersLoadBatch(ctx context.Context, providers []schedulercore.ProviderWithConcurrency) (map[int64]*schedulercore.ProviderLoadInfo, error) {
	m.loadBatchCalls++
	if m.loadBatchErr != nil {
		return nil, m.loadBatchErr
	}
	result := make(map[int64]*schedulercore.ProviderLoadInfo, len(providers))
	if m.skipDefaultLoad && m.loadMap != nil {
		for _, acc := range providers {
			if load, ok := m.loadMap[acc.ID]; ok {
				result[acc.ID] = load
			}
		}
		return result, nil
	}
	for _, acc := range providers {
		if m.loadMap != nil {
			if load, ok := m.loadMap[acc.ID]; ok {
				result[acc.ID] = load
				continue
			}
		}
		result[acc.ID] = &schedulercore.ProviderLoadInfo{
			ProviderID:         acc.ID,
			CurrentConcurrency: 0,
			WaitingCount:       0,
			LoadRate:           0,
		}
	}
	return result, nil
}

func (m *mockConcurrencyCache) CleanupExpiredProviderSlots(ctx context.Context, providerID int64) error {
	return nil
}

func (m *mockConcurrencyCache) CleanupExpiredProviderSlotKeys(ctx context.Context) error {
	return nil
}

func (m *mockConcurrencyCache) CleanupStaleProcessSlots(ctx context.Context, activeRequestPrefix string) error {
	return nil
}

func (m *mockConcurrencyCache) GetUsersLoadBatch(ctx context.Context, users []schedulercore.UserWithConcurrency) (map[int64]*schedulercore.UserLoadInfo, error) {
	result := make(map[int64]*schedulercore.UserLoadInfo, len(users))
	for _, user := range users {
		result[user.ID] = &schedulercore.UserLoadInfo{
			UserID:             user.ID,
			CurrentConcurrency: 0,
			WaitingCount:       0,
			LoadRate:           0,
		}
	}
	return result, nil
}

func (m *mockGroupRepoForGemini) GetByID(ctx context.Context, id int64) (*routing.Group, error) {
	m.getByIDCalls++
	if g, ok := m.groups[id]; ok {
		return g, nil
	}
	if id == 1 {
		return &routing.Group{ID: id, Hydrated: true, Status: routing.StatusActive}, nil
	}
	return nil, errors.New("group not found")
}

func (m *mockGroupRepoForGemini) GetByIDLite(ctx context.Context, id int64) (*routing.Group, error) {
	m.getByIDLiteCalls++
	if g, ok := m.groups[id]; ok {
		return g, nil
	}
	if id == 1 {
		return &routing.Group{ID: id, Hydrated: true, Status: routing.StatusActive}, nil
	}
	return nil, errors.New("group not found")
}

// newGenericSelectionForTest creates a generic selector with fixture dependencies and configuration.
func newGenericSelectionForTest(deps GenericDependencies, cfg *config.Config) *Generic {
	if deps.Groups == nil {
		deps.Groups = selectionFixtureGroups{}
	}
	if deps.Parameters == nil {
		deps.Parameters = schedulercore.NewParameters(schedulercore.NewSettingsRuntime(schedulercore.Diagnostics{}), nil, diagnosticParameterDefaults(cfg))
	}
	return NewGeneric(deps, selectionOptionsForTest(cfg))
}

func selectionOptionsForTest(cfg *config.Config) Options {
	options := DefaultOptions()
	if cfg == nil {
		return options
	}

	value := cfg.Gateway.Scheduling
	options.Scheduling = schedulercore.FlowOptions{LoadBatchEnabled: value.LoadBatchEnabled, PreferSoonestReset: value.PreferSoonestReset, FallbackMaxWaiting: value.FallbackMaxWaiting, StickySessionMaxWaiting: value.StickySessionMaxWaiting, FallbackSelectionMode: value.FallbackSelectionMode, FallbackWaitTimeout: value.FallbackWaitTimeout, StickySessionWaitTimeout: value.StickySessionWaitTimeout}
	ws := cfg.Gateway.OpenAIWS
	options.ReadLegacySticky = ws.SessionHashReadOldFallback
	options.WriteLegacySticky = ws.SessionHashDualWriteOld
	if ws.StickySessionTTLSeconds > 0 {
		options.StickyTTL = time.Duration(ws.StickySessionTTLSeconds) * time.Second
	}
	if ws.StickyResponseIDTTLSeconds > 0 {
		options.ResponseTTL = time.Duration(ws.StickyResponseIDTTLSeconds) * time.Second
	}
	return options
}

// newGeminiSelectionForTest creates a Gemini selector with fixture dependencies.
func newGeminiSelectionForTest(deps GeminiDependencies, cfg *config.Config) *Gemini {
	if deps.Groups == nil {
		deps.Groups = selectionFixtureGroups{}
	}
	if deps.Parameters == nil {
		deps.Parameters = schedulercore.NewParameters(schedulercore.NewSettingsRuntime(schedulercore.Diagnostics{}), nil, diagnosticParameterDefaults(cfg))
	}
	return NewGemini(deps, selectionOptionsForTest(cfg))
}

// newCompatibleSelectionForTest supplies fixture parameters for compatible selection.
func newCompatibleSelectionForTest(deps CompatibleDependencies, cfg *config.Config) *Compatible {
	if deps.Groups == nil {
		deps.Groups = selectionFixtureGroups{}
	}
	// Create one response-ownership store for this selector when none is supplied.
	if deps.Responses == nil {
		cache, _ := deps.Cache.(session.GatewayCache)
		deps.Responses = session.NewOpenAIWSStateStore(cache, gatewayprovider.LogOpenAIWSModeInfo)
	}
	if deps.Parameters == nil {
		deps.Parameters = schedulercore.NewParameters(schedulercore.NewSettingsRuntime(schedulercore.Diagnostics{}), nil, diagnosticParameterDefaults(cfg))
	}
	return NewCompatible(deps, selectionOptionsForTest(cfg))
}

// selectionFixtureGroupID returns the request's group ID or the selection fixture's default.
func selectionFixtureGroupID(ctx context.Context) *int64 {
	if group, ok := requeststate.GroupFromContext(ctx); ok && group != nil {
		return &group.ID
	}
	if policy, ok := ctx.Value(candidatePolicyKey{}).(candidatePolicy); ok && policy.groupID != nil {
		return policy.groupID
	}
	if input, ok := ctx.Value(selectionRequestKey{}).(selectionRequest); ok && input.groupID != nil {
		return input.groupID
	}
	id := int64(1)
	return &id
}

// prepareSelectionFixtureProvider fills provider defaults, group membership, and model scope for selection tests.
func prepareSelectionFixtureProvider(ctx context.Context, value *gatewayprovider.ExecutionProvider, groupID *int64) {
	if value == nil {
		return
	}
	if value.Record.Type == "" {
		value.Record.Type = capability.ProviderTypeAPIKey
	}
	if value.Record.Credentials == nil {
		value.Record.Credentials = map[string]any{}
	}
	if _, set := value.Record.Credentials["model_whitelist"]; !set {
		mapping := providercore.ResolveModelMapping(&value.Record, provideradapter.ModelDefaults())
		if len(mapping) == 0 {
			value.Record.Credentials["model_whitelist"] = []string{"*"}
		} else {
			models := make([]string, 0, len(mapping))
			for _, model := range mapping {
				models = append(models, model)
			}
			value.Record.Credentials["model_whitelist"] = models
		}
	}
	if groupID == nil {
		groupID = selectionFixtureGroupID(ctx)
	}
	if len(value.Record.GroupIDs) == 0 && len(value.Record.ProviderGroups) == 0 {
		value.Record.GroupIDs = []int64{*groupID}
	}
}

func (selectionFixtureGroups) GetByID(_ context.Context, id int64) (*routing.Group, error) {
	return &routing.Group{ID: id, Hydrated: true, Status: routing.StatusActive}, nil
}

func (s selectionFixtureGroups) GetByIDLite(ctx context.Context, id int64) (*routing.Group, error) {
	return s.GetByID(ctx, id)
}

func (s *mixedGroupProviders) GetByID(_ context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	for i := range s.values {
		if s.values[i].Record.ID == id {
			value := s.values[i]
			return &value, nil
		}
	}
	return nil, fmt.Errorf("provider %d missing", id)
}

func (s *mixedGroupProviders) ListSchedulableByGroupIDAndPlatforms(_ context.Context, group int64, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
	s.groupQueries = append(s.groupQueries, group)
	var out []gatewayprovider.ExecutionProvider
	for _, value := range s.values {
		if slices.Contains(platforms, value.Record.Platform) && slices.Contains(value.Record.GroupIDs, group) {
			out = append(out, value)
		}
	}
	return out, nil
}

func (s *mixedGroupProviders) ListSchedulableByGroupIDAndPlatform(ctx context.Context, group int64, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	return s.ListSchedulableByGroupIDAndPlatforms(ctx, group, []string{platform})
}

func mixedGroupProvider(id int64, platform, model string, groupID int64) gatewayprovider.ExecutionProvider {
	value := providercore.Record{ID: id, Platform: platform, Type: capability.ProviderTypeAPIKey, Status: providercore.StatusActive, Schedulable: true, Concurrency: 2, GroupIDs: []int64{groupID}, Credentials: map[string]any{"api_key": "test-key", "model_whitelist": []string{model}}}
	return *gatewayprovider.NewExecutionProvider(&value)
}

func withAdvancedSchedulerTestGroup(ctx context.Context, groupID int64) context.Context {
	return requeststate.WithGroup(ctx, &routing.Group{
		ID: groupID,

		SchedulerType: routing.GroupSchedulerTypeAdvanced,
		Status:        billing.StatusActive,
		Hydrated:      true,
	})
}

func (r schedulerTestOpenAIProviderRepo) GetByID(ctx context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	for i := range r.providers {
		if r.providers[i].Record.ID == id {
			prepareSelectionFixtureProvider(ctx, &r.providers[i], nil)
			return &r.providers[i], nil
		}
	}
	return nil, errors.New("provider not found")
}

func (r schedulerTestOpenAIProviderRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	return r.ListSchedulableByGroupIDAndPlatforms(ctx, groupID, []string{platform})
}

func (r schedulerTestOpenAIProviderRepo) ListSchedulableByPlatform(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	var result []gatewayprovider.ExecutionProvider
	for _, acc := range r.providers {
		if acc.Record.Platform == platform {
			result = append(result, acc)
		}
	}
	return result, nil
}

func (r schedulerTestOpenAIProviderRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}

// ListModelAvailabilityCandidates filters diagnosis candidates by stored configuration.
func (r schedulerTestOpenAIProviderRepo) ListModelAvailabilityCandidates(_ context.Context, groupID *int64, platforms []string, includeGrouped bool) ([]gatewayprovider.ExecutionProvider, error) {
	platformSet := make(map[string]struct{}, len(platforms))
	for _, platform := range platforms {
		platformSet[platform] = struct{}{}
	}
	result := make([]gatewayprovider.ExecutionProvider, 0, len(r.providers))
	for _, provider := range r.providers {
		if _, ok := platformSet[provider.Record.Platform]; !ok || provider.Record.Status != billing.StatusActive || !provider.Record.Schedulable {
			continue
		}
		if groupID != nil && !openAIStickyProviderMatchesGroup(&provider, groupID) {
			continue
		}
		if groupID == nil && !includeGrouped && !openAIStickyProviderMatchesGroup(&provider, nil) {
			continue
		}
		result = append(result, provider)
	}
	return result, nil
}

func (r schedulerGroupAwareOpenAIProviderRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	var result []gatewayprovider.ExecutionProvider
	for _, acc := range r.providers {
		if acc.Record.Platform == platform && openAIStickyProviderMatchesGroup(&acc, &groupID) {
			result = append(result, acc)
		}
	}
	return result, nil
}

func (r schedulerGroupAwareOpenAIProviderRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	var result []gatewayprovider.ExecutionProvider
	for _, acc := range r.providers {
		if acc.Record.Platform == platform && openAIStickyProviderMatchesGroup(&acc, nil) {
			result = append(result, acc)
		}
	}
	return result, nil
}

func (c schedulerTestConcurrencyCache) AcquireProviderSlot(ctx context.Context, providerID int64, maxConcurrency int, requestID string) (bool, error) {
	if c.acquiredIDs != nil {
		*c.acquiredIDs = append(*c.acquiredIDs, providerID)
	}
	if c.acquireResults != nil {
		if result, ok := c.acquireResults[providerID]; ok {
			return result, nil
		}
	}
	return true, nil
}

func (c schedulerTestConcurrencyCache) ReleaseProviderSlot(ctx context.Context, providerID int64, requestID string) error {
	if c.releasedIDs != nil {
		*c.releasedIDs = append(*c.releasedIDs, providerID)
	}
	return nil
}

func (c schedulerTestConcurrencyCache) GetProvidersLoadBatch(ctx context.Context, providers []schedulercore.ProviderWithConcurrency) (map[int64]*schedulercore.ProviderLoadInfo, error) {
	if c.loadBatchErr != nil {
		return nil, c.loadBatchErr
	}
	out := make(map[int64]*schedulercore.ProviderLoadInfo, len(providers))
	if c.skipDefaultLoad && c.loadMap != nil {
		for _, acc := range providers {
			if load, ok := c.loadMap[acc.ID]; ok {
				out[acc.ID] = load
			}
		}
		return out, nil
	}
	for _, acc := range providers {
		if c.loadMap != nil {
			if load, ok := c.loadMap[acc.ID]; ok {
				out[acc.ID] = load
				continue
			}
		}
		out[acc.ID] = &schedulercore.ProviderLoadInfo{ProviderID: acc.ID, LoadRate: 0}
	}
	return out, nil
}

func (c schedulerTestConcurrencyCache) GetProviderWaitingCount(ctx context.Context, providerID int64) (int, error) {
	if c.waitCounts != nil {
		if count, ok := c.waitCounts[providerID]; ok {
			return count, nil
		}
	}
	return 0, nil
}

func (c *schedulerTestGatewayCache) GetSessionProviderID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	if id, ok := c.sessionBindings[sessionHash]; ok {
		return id, nil
	}
	return 0, errors.New("not found")
}

func (c *schedulerTestGatewayCache) SetSessionProviderID(ctx context.Context, groupID int64, sessionHash string, providerID int64, ttl time.Duration) error {
	if c.sessionBindings == nil {
		c.sessionBindings = make(map[string]int64)
	}
	c.sessionBindings[sessionHash] = providerID
	return nil
}

func (c *schedulerTestGatewayCache) RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error {
	return nil
}

func (c *schedulerTestGatewayCache) DeleteSessionProviderID(ctx context.Context, groupID int64, sessionHash string) error {
	if c.sessionBindings == nil {
		return nil
	}
	if c.deletedSessions == nil {
		c.deletedSessions = make(map[string]int)
	}
	c.deletedSessions[sessionHash]++
	delete(c.sessionBindings, sessionHash)
	return nil
}

func (c *schedulerTestGatewayCache) SetSessionOwnerGroupID(ctx context.Context, userID int64, source, sessionHash string, groupID int64, ttl time.Duration) (bool, error) {
	return true, nil
}

func (c *schedulerTestGatewayCache) GetSessionOwnerGroupID(ctx context.Context, userID int64, source, sessionHash string) (int64, error) {
	return 0, errors.New("not found")
}

func (c *schedulerTestGatewayCache) RefreshSessionOwnerTTL(ctx context.Context, userID int64, source, sessionHash string, ttl time.Duration) error {
	return nil
}

func newSchedulerTestOpenAIWSV2Config() *config.Config {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = 3600
	return cfg
}

func (s *advancedSchedulerSettingRepoStub) Get(ctx context.Context, key string) (*settings.Setting, error) {
	value, err := s.GetValue(ctx, key)
	if err != nil {
		return nil, err
	}
	return &settings.Setting{Key: key, Value: value}, nil
}

func (s *advancedSchedulerSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if s == nil || s.values == nil {
		return "", settings.ErrSettingNotFound
	}
	value, ok := s.values[key]
	if !ok {
		return "", settings.ErrSettingNotFound
	}
	return value, nil
}

func (s *advancedSchedulerSettingRepoStub) Set(context.Context, string, string) error {
	panic("unexpected call to Set")
}

func (s *advancedSchedulerSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, err := s.GetValue(context.Background(), key); err == nil {
			result[key] = value
		}
	}
	return result, nil
}

func (s *advancedSchedulerSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected call to SetMultiple")
}

func (s *advancedSchedulerSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected call to GetAll")
}

func (s *advancedSchedulerSettingRepoStub) Delete(context.Context, string) error {
	panic("unexpected call to Delete")
}

func newAdvancedSchedulerParametersForTest(cfg *config.Config, _ string, values ...string) *schedulercore.Parameters {
	repo := &advancedSchedulerSettingRepoStub{
		values: map[string]string{},
	}
	if len(values) > 0 && values[0] != "" {
		repo.values[schedulercore.SettingKeyAdvancedSchedulerStickyWeightedEnabled] = values[0]
	}
	if len(values) > 1 && values[1] != "" {
		repo.values[schedulercore.SettingKeyAdvancedSchedulerSubscriptionPriorityEnabled] = values[1]
	}
	return schedulercore.NewParameters(schedulercore.NewSettingsRuntime(schedulercore.Diagnostics{}), repo, diagnosticParameterDefaults(cfg))
}

func (s *openAISnapshotCacheStub) GetSnapshot(ctx context.Context, bucket schedulercore.SchedulerBucket) ([]schedulercore.SnapshotProvider, bool, error) {
	if len(s.snapshotProviders) == 0 {
		return nil, false, nil
	}
	out := make([]schedulercore.SnapshotProvider, 0, len(s.snapshotProviders))
	for _, provider := range s.snapshotProviders {
		if provider == nil {
			continue
		}
		cloned := *provider
		prepareSelectionFixtureProvider(ctx, &cloned, &bucket.GroupID)
		out = append(out, codec.WrapRecord(&cloned.Record))
	}
	return out, true, nil
}

func (s *openAISnapshotCacheStub) GetProvider(ctx context.Context, providerID int64) (schedulercore.SnapshotProvider, error) {
	if s.providersByID == nil {
		return nil, nil
	}
	provider := s.providersByID[providerID]
	if provider == nil {
		return nil, nil
	}
	cloned := *provider
	prepareSelectionFixtureProvider(ctx, &cloned, nil)
	return codec.WrapRecord(&cloned.Record), nil
}

func (r schedulerTestOpenAIProviderRepo) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
	var result []gatewayprovider.ExecutionProvider
	for i := range r.providers {
		prepareSelectionFixtureProvider(ctx, &r.providers[i], &groupID)
		if slices.Contains(platforms, r.providers[i].Record.Platform) && openAIStickyProviderMatchesGroup(&r.providers[i], &groupID) {
			result = append(result, r.providers[i])
		}
	}
	return result, nil
}

func (r schedulerGroupAwareOpenAIProviderRepo) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
	var result []gatewayprovider.ExecutionProvider
	for i := range r.providers {
		if slices.Contains(platforms, r.providers[i].Record.Platform) && openAIStickyProviderMatchesGroup(&r.providers[i], &groupID) {
			prepareSelectionFixtureProvider(ctx, &r.providers[i], &groupID)
			result = append(result, r.providers[i])
		}
	}
	return result, nil
}

func (r schedulerGroupAwareOpenAIProviderRepo) GetByID(ctx context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	for i := range r.providers {
		if r.providers[i].Record.ID == id {
			copy := r.providers[i]
			groups := copy.Record.GroupIDs
			prepareSelectionFixtureProvider(ctx, &copy, nil)
			copy.Record.GroupIDs = groups
			return &copy, nil
		}
	}
	return nil, errors.New("provider not found")
}

func (r *thresholdSelectionProviderRepoStub) ListSchedulableByPlatform(_ context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	filtered := make([]gatewayprovider.ExecutionProvider, 0, len(r.providers))
	for _, provider := range r.providers {
		if provider.Record.Platform == platform {
			filtered = append(filtered, provider)
		}
	}
	return filtered, nil
}

func (r *thresholdSelectionProviderRepoStub) ListSchedulableByGroupIDAndPlatform(ctx context.Context, _ int64, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}

func (r *thresholdSelectionProviderRepoStub) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}

// responseSelectionOptions sets session and response TTLs while leaving scheduling waits at zero.
func responseSelectionOptions() Options {
	return Options{
		StickyTTL:   time.Hour,
		ResponseTTL: time.Hour,
	}
}

func responseSelectionParameters() *schedulercore.Parameters {
	return schedulercore.NewParameters(schedulercore.NewSettingsRuntime(schedulercore.Diagnostics{}), nil, diagnosticParameterDefaults(&config.Config{}))
}

func (r selectionProviderFixture) GetByID(ctx context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	for i := range r.providers {
		if r.providers[i].Record.ID == id {
			prepareSelectionFixtureProvider(ctx, &r.providers[i], nil)
			return &r.providers[i], nil
		}
	}
	return nil, errors.New("provider not found")
}

func (r selectionProviderFixture) ListSchedulableByPlatform(_ context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	var out []gatewayprovider.ExecutionProvider
	for _, value := range r.providers {
		if value.Record.Platform == platform {
			out = append(out, value)
		}
	}
	return out, nil
}

func (r selectionProviderFixture) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	return r.ListSchedulableByGroupIDAndPlatforms(ctx, groupID, []string{platform})
}

func (r selectionProviderFixture) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}

func (c *responseCacheFixture) GetSessionProviderID(ctx context.Context, group int64, key string) (int64, error) {
	return c.stickyCacheFixture.GetSessionProviderID(ctx, group, key)
}

func (c *responseCacheFixture) SetSessionProviderID(ctx context.Context, group int64, key string, id int64, ttl time.Duration) error {
	return c.stickyCacheFixture.SetSessionProviderID(ctx, group, key, id, ttl)
}

func (c *responseCacheFixture) RefreshSessionTTL(ctx context.Context, group int64, key string, ttl time.Duration) error {
	return c.stickyCacheFixture.RefreshSessionTTL(ctx, group, key, ttl)
}

func (c *responseCacheFixture) DeleteSessionProviderID(ctx context.Context, group int64, key string) error {
	return c.stickyCacheFixture.DeleteSessionProviderID(ctx, group, key)
}

func (c selectionConcurrencyFixture) AcquireProviderSlot(_ context.Context, id int64, _ int, _ string) (bool, error) {
	if value, ok := c.acquireResults[id]; ok {
		return value, nil
	}
	return true, nil
}

func (selectionConcurrencyFixture) ReleaseProviderSlot(context.Context, int64, string) error {
	return nil
}

func (c selectionConcurrencyFixture) GetProviderWaitingCount(_ context.Context, id int64) (int, error) {
	return c.waitCounts[id], nil
}

func (c selectionConcurrencyFixture) GetProvidersLoadBatch(ctx context.Context, providers []schedulercore.ProviderWithConcurrency) (map[int64]*schedulercore.ProviderLoadInfo, error) {
	if c.loadBatchErr != nil {
		return nil, c.loadBatchErr
	}
	out := make(map[int64]*schedulercore.ProviderLoadInfo, len(providers))
	if c.skipDefaultLoad && c.loadMap != nil {
		for _, acc := range providers {
			if load, ok := c.loadMap[acc.ID]; ok {
				out[acc.ID] = load
			}
		}
		return out, nil
	}
	for _, acc := range providers {
		if c.loadMap != nil {
			if load, ok := c.loadMap[acc.ID]; ok {
				out[acc.ID] = load
				continue
			}
		}
		out[acc.ID] = &schedulercore.ProviderLoadInfo{ProviderID: acc.ID, LoadRate: 0}
	}
	return out, nil
}

func (s hydrationProviderSource) GetByID(ctx context.Context, id int64) (schedulercore.SnapshotProvider, error) {
	value, err := s.source.GetByID(ctx, id)
	return codec.WrapRecord(gatewayprovider.ExecutionRecord(value)), err
}

func (r selectionProviderFixture) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
	var result []gatewayprovider.ExecutionProvider
	for i := range r.providers {
		prepareSelectionFixtureProvider(ctx, &r.providers[i], &groupID)
		if slices.Contains(platforms, r.providers[i].Record.Platform) && openAIStickyProviderMatchesGroup(&r.providers[i], &groupID) {
			result = append(result, r.providers[i])
		}
	}
	return result, nil
}

func (c *snapshotHydrationCache) GetSnapshot(ctx context.Context, bucket schedulercore.SchedulerBucket) ([]schedulercore.SnapshotProvider, bool, error) {
	out := make([]schedulercore.SnapshotProvider, 0, len(c.snapshot))
	for _, v := range c.snapshot {
		prepareSelectionFixtureProvider(ctx, v, &bucket.GroupID)
		out = append(out, codec.WrapRecord(gatewayprovider.ExecutionRecord(v)))
	}
	return out, true, nil
}

func (c *snapshotHydrationCache) GetProvider(ctx context.Context, id int64) (schedulercore.SnapshotProvider, error) {
	prepareSelectionFixtureProvider(ctx, c.providers[id], nil)
	return codec.WrapRecord(gatewayprovider.ExecutionRecord(c.providers[id])), nil
}

func newHydrationSnapshotForTest(cache *snapshotHydrationCache, source Providers) *schedulercore.SnapshotService {
	var read schedulercore.SnapshotProviderSource
	if source != nil {
		read = hydrationProviderSource{source: source}
	}
	return schedulercore.NewSnapshotService(cache, nil, read, nil, nil, schedulercore.SnapshotBindings{ProviderNotFound: providercore.ErrProviderNotFound, GroupNotFound: routing.ErrGroupNotFound, Diagnostics: schedulercore.Diagnostics{Logf: logging.LegacyPrintf, Event: logging.Event}})
}

func (c *stickyCacheFixture) GetSessionProviderID(_ context.Context, _ int64, key string) (int64, error) {
	if id, ok := c.sessionBindings[key]; ok {
		return id, nil
	}
	return 0, errors.New("not found")
}

func (c *stickyCacheFixture) SetSessionProviderID(_ context.Context, _ int64, key string, id int64, _ time.Duration) error {
	if c.sessionBindings == nil {
		c.sessionBindings = map[string]int64{}
	}
	c.sessionBindings[key] = id
	return nil
}

func (*stickyCacheFixture) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

func (c *stickyCacheFixture) DeleteSessionProviderID(_ context.Context, _ int64, key string) error {
	if c.sessionBindings == nil {
		return nil
	}
	if c.deletedSessions == nil {
		c.deletedSessions = map[string]int{}
	}
	c.deletedSessions[key]++
	delete(c.sessionBindings, key)
	return nil
}
