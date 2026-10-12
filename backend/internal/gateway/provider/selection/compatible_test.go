package selection

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	routingtestkit "github.com/TokenFlux/TokenRouter/internal/routing/testkit"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
	schedulerredis "github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	settingstestkit "github.com/TokenFlux/TokenRouter/internal/settings/testkit"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// noSlotSchedulerTestConcurrencyCache fails the test if auxiliary selection acquires a slot.
type noSlotSchedulerTestConcurrencyCache struct {
	schedulerTestConcurrencyCache
	acquireCalls int
}

type groupAwareStubOpenAIProviderRepo struct {
	selectionProviderFixture
}

// openAICodexExtraListRepo detects unexpected writes during quota-read tests.
type openAICodexExtraListRepo struct {
	selectionProviderFixture
	rateLimitCh chan time.Time
}

// TestOpenAIGetSchedulableProvider_AppliesGrokFreeSoftGate checks the Free OAuth soft gate on compatible selection.
func TestOpenAIGetSchedulableProvider_AppliesGrokFreeSoftGate(t *testing.T) {
	// Exercise the basic selector's sticky lookup path.
	cfg := &config.Config{}
	cfg.Gateway.Grok.FreeQuotaSoftGateEnabled = true
	cfg.Gateway.Grok.FreeQuotaTokenLimit = 500_000
	cfg.Gateway.Grok.FreeQuotaSoftGatePercent = 95
	cfg.Gateway.Grok.FreeQuotaWindowHours = 24
	cfg.Gateway.Grok.FreeQuotaStatsCacheSeconds = 60

	provider := healthyGrokOAuthGatewayTestProvider(8802, "tok")
	provider.Record.Credentials["subscription_tier"] = "free"
	provider.Record.Status = billing.StatusActive
	provider.Record.Schedulable = true

	repo := &mockProviderRepoForPlatform{
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{provider.Record.ID: provider},
	}
	usageRepo := &grokFreeQuotaUsageRepoStub{stats: map[int64]*usage.ProviderStats{
		provider.Record.ID: {Tokens: 480_000},
	}}
	factory := freeQuotaFactoryForTest(t, cfg, usageRepo)
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads:                Reads{Providers: repo},
		FreeQuota:            factory(),
		NewAdvancedFreeQuota: factory,
	}, cfg)

	got, err := svc.getSchedulableProvider(context.Background(), provider.Record.ID)
	require.NoError(t, err)
	require.NotNil(t, got, "first sticky hit fail-opens while free-gate stats refresh")

	require.Eventually(t, func() bool {
		got, err := svc.getSchedulableProvider(context.Background(), provider.Record.ID)
		return err == nil && got == nil
	}, 2*time.Second, 10*time.Millisecond, "OpenAI legacy sticky must apply free soft-gate after cache warm")
}

func TestOpenAISelectProviderWithLoadAwareness_FiltersUnschedulable(t *testing.T) {
	now := time.Now()
	resetAt := now.Add(10 * time.Minute)
	groupID := int64(1)

	rateLimited := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1,
			Platform:         capability.PlatformOpenAI,
			Type:             capability.ProviderTypeAPIKey,
			Status:           billing.StatusActive,
			Schedulable:      true,
			Concurrency:      1,
			Priority:         0,
			RateLimitResetAt: &resetAt,
		},
	}
	available := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    1,
		},
	}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: selectionProviderFixture{providers: []gatewayprovider.ExecutionProvider{
				rateLimited,
				available,
			}},
		},
		Shared: Shared{Concurrency: schedulercore.NewConcurrencyService(selectionConcurrencyFixture{}, schedulercore.Diagnostics{Logf: logging.LegacyPrintf, Event: logging.Event})},
	}, nil)

	selection, err := svc.SelectProviderWithLoadAwareness(context.Background(), &groupID, "", "gpt-5.2", nil)
	if err != nil {
		t.Fatalf("SelectProviderWithLoadAwareness error: %v", err)
	}
	if selection == nil || selection.Provider == nil {
		t.Fatalf("expected selection with provider")
	}
	if selection.Provider.Record.ID != available.Record.ID {
		t.Fatalf("expected provider %d, got %d", available.Record.ID, selection.Provider.Record.ID)
	}
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAISelectProviderWithLoadAwareness_ImageRateLimitSkipsOnlyImageRequests(t *testing.T) {
	future := time.Now().Add(10 * time.Minute).Format(time.RFC3339)
	groupID := int64(1)

	imageLimited := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Extra: map[string]any{
				"model_rate_limits": map[string]any{
					providercore.OpenAIImageGenerationRateLimitKey: map[string]any{
						"rate_limit_reset_at": future,
					},
				},
			},
		},
	}
	available := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    1,
		},
	}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: selectionProviderFixture{providers: []gatewayprovider.ExecutionProvider{
				imageLimited,
				available,
			}},
		},
		Shared: Shared{Concurrency: schedulercore.NewConcurrencyService(selectionConcurrencyFixture{}, schedulercore.Diagnostics{Logf: logging.LegacyPrintf, Event: logging.Event})},
	}, nil)

	imageSelection, err := svc.SelectProviderWithLoadAwareness(requeststate.WithOpenAIImageGenerationIntent(context.Background()), &groupID, "", "gpt-5.4", nil)
	require.NoError(t, err)
	require.NotNil(t, imageSelection)
	require.Equal(t, available.Record.ID, imageSelection.Provider.Record.ID)
	if imageSelection.ReleaseFunc != nil {
		imageSelection.ReleaseFunc()
	}

	textSelection, err := svc.SelectProviderWithLoadAwareness(context.Background(), &groupID, "", "gpt-5.4", nil)
	require.NoError(t, err)
	require.NotNil(t, textSelection)
	require.Equal(t, imageLimited.Record.ID, textSelection.Provider.Record.ID)
	if textSelection.ReleaseFunc != nil {
		textSelection.ReleaseFunc()
	}
}

func TestOpenAISelectProviderWithLoadAwareness_FiltersUnschedulableWhenNoConcurrencyService(t *testing.T) {
	now := time.Now()
	resetAt := now.Add(10 * time.Minute)
	groupID := int64(1)

	rateLimited := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1,
			Platform:         capability.PlatformOpenAI,
			Type:             capability.ProviderTypeAPIKey,
			Status:           billing.StatusActive,
			Schedulable:      true,
			Concurrency:      1,
			Priority:         0,
			RateLimitResetAt: &resetAt,
		},
	}
	available := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    1,
		},
	}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: selectionProviderFixture{providers: []gatewayprovider.ExecutionProvider{
				rateLimited,
				available,
			}},
		},
		Shared: Shared{},
	}, nil)

	// concurrencyService is nil, forcing the non-load-batch selection path.

	selection, err := svc.SelectProviderWithLoadAwareness(context.Background(), &groupID, "", "gpt-5.2", nil)
	if err != nil {
		t.Fatalf("SelectProviderWithLoadAwareness error: %v", err)
	}
	if selection == nil || selection.Provider == nil {
		t.Fatalf("expected selection with provider")
	}
	if selection.Provider.Record.ID != available.Record.ID {
		t.Fatalf("expected provider %d, got %d", available.Record.ID, selection.Provider.Record.ID)
	}
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAISelectProviderForModelWithExclusions_StickyUnschedulableClearsSession(t *testing.T) {
	sessionHash := "session-1"
	repo := selectionProviderFixture{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Status: billing.StatusDisabled, Schedulable: true, Concurrency: 1}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1}},
		},
	}
	cache := &schedulerTestGatewayCache{
		sessionBindings: map[string]int64{"openai:" + sessionHash: 1},
	}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{Reads: Reads{
		Providers: repo,
	}, Shared: Shared{Cache: cache}}, nil,
	)

	acc, err := svc.SelectProviderForModelWithExclusions(context.Background(), selectionFixtureGroupID(context.Background()), sessionHash, "gpt-4", nil)
	if err != nil {
		t.Fatalf("SelectProviderForModelWithExclusions error: %v", err)
	}
	if acc == nil || acc.Record.ID != 2 {
		t.Fatalf("expected provider 2, got %+v", acc)
	}
	if cache.deletedSessions["openai:"+sessionHash] != 1 {
		t.Fatalf("expected sticky session to be deleted")
	}
	if cache.sessionBindings["openai:"+sessionHash] != 2 {
		t.Fatalf("expected sticky session to bind to provider 2")
	}
}

func TestOpenAISelectProviderForModelWithExclusions_StickyOutsideGroupClearsSession(t *testing.T) {
	sessionHash := "session-outside-group"
	groupID := int64(1001)
	repo := groupAwareStubOpenAIProviderRepo{
		selectionProviderFixture{
			providers: []gatewayprovider.ExecutionProvider{
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1}},
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, ProviderGroups: []providercore.GroupMembership{{GroupID: groupID}}}},
			},
		},
	}
	cache := &schedulerTestGatewayCache{
		sessionBindings: map[string]int64{"openai:" + sessionHash: 1},
	}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{Reads: Reads{
		Providers: repo,
	}, Shared: Shared{Cache: cache}}, nil,
	)

	acc, err := svc.SelectProviderForModelWithExclusions(context.Background(), &groupID, sessionHash, "gpt-4", nil)
	if err != nil {
		t.Fatalf("SelectProviderForModelWithExclusions error: %v", err)
	}
	if acc == nil || acc.Record.ID != 2 {
		t.Fatalf("expected provider 2, got %+v", acc)
	}
	if cache.deletedSessions["openai:"+sessionHash] != 1 {
		t.Fatalf("expected sticky session to be deleted")
	}
	if cache.sessionBindings["openai:"+sessionHash] != 2 {
		t.Fatalf("expected sticky session to bind to provider 2")
	}
}

func TestOpenAISelectProviderWithLoadAwareness_StickyUnschedulableClearsSession(t *testing.T) {
	sessionHash := "session-2"
	groupID := int64(1)
	repo := selectionProviderFixture{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Status: billing.StatusDisabled, Schedulable: true, Concurrency: 1}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1}},
		},
	}
	cache := &schedulerTestGatewayCache{
		sessionBindings: map[string]int64{"openai:" + sessionHash: 1},
	}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: repo,
		},
		Shared: Shared{
			Concurrency: schedulercore.NewConcurrencyService(
				selectionConcurrencyFixture{}, schedulercore.Diagnostics{
					Logf:  logging.LegacyPrintf,
					Event: logging.Event,
				}),
			Cache: cache,
		},
	}, nil,
	)

	selection, err := svc.SelectProviderWithLoadAwareness(context.Background(), &groupID, sessionHash, "gpt-4", nil)
	if err != nil {
		t.Fatalf("SelectProviderWithLoadAwareness error: %v", err)
	}
	if selection == nil || selection.Provider == nil || selection.Provider.Record.ID != 2 {
		t.Fatalf("expected provider 2, got %+v", selection)
	}
	if cache.deletedSessions["openai:"+sessionHash] != 1 {
		t.Fatalf("expected sticky session to be deleted")
	}
	if cache.sessionBindings["openai:"+sessionHash] != 2 {
		t.Fatalf("expected sticky session to bind to provider 2")
	}
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAISelectProviderWithLoadAwareness_StickyOutsideGroupClearsSession(t *testing.T) {
	sessionHash := "session-load-outside-group"
	groupID := int64(1002)
	repo := groupAwareStubOpenAIProviderRepo{
		selectionProviderFixture{
			providers: []gatewayprovider.ExecutionProvider{
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1}},
				{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, ProviderGroups: []providercore.GroupMembership{{GroupID: groupID}}}},
			},
		},
	}
	cache := &schedulerTestGatewayCache{
		sessionBindings: map[string]int64{"openai:" + sessionHash: 1},
	}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: repo,
		},
		Shared: Shared{
			Cache:       cache,
			Concurrency: schedulercore.NewConcurrencyService(selectionConcurrencyFixture{}, schedulercore.Diagnostics{Logf: logging.LegacyPrintf, Event: logging.Event}),
		},
	}, nil,
	)

	selection, err := svc.SelectProviderWithLoadAwareness(context.Background(), &groupID, sessionHash, "gpt-4", nil)
	if err != nil {
		t.Fatalf("SelectProviderWithLoadAwareness error: %v", err)
	}
	if selection == nil || selection.Provider == nil || selection.Provider.Record.ID != 2 {
		t.Fatalf("expected provider 2, got %+v", selection)
	}
	if cache.deletedSessions["openai:"+sessionHash] != 1 {
		t.Fatalf("expected sticky session to be deleted")
	}
	if cache.sessionBindings["openai:"+sessionHash] != 2 {
		t.Fatalf("expected sticky session to bind to provider 2")
	}
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAISelectProviderForModelWithExclusions_NoModelSupport(t *testing.T) {
	repo := selectionProviderFixture{
		providers: []gatewayprovider.ExecutionProvider{
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 1,
					Platform:    capability.PlatformOpenAI,
					Status:      billing.StatusActive,
					Schedulable: true,
					Credentials: map[string]any{"model_mapping": map[string]any{"gpt-3.5-turbo": "gpt-3.5-turbo"}},
				},
			},
		},
	}
	cache := &schedulerTestGatewayCache{}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{Reads: Reads{
		Providers: repo,
	}, Shared: Shared{Cache: cache}}, nil,
	)

	acc, err := svc.SelectProviderForModelWithExclusions(context.Background(), selectionFixtureGroupID(context.Background()), "", "gpt-4", nil)
	if err == nil {
		t.Fatalf("expected error for unsupported model")
	}
	if acc != nil {
		t.Fatalf("expected nil provider for unsupported model")
	}
	if !strings.Contains(err.Error(), "does not support the requested model") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOpenAISelectProviderWithLoadAwareness_LoadBatchErrorFallback(t *testing.T) {
	groupID := int64(1)
	repo := selectionProviderFixture{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 2}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1}},
		},
	}
	cache := &schedulerTestGatewayCache{}
	concurrencyCache := selectionConcurrencyFixture{
		loadBatchErr: errors.New("load batch failed"),
	}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: repo,
		},
		Shared: Shared{
			Concurrency: schedulercore.NewConcurrencyService(
				concurrencyCache, schedulercore.Diagnostics{
					Logf:  logging.LegacyPrintf,
					Event: logging.Event,
				}),
			Cache: cache,
		},
	}, nil)

	selection, err := svc.SelectProviderWithLoadAwareness(context.Background(), &groupID, "fallback", "gpt-4", nil)
	if err != nil {
		t.Fatalf("SelectProviderWithLoadAwareness error: %v", err)
	}
	if selection == nil || selection.Provider == nil {
		t.Fatalf("expected selection")
	}
	if selection.Provider.Record.ID != 2 {
		t.Fatalf("expected provider 2, got %d", selection.Provider.Record.ID)
	}
	if cache.sessionBindings["openai:fallback"] != 2 {
		t.Fatalf("expected sticky session updated")
	}
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAISelectProviderWithLoadAwareness_NoSlotFallbackWait(t *testing.T) {
	groupID := int64(1)
	repo := selectionProviderFixture{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1}},
		},
	}
	cache := &schedulerTestGatewayCache{}
	concurrencyCache := selectionConcurrencyFixture{
		acquireResults: map[int64]bool{1: false},
		loadMap: map[int64]*schedulercore.ProviderLoadInfo{
			1: {ProviderID: 1, LoadRate: 10},
		},
	}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: repo,
		},
		Shared: Shared{
			Cache:       cache,
			Concurrency: schedulercore.NewConcurrencyService(concurrencyCache, schedulercore.Diagnostics{Logf: logging.LegacyPrintf, Event: logging.Event}),
		},
	}, nil)

	selection, err := svc.SelectProviderWithLoadAwareness(context.Background(), &groupID, "", "gpt-4", nil)
	if err != nil {
		t.Fatalf("SelectProviderWithLoadAwareness error: %v", err)
	}
	if selection == nil || selection.WaitPlan == nil {
		t.Fatalf("expected wait plan fallback")
	}
	if selection.Provider == nil || selection.Provider.Record.ID != 1 {
		t.Fatalf("expected provider 1")
	}
}

func TestOpenAISelectProviderForModelWithExclusions_SetsStickyBinding(t *testing.T) {
	sessionHash := "bind"
	repo := selectionProviderFixture{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1}},
		},
	}
	cache := &schedulerTestGatewayCache{}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{Reads: Reads{
		Providers: repo,
	}, Shared: Shared{Cache: cache}}, nil,
	)

	acc, err := svc.SelectProviderForModelWithExclusions(context.Background(), selectionFixtureGroupID(context.Background()), sessionHash, "gpt-4", nil)
	if err != nil {
		t.Fatalf("SelectProviderForModelWithExclusions error: %v", err)
	}
	if acc == nil || acc.Record.ID != 1 {
		t.Fatalf("expected provider 1")
	}
	if cache.sessionBindings["openai:"+sessionHash] != 1 {
		t.Fatalf("expected sticky session binding")
	}
}

func TestOpenAISelectProviderWithLoadAwareness_StickyWaitPlan(t *testing.T) {
	sessionHash := "sticky-wait"
	groupID := int64(1)
	repo := selectionProviderFixture{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1}},
		},
	}
	cache := &schedulerTestGatewayCache{
		sessionBindings: map[string]int64{"openai:" + sessionHash: 1},
	}
	concurrencyCache := selectionConcurrencyFixture{
		acquireResults: map[int64]bool{1: false},
		waitCounts:     map[int64]int{1: 0},
	}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: repo,
		},
		Shared: Shared{
			Cache:       cache,
			Concurrency: schedulercore.NewConcurrencyService(concurrencyCache, schedulercore.Diagnostics{Logf: logging.LegacyPrintf, Event: logging.Event}),
		},
	}, nil)

	selection, err := svc.SelectProviderWithLoadAwareness(context.Background(), &groupID, sessionHash, "gpt-4", nil)
	if err != nil {
		t.Fatalf("SelectProviderWithLoadAwareness error: %v", err)
	}
	if selection == nil || selection.WaitPlan == nil {
		t.Fatalf("expected sticky wait plan")
	}
	if selection.Provider == nil || selection.Provider.Record.ID != 1 {
		t.Fatalf("expected provider 1")
	}
}

func TestOpenAISelectProviderWithLoadAwareness_StickyCapacitySpilloverKeepsBinding(t *testing.T) {
	sessionHash := "sticky-spillover"
	groupID := int64(1)
	repo := selectionProviderFixture{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 6, Priority: 1, GroupIDs: []int64{groupID}}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 6, Priority: 1, GroupIDs: []int64{groupID}}},
		},
	}
	cache := &schedulerTestGatewayCache{
		sessionBindings: map[string]int64{"openai:" + sessionHash: 1},
	}
	concurrencyCache := selectionConcurrencyFixture{
		acquireResults: map[int64]bool{1: false, 2: true},
		waitCounts:     map[int64]int{1: 1},
		loadMap: map[int64]*schedulercore.ProviderLoadInfo{
			1: {ProviderID: 1, LoadRate: 100},
			2: {ProviderID: 2, LoadRate: 10},
		},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 1

	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: repo,
		},
		Shared: Shared{
			Cache:       cache,
			Concurrency: schedulercore.NewConcurrencyService(concurrencyCache, schedulercore.Diagnostics{Logf: logging.LegacyPrintf, Event: logging.Event}),
		},
	}, cfg)

	selection, err := svc.SelectProviderWithLoadAwareness(context.Background(), &groupID, sessionHash, "gpt-4", nil)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Provider)
	require.Equal(t, int64(2), selection.Provider.Record.ID, "capacity spillover should use the other provider for this request")
	require.True(t, selection.Acquired)
	require.Equal(t, int64(1), cache.sessionBindings["openai:"+sessionHash], "capacity spillover must not migrate the durable sticky binding")
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAISelectProviderWithLoadAwareness_PrefersLowerLoad(t *testing.T) {
	groupID := int64(1)
	repo := selectionProviderFixture{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1}},
		},
	}
	cache := &schedulerTestGatewayCache{}
	concurrencyCache := selectionConcurrencyFixture{
		loadMap: map[int64]*schedulercore.ProviderLoadInfo{
			1: {ProviderID: 1, LoadRate: 80},
			2: {ProviderID: 2, LoadRate: 10},
		},
	}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: repo,
		},
		Shared: Shared{
			Cache:       cache,
			Concurrency: schedulercore.NewConcurrencyService(concurrencyCache, schedulercore.Diagnostics{Logf: logging.LegacyPrintf, Event: logging.Event}),
		},
	}, nil)

	selection, err := svc.SelectProviderWithLoadAwareness(context.Background(), &groupID, "load", "gpt-4", nil)
	if err != nil {
		t.Fatalf("SelectProviderWithLoadAwareness error: %v", err)
	}
	if selection == nil || selection.Provider == nil || selection.Provider.Record.ID != 2 {
		t.Fatalf("expected provider 2")
	}
	if cache.sessionBindings["openai:load"] != 2 {
		t.Fatalf("expected sticky session updated")
	}
}

func TestOpenAISelectProviderForModelWithExclusions_StickyExcludedFallback(t *testing.T) {
	sessionHash := "excluded"
	repo := selectionProviderFixture{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 2}},
		},
	}
	cache := &schedulerTestGatewayCache{
		sessionBindings: map[string]int64{"openai:" + sessionHash: 1},
	}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{Reads: Reads{
		Providers: repo,
	}, Shared: Shared{Cache: cache}}, nil,
	)

	excluded := map[int64]struct{}{1: {}}
	acc, err := svc.SelectProviderForModelWithExclusions(context.Background(), selectionFixtureGroupID(context.Background()), sessionHash, "gpt-4", excluded)
	if err != nil {
		t.Fatalf("SelectProviderForModelWithExclusions error: %v", err)
	}
	if acc == nil || acc.Record.ID != 2 {
		t.Fatalf("expected provider 2")
	}
}

func TestOpenAISelectProviderForModelWithExclusions_KeepsCompatibleCrossPlatformSticky(t *testing.T) {
	sessionHash := "non-openai"
	repo := selectionProviderFixture{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformAnthropic, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 2}},
		},
	}
	cache := &schedulerTestGatewayCache{
		sessionBindings: map[string]int64{"openai:" + sessionHash: 1},
	}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{Reads: Reads{
		Providers: repo,
	}, Shared: Shared{Cache: cache}}, nil,
	)

	acc, err := svc.SelectProviderForModelWithExclusions(context.Background(), selectionFixtureGroupID(context.Background()), sessionHash, "gpt-4", nil)
	if err != nil {
		t.Fatalf("SelectProviderForModelWithExclusions error: %v", err)
	}
	if acc == nil || acc.Record.ID != 1 {
		t.Fatalf("expected cross-platform sticky provider 1")
	}
}

func TestOpenAISelectProviderForModelWithExclusions_NoProviders(t *testing.T) {
	repo := selectionProviderFixture{providers: []gatewayprovider.ExecutionProvider{}}
	cache := &schedulerTestGatewayCache{}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{Reads: Reads{
		Providers: repo,
	}, Shared: Shared{Cache: cache}}, nil,
	)

	acc, err := svc.SelectProviderForModelWithExclusions(context.Background(), selectionFixtureGroupID(context.Background()), "", "", nil)
	if err == nil {
		t.Fatalf("expected error for no providers")
	}
	if acc != nil {
		t.Fatalf("expected nil provider")
	}
	if !strings.Contains(err.Error(), "no available OpenAI providers") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOpenAISelectProviderWithLoadAwareness_NoCandidates(t *testing.T) {
	groupID := int64(1)
	resetAt := time.Now().Add(1 * time.Hour)
	repo := selectionProviderFixture{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1, RateLimitResetAt: &resetAt}},
		},
	}
	cache := &schedulerTestGatewayCache{}
	concurrencyCache := selectionConcurrencyFixture{}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: repo,
		},
		Shared: Shared{
			Cache:       cache,
			Concurrency: schedulercore.NewConcurrencyService(concurrencyCache, schedulercore.Diagnostics{Logf: logging.LegacyPrintf, Event: logging.Event}),
		},
	}, nil)

	selection, err := svc.SelectProviderWithLoadAwareness(context.Background(), &groupID, "", "gpt-4", nil)
	if err == nil {
		t.Fatalf("expected error for no candidates")
	}
	if selection != nil {
		t.Fatalf("expected nil selection")
	}
}

func TestOpenAISelectProviderWithLoadAwareness_AllFullWaitPlan(t *testing.T) {
	groupID := int64(1)
	repo := selectionProviderFixture{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1}},
		},
	}
	cache := &schedulerTestGatewayCache{}
	concurrencyCache := selectionConcurrencyFixture{
		loadMap: map[int64]*schedulercore.ProviderLoadInfo{
			1: {ProviderID: 1, LoadRate: 100},
		},
	}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: repo,
		},
		Shared: Shared{
			Concurrency: schedulercore.NewConcurrencyService(
				concurrencyCache, schedulercore.Diagnostics{
					Logf:  logging.LegacyPrintf,
					Event: logging.Event,
				}),
			Cache: cache,
		},
	}, nil)

	selection, err := svc.SelectProviderWithLoadAwareness(context.Background(), &groupID, "", "gpt-4", nil)
	if err != nil {
		t.Fatalf("SelectProviderWithLoadAwareness error: %v", err)
	}
	if selection == nil || selection.WaitPlan == nil {
		t.Fatalf("expected wait plan")
	}
}

func TestOpenAISelectProviderWithLoadAwareness_LoadBatchErrorNoAcquire(t *testing.T) {
	groupID := int64(1)
	repo := selectionProviderFixture{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1}},
		},
	}
	cache := &schedulerTestGatewayCache{}
	concurrencyCache := selectionConcurrencyFixture{
		loadBatchErr:   errors.New("load batch failed"),
		acquireResults: map[int64]bool{1: false},
	}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: repo,
		},
		Shared: Shared{
			Cache:       cache,
			Concurrency: schedulercore.NewConcurrencyService(concurrencyCache, schedulercore.Diagnostics{Logf: logging.LegacyPrintf, Event: logging.Event}),
		},
	}, nil)

	selection, err := svc.SelectProviderWithLoadAwareness(context.Background(), &groupID, "", "gpt-4", nil)
	if err != nil {
		t.Fatalf("SelectProviderWithLoadAwareness error: %v", err)
	}
	if selection == nil || selection.WaitPlan == nil {
		t.Fatalf("expected wait plan")
	}
}

func TestOpenAISelectProviderWithLoadAwareness_MissingLoadInfo(t *testing.T) {
	groupID := int64(1)
	repo := selectionProviderFixture{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1}},
		},
	}
	cache := &schedulerTestGatewayCache{}
	concurrencyCache := selectionConcurrencyFixture{
		loadMap: map[int64]*schedulercore.ProviderLoadInfo{
			1: {ProviderID: 1, LoadRate: 50},
		},
		skipDefaultLoad: true,
	}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: repo,
		},
		Shared: Shared{
			Cache:       cache,
			Concurrency: schedulercore.NewConcurrencyService(concurrencyCache, schedulercore.Diagnostics{Logf: logging.LegacyPrintf, Event: logging.Event}),
		},
	}, nil)

	selection, err := svc.SelectProviderWithLoadAwareness(context.Background(), &groupID, "", "gpt-4", nil)
	if err != nil {
		t.Fatalf("SelectProviderWithLoadAwareness error: %v", err)
	}
	if selection == nil || selection.Provider == nil || selection.Provider.Record.ID != 2 {
		t.Fatalf("expected provider 2")
	}
}

func TestOpenAISelectProviderForModelWithExclusions_LeastRecentlyUsed(t *testing.T) {
	oldTime := time.Now().Add(-2 * time.Hour)
	newTime := time.Now().Add(-1 * time.Hour)
	repo := selectionProviderFixture{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Priority: 1, LastUsedAt: &newTime}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Priority: 1, LastUsedAt: &oldTime}},
		},
	}
	cache := &schedulerTestGatewayCache{}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{Reads: Reads{
		Providers: repo,
	}, Shared: Shared{Cache: cache}}, nil,
	)

	acc, err := svc.SelectProviderForModelWithExclusions(context.Background(), selectionFixtureGroupID(context.Background()), "", "gpt-4", nil)
	if err != nil {
		t.Fatalf("SelectProviderForModelWithExclusions error: %v", err)
	}
	if acc == nil || acc.Record.ID != 2 {
		t.Fatalf("expected provider 2")
	}
}

func TestOpenAISelectProviderWithLoadAwareness_PreferNeverUsed(t *testing.T) {
	groupID := int64(1)
	lastUsed := time.Now().Add(-1 * time.Hour)
	repo := selectionProviderFixture{
		providers: []gatewayprovider.ExecutionProvider{
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1, LastUsedAt: &lastUsed}},
			{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1}},
		},
	}
	cache := &schedulerTestGatewayCache{}
	concurrencyCache := selectionConcurrencyFixture{
		loadMap: map[int64]*schedulercore.ProviderLoadInfo{
			1: {ProviderID: 1, LoadRate: 10},
			2: {ProviderID: 2, LoadRate: 10},
		},
	}

	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: repo,
		},
		Shared: Shared{
			Cache:       cache,
			Concurrency: schedulercore.NewConcurrencyService(concurrencyCache, schedulercore.Diagnostics{Logf: logging.LegacyPrintf, Event: logging.Event}),
		},
	}, nil)

	selection, err := svc.SelectProviderWithLoadAwareness(context.Background(), &groupID, "", "gpt-4", nil)
	if err != nil {
		t.Fatalf("SelectProviderWithLoadAwareness error: %v", err)
	}
	if selection == nil || selection.Provider == nil || selection.Provider.Record.ID != 2 {
		t.Fatalf("expected provider 2")
	}
}

func TestOpenAISelectProviderForModelWithExclusions_GroupMappedRestrictionRejectsEarly(t *testing.T) {
	t.Parallel()

	pricingConfigSvc := routingtestkit.NewConfigServiceFixture(routingtestkit.StandardPricingConfigRepository(routingtestkit.Configuration{
		ID:                 1,
		Status:             billing.StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: routing.BillingModelSourceGroupMapped,
		ModelPricing: []routing.ModelPricingEntry{
			{Models: []string{"gpt-4o"}},
		},
		ModelMapping: map[string]string{"gpt-4.1": "o3-mini"},
	}, map[int64]string{10: capability.PlatformOpenAI}))

	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: selectionProviderFixture{providers: []gatewayprovider.ExecutionProvider{{Record: providercore.Record{
				Credentials:  map[string]any{"model_whitelist": []string{"*"}},
				LoadLocation: time.LoadLocation,
				ID:           1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive,
				Schedulable: true,
			}}}},
		},
		Shared: Shared{GroupPolicies: pricingConfigSvc},
	}, nil)

	groupID := int64(10)
	_, err := svc.SelectProviderForModelWithExclusions(context.Background(), &groupID, "", "gpt-4.1", nil)
	require.ErrorIs(t, err, schedulercore.ErrNoAvailableProviders)
	require.Contains(t, err.Error(), "group model restriction")
}

func TestOpenAISelectProviderForModelWithExclusions_UpstreamRestrictionSkipsDisallowedProvider(t *testing.T) {
	t.Parallel()

	pricingConfigSvc := routingtestkit.NewConfigServiceFixture(routingtestkit.StandardPricingConfigRepository(routingtestkit.Configuration{
		ID:                 1,
		Status:             billing.StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: routing.BillingModelSourceUpstream,
		ModelPricing: []routing.ModelPricingEntry{
			{Models: []string{"o3-mini"}},
		},
	}, map[int64]string{10: capability.PlatformOpenAI}))

	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: selectionProviderFixture{providers: []gatewayprovider.ExecutionProvider{
				{Record: providercore.Record{
					LoadLocation: time.LoadLocation,
					ID:           1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive,
					Schedulable: true, Priority: 10, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-4.1": "gpt-4o"}},
				}},
				{Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Status: billing.StatusActive,
					Schedulable: true, Priority: 20,
					Credentials: map[string]any{"model_mapping": map[string]any{"gpt-4.1": "o3-mini"}},
				}},
			}},
		},
		Shared: Shared{GroupPolicies: pricingConfigSvc},
	}, nil)

	groupID := int64(10)
	provider, err := svc.SelectProviderForModelWithExclusions(context.Background(), &groupID, "", "gpt-4.1", nil)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(2), provider.Record.ID)
}

func TestOpenAISelectProviderForModelWithExclusions_StickyRestrictedUpstreamFallsBack(t *testing.T) {
	t.Parallel()

	pricingConfigSvc := routingtestkit.NewConfigServiceFixture(routingtestkit.StandardPricingConfigRepository(routingtestkit.Configuration{
		ID:                 1,
		Status:             billing.StatusActive,
		GroupIDs:           []int64{10},
		RestrictModels:     true,
		BillingModelSource: routing.BillingModelSourceUpstream,
		ModelPricing: []routing.ModelPricingEntry{
			{Models: []string{"o3-mini"}},
		},
	}, map[int64]string{10: capability.PlatformOpenAI}))

	cache := &schedulerTestGatewayCache{
		sessionBindings: map[string]int64{"openai:sticky-session": 1},
	}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: selectionProviderFixture{providers: []gatewayprovider.ExecutionProvider{
				{Record: providercore.Record{
					LoadLocation: time.LoadLocation,
					ID:           1, Platform: capability.PlatformOpenAI, Status: billing.StatusActive,
					Schedulable: true, Priority: 10, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-4.1": "gpt-4o"}},
				}},
				{Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 2, Platform: capability.PlatformOpenAI, Status: billing.StatusActive,
					Schedulable: true, Priority: 20,
					Credentials: map[string]any{"model_mapping": map[string]any{"gpt-4.1": "o3-mini"}},
				}},
			}},
		},
		Shared: Shared{
			GroupPolicies: pricingConfigSvc,

			Cache: cache,
		},
	}, nil)

	groupID := int64(10)
	provider, err := svc.SelectProviderForModelWithExclusions(context.Background(), &groupID, "sticky-session", "gpt-4.1", nil)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(2), provider.Record.ID)
	require.Equal(t, 1, cache.deletedSessions["openai:sticky-session"])
	require.Equal(t, int64(2), cache.sessionBindings["openai:sticky-session"])
}

func (c *noSlotSchedulerTestConcurrencyCache) AcquireProviderSlot(ctx context.Context, providerID int64, maxConcurrency int, requestID string) (bool, error) {
	c.acquireCalls++
	return false, errors.New("辅助选择不应申请并发槽")
}

func TestOpenAIGatewayService_SelectProviderForModelWithExclusions_AdvancedGroupSkipsConcurrencySlot(t *testing.T) {
	groupID := int64(10105)
	ctx := requeststate.WithGroup(context.Background(), &routing.Group{
		ID: groupID,

		SchedulerType: routing.GroupSchedulerTypeAdvanced,
		Status:        billing.StatusActive,
		Hydrated:      true,
	})
	concurrencyCache := &noSlotSchedulerTestConcurrencyCache{}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{Providers: schedulerTestOpenAIProviderRepo{providers: []gatewayprovider.ExecutionProvider{{Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 36000, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
			Status: billing.StatusActive, Schedulable: true, Concurrency: 1,
		}}}}},
		Shared: Shared{
			Cache: &schedulerTestGatewayCache{},
			Concurrency: schedulercore.NewConcurrencyService(concurrencyCache, schedulercore.Diagnostics{
				Logf:  logging.LegacyPrintf,
				Event: logging.Event,
			}),
		},
	}, &config.Config{})

	provider, err := svc.SelectProviderForModelWithExclusions(ctx, &groupID, "", "gpt-5.1", nil)

	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(36000), provider.Record.ID)
	require.Zero(t, concurrencyCache.acquireCalls, "仅选提供商入口不得占用真实并发槽")
}

func TestOpenAIGatewayService_SelectProviderForTokenCount_DoesNotAcquireGenerationSlot(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10115)
	acquiredIDs := make([]int64, 0)
	providers := []gatewayprovider.ExecutionProvider{
		{
			Record: providercore.Record{
				LoadLocation: time.LoadLocation, ID: 36501, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
				Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
				Credentials: map[string]any{"model_whitelist": []string{"*"}, "openai_capabilities": []any{"chat_completions"}},
			},
		},
		{
			Record: providercore.Record{
				LoadLocation: time.LoadLocation, ID: 36502, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
				Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 5,
				Credentials: map[string]any{"model_whitelist": []string{"*"}, "openai_capabilities": []any{"embeddings"}},
			},
		},
		{
			Record: providercore.Record{
				LoadLocation: time.LoadLocation, ID: 36503, Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey,
				Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 10,
				Credentials: map[string]any{"model_whitelist": []string{"*"}, "openai_capabilities": []any{"chat_completions"}},
			},
		},
		{
			Record: providercore.Record{
				LoadLocation: time.LoadLocation, ID: 36504, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
				Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 15,
				Credentials: map[string]any{
					"openai_capabilities": []any{"chat_completions"},
					"model_mapping":       map[string]any{"gpt-4o": "gpt-4o"},
				},
			},
		},
	}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{Providers: schedulerTestOpenAIProviderRepo{providers: providers}},
		Shared: Shared{
			Cache:       &schedulerTestGatewayCache{},
			Concurrency: schedulercore.NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{36501: false}, acquiredIDs: &acquiredIDs}, schedulercore.Diagnostics{Logf: logging.LegacyPrintf, Event: logging.Event}),
		},
	}, &config.Config{})

	provider, err := svc.SelectProviderForTokenCount(
		ctx,
		&groupID,
		"",
		"gpt-5.1",
		providercore.OpenAIEndpointCapabilityTextGeneration,
		capability.PlatformOpenAI,
	)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(36501), provider.Record.ID)
	require.Empty(t, acquiredIDs, "token counting must not acquire a generation slot")
}

func TestOpenAIGatewayService_SelectProviderForModelWithExclusions_AutoPauseBy5hThreshold(t *testing.T) {
	ctx := context.Background()
	primary := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35001,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Extra: map[string]any{
				"codex_5h_used_percent":   95.0,
				"auto_pause_5h_threshold": 0.95,
			},
		},
	}
	secondary := gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35002, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads:  Reads{Providers: schedulerTestOpenAIProviderRepo{providers: []gatewayprovider.ExecutionProvider{primary, secondary}}},
		Shared: Shared{},
	}, &config.Config{})

	provider, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(35002), provider.Record.ID)
}

func TestOpenAIGatewayService_SelectProviderForModelWithExclusions_AllowsBelow5hThreshold(t *testing.T) {
	ctx := context.Background()
	primary := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35101,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Extra: map[string]any{
				"codex_5h_used_percent":   80.0,
				"auto_pause_5h_threshold": 0.95,
			},
		},
	}
	secondary := gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35102, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads:  Reads{Providers: schedulerTestOpenAIProviderRepo{providers: []gatewayprovider.ExecutionProvider{primary, secondary}}},
		Shared: Shared{},
	}, &config.Config{})

	provider, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(35101), provider.Record.ID)
}

func TestOpenAIGatewayService_SelectProviderForModelWithExclusions_AutoPauseBy7dThreshold(t *testing.T) {
	ctx := context.Background()
	primary := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35201,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Extra: map[string]any{
				"codex_7d_used_percent":   95.0,
				"auto_pause_7d_threshold": 0.95,
			},
		},
	}
	secondary := gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35202, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads:  Reads{Providers: schedulerTestOpenAIProviderRepo{providers: []gatewayprovider.ExecutionProvider{primary, secondary}}},
		Shared: Shared{},
	}, &config.Config{})

	provider, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(35202), provider.Record.ID)
}

func TestOpenAIGatewayService_SelectProviderForModelWithExclusions_UnconfiguredThresholdKeepsLegacyBehavior(t *testing.T) {
	ctx := context.Background()
	primary := gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35301, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, Extra: map[string]any{"codex_5h_used_percent": 99.0, "codex_7d_used_percent": 99.0}}}
	secondary := gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35302, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads:  Reads{Providers: schedulerTestOpenAIProviderRepo{providers: []gatewayprovider.ExecutionProvider{primary, secondary}}},
		Shared: Shared{},
	}, &config.Config{})

	provider, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(35301), provider.Record.ID)
}

func TestOpenAIGatewayService_SelectProviderForModelWithExclusions_UsesGlobalDefaultThreshold(t *testing.T) {
	ctx := gatewayprovider.WithQuotaAutoPauseSettings(context.Background(), ops.OpsOpenAIProviderQuotaAutoPauseSettings{DefaultThreshold5h: 0.95})
	primary := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35401,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Extra: map[string]any{
				"codex_5h_used_percent": 95.0,
			},
		},
	}
	secondary := gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35402, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads:  Reads{Providers: schedulerTestOpenAIProviderRepo{providers: []gatewayprovider.ExecutionProvider{primary, secondary}}},
		Shared: Shared{},
	}, &config.Config{})

	provider, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(35402), provider.Record.ID)
}

// TestOpenAIGatewayService_SelectProviderForModelWithExclusions_PerProviderDisableOverridesGlobalDefault checks that a provider's disable flag overrides the global threshold.
// An unset provider threshold alone would inherit the global value.
func TestOpenAIGatewayService_SelectProviderForModelWithExclusions_PerProviderDisableOverridesGlobalDefault(t *testing.T) {
	ctx := gatewayprovider.WithQuotaAutoPauseSettings(context.Background(), ops.OpsOpenAIProviderQuotaAutoPauseSettings{DefaultThreshold5h: 0.95})
	// High usage and an unset threshold would normally trigger the global pause rule.
	// The provider disables automatic pausing.
	primary := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35701,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Extra: map[string]any{
				"codex_5h_used_percent":  99.0,
				"auto_pause_5h_disabled": true,
			},
		},
	}
	secondary := gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35702, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads:  Reads{Providers: schedulerTestOpenAIProviderRepo{providers: []gatewayprovider.ExecutionProvider{primary, secondary}}},
		Shared: Shared{},
	}, &config.Config{})

	provider, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(35701), provider.Record.ID)
}

// TestOpenAIGatewayService_SelectProviderForModelWithExclusions_PerWindowDisableScoped checks that disabling the 5h pause leaves the 7d pause active.
func TestOpenAIGatewayService_SelectProviderForModelWithExclusions_PerWindowDisableScoped(t *testing.T) {
	ctx := context.Background()
	primary := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35801,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Extra: map[string]any{
				"codex_5h_used_percent":   99.0,
				"codex_7d_used_percent":   99.0,
				"auto_pause_5h_disabled":  true,
				"auto_pause_7d_threshold": 0.95,
			},
		},
	}
	secondary := gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35802, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads:  Reads{Providers: schedulerTestOpenAIProviderRepo{providers: []gatewayprovider.ExecutionProvider{primary, secondary}}},
		Shared: Shared{},
	}, &config.Config{})

	provider, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(35802), provider.Record.ID, "7d auto-pause must still fire even though 5h is disabled")
}

func TestOpenAIGatewayService_SelectProviderForModelWithExclusions_StaleUsageWindowResetSkipsPause(t *testing.T) {
	ctx := context.Background()
	// The reset time has passed, so cached usage is stale.
	// Allowing traffic lets response headers refresh the window.
	primary := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35501,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Extra: map[string]any{
				"codex_5h_used_percent":   99.0,
				"auto_pause_5h_threshold": 0.95,
				"codex_5h_reset_at":       time.Now().Add(-time.Minute).Format(time.RFC3339),
			},
		},
	}
	secondary := gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35502, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads:  Reads{Providers: schedulerTestOpenAIProviderRepo{providers: []gatewayprovider.ExecutionProvider{primary, secondary}}},
		Shared: Shared{},
	}, &config.Config{})

	provider, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(35501), provider.Record.ID)
}

func TestOpenAIGatewayService_SelectProviderForModelWithExclusions_FreshUsageWindowStillPauses(t *testing.T) {
	ctx := context.Background()
	// The exhausted window has not reset, so the provider remains paused.
	primary := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35601,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Extra: map[string]any{
				"codex_5h_used_percent":   99.0,
				"auto_pause_5h_threshold": 0.95,
				"codex_5h_reset_at":       time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		},
	}
	secondary := gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35602, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads:  Reads{Providers: schedulerTestOpenAIProviderRepo{providers: []gatewayprovider.ExecutionProvider{primary, secondary}}},
		Shared: Shared{},
	}, &config.Config{})

	provider, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(35602), provider.Record.ID)
}

// TestOpenAIGatewayService_SelectProviderForModelWithExclusions_StaleUsageSnapshotSkipsPause_Issue2994 checks that stale usage permits a probe request.
// Snapshot age uses observation time; allowing traffic refreshes usage from response headers.
func TestOpenAIGatewayService_SelectProviderForModelWithExclusions_StaleUsageSnapshotSkipsPause_Issue2994(t *testing.T) {
	ctx := context.Background()
	primary := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35701,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Extra: map[string]any{
				"codex_5h_used_percent":   99.0,
				"auto_pause_5h_threshold": 0.95,
				// The future reset time isolates the stale-snapshot rule.
				"codex_5h_reset_at": time.Now().Add(time.Hour).Format(time.RFC3339),
				// This three-hour-old snapshot exceeds the two-hour auto-pause validity period.
				"codex_usage_updated_at": time.Now().Add(-3 * time.Hour).Format(time.RFC3339),
			},
		},
	}
	secondary := gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35702, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads:  Reads{Providers: schedulerTestOpenAIProviderRepo{providers: []gatewayprovider.ExecutionProvider{primary, secondary}}},
		Shared: Shared{},
	}, &config.Config{})

	provider, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(35701), provider.Record.ID)
}

// TestOpenAIGatewayService_SelectProviderForModelWithExclusions_FreshExhaustedSnapshotStillPauses_Issue2994 checks that fresh exhausted quota still pauses the provider.
// A fresh snapshot at 99% usage remains eligible for automatic pause.
func TestOpenAIGatewayService_SelectProviderForModelWithExclusions_FreshExhaustedSnapshotStillPauses_Issue2994(t *testing.T) {
	ctx := context.Background()
	primary := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35801,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Extra: map[string]any{
				"codex_5h_used_percent":   99.0,
				"auto_pause_5h_threshold": 0.95,
				"codex_5h_reset_at":       time.Now().Add(time.Hour).Format(time.RFC3339),
				// The one-minute-old snapshot is fresh, so the provider stays paused.
				"codex_usage_updated_at": time.Now().Add(-time.Minute).Format(time.RFC3339),
			},
		},
	}
	secondary := gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 35802, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 5}}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads:  Reads{Providers: schedulerTestOpenAIProviderRepo{providers: []gatewayprovider.ExecutionProvider{primary, secondary}}},
		Shared: Shared{},
	}, &config.Config{})

	provider, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(35802), provider.Record.ID)
}

func TestOpenAIGatewayService_SelectProviderForModelWithExclusions_SkipsFreshlyRateLimitedSnapshotCandidate(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10102)
	rateLimitedUntil := time.Now().Add(30 * time.Minute)
	stalePrimary := &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 32001, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}}}
	staleSecondary := &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 32002, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, GroupIDs: []int64{groupID}}}
	freshPrimary := &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 32001, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}, RateLimitResetAt: &rateLimitedUntil}}
	freshSecondary := &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 32002, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, GroupIDs: []int64{groupID}}}
	snapshotCache := &openAISnapshotCacheStub{snapshotProviders: []*gatewayprovider.ExecutionProvider{stalePrimary, staleSecondary}, providersByID: map[int64]*gatewayprovider.ExecutionProvider{32001: freshPrimary, 32002: freshSecondary}}
	snapshotService := schedulercore.NewSnapshotService(snapshotCache, nil, nil, nil, nil)
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: schedulerTestOpenAIProviderRepo{providers: []gatewayprovider.ExecutionProvider{*freshPrimary, *freshSecondary}},
			Snapshot:  schedulerredis.NewSnapshotReader(snapshotService),
		},
		Shared: Shared{
			Health:     gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{}),
			Parameters: newAdvancedSchedulerParametersForTest(&config.Config{}, "true"),
		},
	}, &config.Config{})

	provider, err := svc.SelectProviderForModelWithExclusions(ctx, &groupID, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(32002), provider.Record.ID)
}

func TestOpenAIGatewayService_SelectProviderForModelWithExclusions_ModelRateLimitOnlySkipsThatModel(t *testing.T) {
	ctx := context.Background()
	resetAt := time.Now().Add(30 * time.Minute).Format(time.RFC3339)
	primary := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 32101,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Extra: map[string]any{
				"model_rate_limits": map[string]any{
					"gpt-5.4": map[string]any{
						"rate_limit_reset_at": resetAt,
					},
				},
			},
		},
	}
	secondary := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 32102,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    5,
		},
	}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads:  Reads{Providers: schedulerTestOpenAIProviderRepo{providers: []gatewayprovider.ExecutionProvider{primary, secondary}}},
		Shared: Shared{},
	}, &config.Config{})

	provider, err := svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gpt-5.4", nil)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(32102), provider.Record.ID)

	provider, err = svc.SelectProviderForModelWithExclusions(ctx, selectionFixtureGroupID(ctx), "", "gpt-5.3", nil)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(32101), provider.Record.ID)
}

func TestOpenAIGatewayService_SelectProviderForModelWithExclusions_DBRuntimeRecheckSkipsStaleCachedCandidate(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10104)
	rateLimitedUntil := time.Now().Add(30 * time.Minute)
	stalePrimary := &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 34001, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}}}
	staleSecondary := &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 34002, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, GroupIDs: []int64{groupID}}}
	dbPrimary := gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 34001, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}, RateLimitResetAt: &rateLimitedUntil}}
	dbSecondary := gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 34002, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, GroupIDs: []int64{groupID}}}
	snapshotCache := &openAISnapshotCacheStub{
		snapshotProviders: []*gatewayprovider.ExecutionProvider{stalePrimary, staleSecondary},
		providersByID:     map[int64]*gatewayprovider.ExecutionProvider{34001: stalePrimary, 34002: staleSecondary},
	}
	snapshotService := schedulercore.NewSnapshotService(snapshotCache, nil, nil, nil, nil)
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: schedulerTestOpenAIProviderRepo{providers: []gatewayprovider.ExecutionProvider{dbPrimary, dbSecondary}},
			Snapshot:  schedulerredis.NewSnapshotReader(snapshotService),
		},
		Shared: Shared{
			Parameters: newAdvancedSchedulerParametersForTest(&config.Config{}, "true"),
			Health:     gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{}),
		},
	}, &config.Config{})

	provider, err := svc.SelectProviderForModelWithExclusions(ctx, &groupID, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, provider)
	require.Equal(t, int64(34002), provider.Record.ID)
}

func TestOpenAIGatewayService_SelectProviderWithLoadAwareness_DBFreshGroupRecheckWaitsOnValidProvider(t *testing.T) {
	ctx := context.Background()
	groupID, otherGroupID := int64(10107), int64(10108)
	stalePrimary := &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 34201, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}}}
	staleBackup := &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 34202, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 10, GroupIDs: []int64{groupID}}}
	dbPrimary := *stalePrimary
	dbPrimary.Record.GroupIDs = []int64{otherGroupID}
	dbBackup := *staleBackup
	snapshotCache := &openAISnapshotCacheStub{
		snapshotProviders: []*gatewayprovider.ExecutionProvider{stalePrimary, staleBackup},
		providersByID:     map[int64]*gatewayprovider.ExecutionProvider{stalePrimary.Record.ID: stalePrimary, staleBackup.Record.ID: staleBackup},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Snapshot: schedulerredis.NewSnapshotReader(schedulercore.NewSnapshotService(snapshotCache,

				nil, nil, nil, nil)),
			Providers: schedulerTestOpenAIProviderRepo{providers: []gatewayprovider.ExecutionProvider{dbPrimary, dbBackup}},
		},

		Shared: Shared{Concurrency: schedulercore.NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{staleBackup.Record.ID: false}}, schedulercore.Diagnostics{Logf: logging.LegacyPrintf, Event: logging.Event})},
	}, cfg)

	selection, err := svc.SelectProviderWithLoadAwareness(ctx, &groupID, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, selection.WaitPlan)
	require.Equal(t, staleBackup.Record.ID, selection.Provider.Record.ID)
	require.Equal(t, staleBackup.Record.ID, selection.WaitPlan.ProviderID)
}

func TestOpenAIGatewayService_RecheckSelectedOpenAIProviderFromDB_KeepsGroupBoundary(t *testing.T) {
	grouped := gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 34301, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{99}}}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: schedulerTestOpenAIProviderRepo{providers: []gatewayprovider.ExecutionProvider{grouped}},
			Snapshot:  schedulerredis.NewSnapshotReader(schedulercore.NewSnapshotService(&openAISnapshotCacheStub{}, nil, nil, nil, nil)),
		},

		Shared: Shared{},
	}, &config.Config{})

	requestedGroupID := int64(100)

	for _, groupID := range []*int64{nil, &requestedGroupID} {
		fresh := svc.recheckSelectedOpenAIProviderFromDB(context.Background(), &grouped, groupID, capability.PlatformOpenAI, "gpt-5.1", false, "")
		require.Nil(t, fresh)
	}

	ungrouped := grouped
	ungrouped.Record.ID++
	ungrouped.Record.GroupIDs = nil
	standardSvc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: schedulerTestOpenAIProviderRepo{providers: []gatewayprovider.ExecutionProvider{grouped, ungrouped}},
			Snapshot: schedulerredis.NewSnapshotReader(
				schedulercore.NewSnapshotService(&openAISnapshotCacheStub{}, nil, nil,
					nil, nil)),
		},
		Shared: Shared{},
	}, &config.Config{})

	require.Nil(t, standardSvc.recheckSelectedOpenAIProviderFromDB(context.Background(), &grouped, nil, capability.PlatformOpenAI, "gpt-5.1", false, ""))
	require.Nil(t, standardSvc.recheckSelectedOpenAIProviderFromDB(context.Background(), &ungrouped, nil, capability.PlatformOpenAI, "gpt-5.1", false, ""))
}

func TestOpenAIGatewayService_GetSchedulableProvider_ExhaustedCodexExtraDoesNotSetRateLimit(t *testing.T) {
	resetAt := time.Now().Add(6 * 24 * time.Hour)
	provider := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 701,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Extra: map[string]any{
				"codex_7d_used_percent": 100.0,
				"codex_7d_reset_at":     resetAt.UTC().Format(time.RFC3339),
			},
		},
	}
	repo := &openAICodexExtraListRepo{selectionProviderFixture: selectionProviderFixture{providers: []gatewayprovider.ExecutionProvider{provider}}, rateLimitCh: make(chan time.Time, 1)}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Providers: repo,
		}, Shared: Shared{},
	},
		nil)

	fresh, err := svc.getSchedulableProvider(context.Background(), provider.Record.ID)
	require.NoError(t, err)
	require.NotNil(t, fresh)
	require.Nil(t, fresh.Record.RateLimitResetAt)
	select {
	case persisted := <-repo.rateLimitCh:
		t.Fatalf("不应将已耗尽的 codex extra 提升为运行时限流状态: %v", persisted)
	case <-time.After(2 * time.Second):
	}
}

func TestOpenAIGatewayService_ListSchedulableProviders_FiltersThresholdBlockedProviders(t *testing.T) {
	settingsRepo := settingstestkit.NewMemory()
	settingsRepo.Data[providercore.SettingKeyProviderSchedulingThresholds] = `{"openai":85}`

	providerRepo := &thresholdSelectionProviderRepoStub{
		providers: []gatewayprovider.ExecutionProvider{
			{
				Record: providercore.Record{
					Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 4101,
					Platform:    capability.PlatformOpenAI,
					Status:      billing.StatusActive,
					Schedulable: true,
					Extra: map[string]any{
						"codex_7d_used_percent": 91.0,
						"codex_7d_reset_at":     time.Now().UTC().Add(12 * time.Hour).Format(time.RFC3339),
					},
				},
			},
			{
				Record: providercore.Record{
					Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 4102,
					Platform:    capability.PlatformOpenAI,
					Status:      billing.StatusActive,
					Schedulable: true,
					Extra: map[string]any{
						"codex_7d_used_percent": 40.0,
						"codex_7d_reset_at":     time.Now().UTC().Add(12 * time.Hour).Format(time.RFC3339),
					},
				},
			},
		},
	}

	healthObserver := gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Store: providerRepo, Readers: gatewaytestkit.RuntimeReaders(settings.New(settingsRepo))})
	svc := newCompatibleSelectionForTest(CompatibleDependencies{Reads: Reads{Providers: providerRepo}, Shared: Shared{Health: healthObserver}}, &config.Config{})

	providers, err := svc.listSchedulableProviders(context.Background(), selectionFixtureGroupID(context.Background()), capability.PlatformOpenAI)

	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Equal(t, int64(4102), providers[0].Record.ID)
	require.Equal(t, 1, providerRepo.TempCalls)
}

func (r groupAwareStubOpenAIProviderRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	var result []gatewayprovider.ExecutionProvider
	for _, acc := range r.providers {
		if acc.Record.Platform == platform && openAIStickyProviderMatchesGroup(&acc, &groupID) {
			result = append(result, acc)
		}
	}
	return result, nil
}

func (r groupAwareStubOpenAIProviderRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	var result []gatewayprovider.ExecutionProvider
	for _, acc := range r.providers {
		if acc.Record.Platform == platform && openAIStickyProviderMatchesGroup(&acc, nil) {
			result = append(result, acc)
		}
	}
	return result, nil
}

func (r *openAICodexExtraListRepo) SetRateLimited(_ context.Context, _ int64, at time.Time) error {
	if r.rateLimitCh != nil {
		r.rateLimitCh <- at
	}
	return nil
}

func (r groupAwareStubOpenAIProviderRepo) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
	var result []gatewayprovider.ExecutionProvider
	for i := range r.providers {
		if slices.Contains(platforms, r.providers[i].Record.Platform) && openAIStickyProviderMatchesGroup(&r.providers[i], &groupID) {
			prepareSelectionFixtureProvider(ctx, &r.providers[i], &groupID)
			result = append(result, r.providers[i])
		}
	}
	return result, nil
}

func (r groupAwareStubOpenAIProviderRepo) GetByID(ctx context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
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

func TestOpenAISelectProviderWithLoadAwareness_HydratesSelectedProviderFromSchedulerSnapshot(t *testing.T) {
	cache := &snapshotHydrationCache{
		snapshot: []*gatewayprovider.ExecutionProvider{
			{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 1,
					Platform:    capability.PlatformOpenAI,
					Type:        capability.ProviderTypeAPIKey,
					Status:      billing.StatusActive,
					Schedulable: true,
					Concurrency: 1,
					Priority:    1,
					Credentials: map[string]any{
						"model_mapping": map[string]any{
							"gpt-4": "gpt-4",
						},
					},
				},
			},
		},
		providers: map[int64]*gatewayprovider.ExecutionProvider{
			1: {
				Record: providercore.Record{
					LoadLocation: time.LoadLocation, ID: 1,
					Platform:    capability.PlatformOpenAI,
					Type:        capability.ProviderTypeAPIKey,
					Status:      billing.StatusActive,
					Schedulable: true,
					Concurrency: 1,
					Priority:    1,
					Credentials: map[string]any{
						"api_key":       "sk-live",
						"model_mapping": map[string]any{"gpt-4": "gpt-4"},
					},
				},
			},
		},
	}

	schedulerSnapshot := newHydrationSnapshotForTest(cache, nil)
	groupID := int64(2)
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Snapshot: schedulerredis.NewSnapshotReader(schedulerSnapshot),
		},
		Shared: Shared{Cache: &responseCacheFixture{}},
	}, nil)

	selection, err := svc.SelectProviderWithLoadAwareness(context.Background(), &groupID, "", "gpt-4", nil)
	if err != nil {
		t.Fatalf("SelectProviderWithLoadAwareness error: %v", err)
	}
	if selection == nil || selection.Provider == nil {
		t.Fatalf("expected selected provider")
	}
	if got := selection.Provider.View().GetOpenAIApiKey(); got != "sk-live" {
		t.Fatalf("expected hydrated api key, got %q", got)
	}
}

func TestOpenAINewAcquiredSelectionResult_ReleasesSlotWhenHydrationFails(t *testing.T) {
	cache := &snapshotHydrationCache{
		providers: map[int64]*gatewayprovider.ExecutionProvider{},
	}
	schedulerSnapshot := newHydrationSnapshotForTest(cache, selectionProviderFixture{})
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{
			Snapshot: schedulerredis.NewSnapshotReader(schedulerSnapshot),
		},
		Shared: Shared{},
	}, nil)

	releaseCalls := 0

	selection, err := svc.newAcquiredSelectionResult(context.Background(), &gatewayprovider.ExecutionProvider{Record: providercore.Record{Credentials: map[string]any{"model_whitelist": []string{"*"}}, LoadLocation: time.LoadLocation, ID: 1001}}, func() {
		releaseCalls++
	})

	if err == nil {
		t.Fatalf("expected hydration error")
	}
	if selection != nil {
		t.Fatalf("expected nil selection on hydration error")
	}
	if releaseCalls != 1 {
		t.Fatalf("expected release to be called once, got %d", releaseCalls)
	}
}
