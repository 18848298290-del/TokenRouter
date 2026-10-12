package selection

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
	schedulerredis "github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache/codec"
)

// selectionSnapshotFixture supplies cached provider copies separately from
// database revalidation targets.
type selectionSnapshotFixture struct {
	schedulercore.SnapshotCache                                              // unused snapshot calls panic
	providersByID               map[int64]*gatewayprovider.ExecutionProvider // cached continuation target fixtures
}

// selectPreviousResponseForTest supplies candidate and group-privacy hints
// before continuation lookup. It applies one group mapping to the client's
// name, then passes the resolved routing model and Compact requirement to
// response selection.
func selectPreviousResponseForTest(s *Compatible, ctx context.Context, group *int64, previous, model string, excluded map[int64]struct{}, compact bool) (*gatewayprovider.SelectionResult, error) {
	ctx = s.withCandidatePolicy(ctx, group, "")
	ctx = s.withOpenAIGroupPrivacyRequirement(ctx, group)
	model = s.resolveGroupRoutingModel(ctx, group, model)
	return s.selectProviderByPreviousResponseIDForCapability(ctx, group, previous, model, excluded, "", compact)
}

// GetProvider returns a shallow copy of the configured provider record wrapped
// for snapshot decoding. Missing IDs return nil without error; referenced
// record data stays shared with the fixture's input.
func (s *selectionSnapshotFixture) GetProvider(_ context.Context, id int64) (schedulercore.SnapshotProvider, error) {
	value := s.providersByID[id]
	if value == nil {
		return nil, nil
	}
	copy := value.Record
	return codec.WrapRecord(&copy), nil
}

// TestOpenAIGatewayService_SelectProviderByPreviousResponseID_Hit binds an
// eligible API Key provider to a prior response. Continuation acquires its slot
// and returns the same provider, protecting response affinity during selection.
func TestOpenAIGatewayService_SelectProviderByPreviousResponseID_Hit(t *testing.T) {
	ctx := context.Background()
	groupID := int64(23)
	provider := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials:  map[string]any{"model_whitelist": []string{"*"}},
			LoadLocation: time.LoadLocation, ID: 2,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 2,
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
	}
	cache := &responseCacheFixture{}
	store := session.NewOpenAIWSStateStore(cache, gatewayprovider.LogOpenAIWSModeInfo)
	cfg := responseSelectionOptions()

	svc := NewCompatible(CompatibleDependencies{
		Reads: Reads{Providers: selectionProviderFixture{providers: []gatewayprovider.ExecutionProvider{provider}}},
		Shared: Shared{
			Cache: cache,
			Concurrency: schedulercore.NewConcurrencyService(selectionConcurrencyFixture{}, schedulercore.Diagnostics{
				Logf: logging.LegacyPrintf,

				Event: logging.Event,
			},
			),
			Parameters: responseSelectionParameters(),
		},
		Responses: store,
	}, cfg)

	require.NoError(t, store.BindResponseProvider(ctx, groupID, "resp_prev_1", provider.Record.ID, time.Hour))

	selection, err := selectPreviousResponseForTest(svc, ctx, &groupID, "resp_prev_1", "gpt-5.1", nil, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Provider)
	require.Equal(t, provider.Record.ID, selection.Provider.Record.ID)
	require.True(t, selection.Acquired)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

// TestOpenAIGatewayService_SelectProviderByPreviousResponseID_QuotaAutoPausedMiss
// exceeds a configured quota threshold on the response-bound provider.
// Selection returns no target and retains the binding so a later eligible
// continuation can resume.
func TestOpenAIGatewayService_SelectProviderByPreviousResponseID_QuotaAutoPausedMiss(t *testing.T) {
	ctx := context.Background()
	groupID := int64(23)
	provider := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials:  map[string]any{"model_whitelist": []string{"*"}},
			LoadLocation: time.LoadLocation, ID: 77,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 2,
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
				"codex_5h_used_percent":                         96.0,
				"auto_pause_5h_threshold":                       0.95,
			},
		},
	}
	cache := &responseCacheFixture{}
	store := session.NewOpenAIWSStateStore(cache, gatewayprovider.LogOpenAIWSModeInfo)
	cfg := responseSelectionOptions()
	svc := NewCompatible(CompatibleDependencies{
		Reads: Reads{Providers: selectionProviderFixture{providers: []gatewayprovider.ExecutionProvider{provider}}},
		Shared: Shared{
			Cache: cache,
			Concurrency: schedulercore.NewConcurrencyService(selectionConcurrencyFixture{}, schedulercore.Diagnostics{
				Logf: logging.LegacyPrintf,

				Event: logging.Event,
			},
			),
			Parameters: responseSelectionParameters(),
		},
		Responses: store,
	}, cfg)

	require.NoError(t, store.BindResponseProvider(ctx, groupID, "resp_prev_quota", provider.Record.ID, time.Hour))

	selection, err := selectPreviousResponseForTest(svc, ctx, &groupID, "resp_prev_quota", "gpt-5.1", nil, false)
	require.NoError(t, err)
	require.Nil(t, selection, "超过 5h 配额阈值的提供商不应继续命中 previous_response_id 粘连")

	// Auto-pause is transient, so the binding is preserved: the chain can resume on the
	// same provider once the quota window resets.
	boundProviderID, getErr := store.GetResponseProvider(ctx, groupID, "resp_prev_quota")
	require.NoError(t, getErr)
	require.Equal(t, provider.Record.ID, boundProviderID)
}

// TestOpenAIGatewayService_SelectProviderByPreviousResponseID_RateLimitedMiss
// gives the response-bound provider an active global cooldown. Continuation
// rejects it and removes the binding, protecting stale-affinity cleanup for an
// unschedulable target.
func TestOpenAIGatewayService_SelectProviderByPreviousResponseID_RateLimitedMiss(t *testing.T) {
	ctx := context.Background()
	groupID := int64(23)
	rateLimitedUntil := time.Now().Add(30 * time.Minute)
	provider := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials:  map[string]any{"model_whitelist": []string{"*"}},
			LoadLocation: time.LoadLocation, ID: 12,
			Platform:         capability.PlatformOpenAI,
			Type:             capability.ProviderTypeAPIKey,
			Status:           billing.StatusActive,
			Schedulable:      true,
			Concurrency:      1,
			RateLimitResetAt: &rateLimitedUntil,
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
	}
	cache := &responseCacheFixture{}
	store := session.NewOpenAIWSStateStore(cache, gatewayprovider.LogOpenAIWSModeInfo)
	cfg := responseSelectionOptions()
	svc := NewCompatible(CompatibleDependencies{
		Reads: Reads{Providers: selectionProviderFixture{providers: []gatewayprovider.ExecutionProvider{provider}}},
		Shared: Shared{
			Cache: cache,
			Concurrency: schedulercore.NewConcurrencyService(selectionConcurrencyFixture{}, schedulercore.Diagnostics{
				Logf: logging.LegacyPrintf,

				Event: logging.Event,
			},
			),
			Parameters: responseSelectionParameters(),
		},
		Responses: store,
	}, cfg)

	require.NoError(t, store.BindResponseProvider(ctx, groupID, "resp_prev_rl", provider.Record.ID, time.Hour))

	selection, err := selectPreviousResponseForTest(svc, ctx, &groupID, "resp_prev_rl", "gpt-5.1", nil, false)
	require.NoError(t, err)
	require.Nil(t, selection, "限额中的提供商不应继续命中 previous_response_id 粘连")
	boundProviderID, getErr := store.GetResponseProvider(ctx, groupID, "resp_prev_rl")
	require.NoError(t, getErr)
	require.Zero(t, boundProviderID)
}

// TestOpenAIGatewayService_SelectProviderByPreviousResponseID_DBRuntimeRecheckRateLimitedMiss
// leaves a usable snapshot while the database record is rate limited.
// Revalidation rejects the target and deletes its prior-response binding before
// continuation dispatch.
func TestOpenAIGatewayService_SelectProviderByPreviousResponseID_DBRuntimeRecheckRateLimitedMiss(t *testing.T) {
	ctx := context.Background()
	groupID := int64(24)
	rateLimitedUntil := time.Now().Add(30 * time.Minute)
	staleProvider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials:  map[string]any{"model_whitelist": []string{"*"}},
			LoadLocation: time.LoadLocation, ID: 13,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
	}
	dbProvider := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials:  map[string]any{"model_whitelist": []string{"*"}},
			LoadLocation: time.LoadLocation, ID: 13,
			Platform:         capability.PlatformOpenAI,
			Type:             capability.ProviderTypeAPIKey,
			Status:           billing.StatusActive,
			Schedulable:      true,
			Concurrency:      1,
			RateLimitResetAt: &rateLimitedUntil,
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
	}
	cache := &responseCacheFixture{}
	store := session.NewOpenAIWSStateStore(cache, gatewayprovider.LogOpenAIWSModeInfo)
	cfg := responseSelectionOptions()
	snapshotCache := &selectionSnapshotFixture{
		providersByID: map[int64]*gatewayprovider.ExecutionProvider{dbProvider.Record.ID: staleProvider},
	}
	svc := NewCompatible(CompatibleDependencies{
		Reads: Reads{
			Providers: selectionProviderFixture{providers: []gatewayprovider.ExecutionProvider{dbProvider}},
			Snapshot:  schedulerredis.NewSnapshotReader(schedulercore.NewSnapshotService(snapshotCache, nil, nil, nil, nil)),
		},
		Shared: Shared{
			Cache: cache,
			Concurrency: schedulercore.NewConcurrencyService(selectionConcurrencyFixture{}, schedulercore.Diagnostics{
				Logf: logging.LegacyPrintf,

				Event: logging.Event,
			},
			),
			Parameters: responseSelectionParameters(),
		},
		Responses: store,
	}, cfg)

	require.NoError(t, store.BindResponseProvider(ctx, groupID, "resp_prev_db_rl", dbProvider.Record.ID, time.Hour))

	selection, err := selectPreviousResponseForTest(svc, ctx, &groupID, "resp_prev_db_rl", "gpt-5.1", nil, false)
	require.NoError(t, err)
	require.Nil(t, selection, "DB 中已限流的提供商不应继续命中 previous_response_id 粘连")
	boundProviderID, getErr := store.GetResponseProvider(ctx, groupID, "resp_prev_db_rl")
	require.NoError(t, getErr)
	require.Zero(t, boundProviderID)
}

// TestOpenAIGatewayService_SelectProviderByPreviousResponseID_Excluded places
// the response-bound provider in the current request's exclusion set. Selection
// returns no target, protecting request exclusions before continuation slot
// acquisition.
func TestOpenAIGatewayService_SelectProviderByPreviousResponseID_Excluded(t *testing.T) {
	ctx := context.Background()
	groupID := int64(23)
	provider := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials:  map[string]any{"model_whitelist": []string{"*"}},
			LoadLocation: time.LoadLocation, ID: 8,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
	}
	cache := &responseCacheFixture{}
	store := session.NewOpenAIWSStateStore(cache, gatewayprovider.LogOpenAIWSModeInfo)
	cfg := responseSelectionOptions()
	svc := NewCompatible(CompatibleDependencies{
		Reads: Reads{Providers: selectionProviderFixture{providers: []gatewayprovider.ExecutionProvider{provider}}},
		Shared: Shared{
			Cache: cache,
			Concurrency: schedulercore.NewConcurrencyService(selectionConcurrencyFixture{}, schedulercore.Diagnostics{
				Logf: logging.LegacyPrintf,

				Event: logging.Event,
			},
			),
			Parameters: responseSelectionParameters(),
		},
		Responses: store,
	}, cfg)

	require.NoError(t, store.BindResponseProvider(ctx, groupID, "resp_prev_2", provider.Record.ID, time.Hour))

	selection, err := selectPreviousResponseForTest(svc, ctx, &groupID, "resp_prev_2", "gpt-5.1", map[int64]struct{}{provider.Record.ID: {}}, false)
	require.NoError(t, err)
	require.Nil(t, selection)
}

// TestOpenAIGatewayService_SelectProviderByPreviousResponseID_APIKeyForceHTTPHit
// binds an API Key target configured to force HTTP. Continuation still chooses
// that key/project, protecting response state created under its API
// credentials.
func TestOpenAIGatewayService_SelectProviderByPreviousResponseID_APIKeyForceHTTPHit(t *testing.T) {
	ctx := context.Background()
	groupID := int64(23)
	provider := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials:  map[string]any{"model_whitelist": []string{"*"}},
			LoadLocation: time.LoadLocation, ID: 11,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Extra: map[string]any{
				"openai_ws_force_http":            true,
				"responses_websockets_v2_enabled": true,
			},
		},
	}
	cache := &responseCacheFixture{}
	store := session.NewOpenAIWSStateStore(cache, gatewayprovider.LogOpenAIWSModeInfo)
	cfg := responseSelectionOptions()
	svc := NewCompatible(CompatibleDependencies{
		Reads: Reads{Providers: selectionProviderFixture{providers: []gatewayprovider.ExecutionProvider{provider}}},
		Shared: Shared{
			Cache: cache,
			Concurrency: schedulercore.NewConcurrencyService(selectionConcurrencyFixture{}, schedulercore.Diagnostics{
				Logf: logging.LegacyPrintf,

				Event: logging.Event,
			},
			),
			Parameters: responseSelectionParameters(),
		},
		Responses: store,
	}, cfg)

	require.NoError(t, store.BindResponseProvider(ctx, groupID, "resp_prev_force_http", provider.Record.ID, time.Hour))

	selection, err := selectPreviousResponseForTest(svc, ctx, &groupID, "resp_prev_force_http", "gpt-5.1", nil, false)
	require.NoError(t, err)
	require.NotNil(t, selection, "API-key HTTP continuation must retain the key/project that created the response")
	require.NotNil(t, selection.Provider)
	require.Equal(t, provider.Record.ID, selection.Provider.Record.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

// TestOpenAIGatewayService_SelectProviderByPreviousResponseID_OAuthHTTPOnlyCannotResumeWS
// supplies an OAuth target forced to HTTP with a prior-response binding. The
// target is rejected, protecting WebSocket continuation requirements for that
// authentication path.
func TestOpenAIGatewayService_SelectProviderByPreviousResponseID_OAuthHTTPOnlyCannotResumeWS(t *testing.T) {
	ctx := context.Background()
	groupID := int64(23)
	provider := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			Credentials:  map[string]any{"model_whitelist": []string{"*"}, "upstream_protocols": []string{"openai_responses"}},
			LoadLocation: time.LoadLocation, ID: 12,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Extra: map[string]any{
				"openai_ws_force_http":            true,
				"responses_websockets_v2_enabled": true,
			},
		},
	}
	cache := &responseCacheFixture{}
	store := session.NewOpenAIWSStateStore(cache, gatewayprovider.LogOpenAIWSModeInfo)
	svc := NewCompatible(CompatibleDependencies{
		Reads: Reads{Providers: selectionProviderFixture{providers: []gatewayprovider.ExecutionProvider{provider}}},
		Shared: Shared{
			Cache: cache,
			Concurrency: schedulercore.NewConcurrencyService(selectionConcurrencyFixture{}, schedulercore.Diagnostics{
				Logf: logging.LegacyPrintf,

				Event: logging.Event,
			},
			),
			Parameters: responseSelectionParameters(),
		},
		Responses: store,
	}, responseSelectionOptions())

	require.NoError(t, store.BindResponseProvider(ctx, groupID, "resp_prev_oauth_force_http", provider.Record.ID, time.Hour))

	selection, err := selectPreviousResponseForTest(svc, ctx, &groupID, "resp_prev_oauth_force_http", "gpt-5.1", nil, false)
	require.NoError(t, err)
	require.Nil(t, selection, "OAuth HTTP fallback cannot preserve WSv2 continuation state")
}

// TestOpenAIGatewayService_SelectProviderByPreviousResponseID_BusyKeepsSticky
// fills the bound provider while another target has capacity. Selection returns
// a wait plan for the bound provider, protecting continuation affinity during
// temporary slot exhaustion.
func TestOpenAIGatewayService_SelectProviderByPreviousResponseID_BusyKeepsSticky(t *testing.T) {
	ctx := context.Background()
	groupID := int64(23)
	providers := []gatewayprovider.ExecutionProvider{
		{
			Record: providercore.Record{
				Credentials:  map[string]any{"model_whitelist": []string{"*"}},
				LoadLocation: time.LoadLocation, ID: 21,
				Platform:    capability.PlatformOpenAI,
				Type:        capability.ProviderTypeAPIKey,
				Status:      billing.StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Priority:    0,
				Extra: map[string]any{
					"openai_apikey_responses_websockets_v2_enabled": true,
				},
			},
		},
		{
			Record: providercore.Record{
				Credentials:  map[string]any{"model_whitelist": []string{"*"}},
				LoadLocation: time.LoadLocation, ID: 22,
				Platform:    capability.PlatformOpenAI,
				Type:        capability.ProviderTypeAPIKey,
				Status:      billing.StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Priority:    9,
				Extra: map[string]any{
					"openai_apikey_responses_websockets_v2_enabled": true,
				},
			},
		},
	}

	cache := &responseCacheFixture{}
	store := session.NewOpenAIWSStateStore(cache, gatewayprovider.LogOpenAIWSModeInfo)
	cfg := responseSelectionOptions()
	cfg.Scheduling.StickySessionMaxWaiting = 2
	cfg.Scheduling.StickySessionWaitTimeout = 30 * time.Second

	concurrencyCache := selectionConcurrencyFixture{
		acquireResults: map[int64]bool{
			21: false, // The response-bound provider is busy.
			22: true,  // The alternative is available if selection incorrectly switches providers.
		},
		waitCounts: map[int64]int{
			21: 999,
		},
	}

	svc := NewCompatible(CompatibleDependencies{
		Reads: Reads{Providers: selectionProviderFixture{providers: providers}},
		Shared: Shared{
			Cache: cache,
			Concurrency: schedulercore.NewConcurrencyService(concurrencyCache, schedulercore.Diagnostics{
				Logf: logging.LegacyPrintf,

				Event: logging.Event,
			},
			),
			Parameters: responseSelectionParameters(),
		},
		Responses: store,
	}, cfg)

	require.NoError(t, store.BindResponseProvider(ctx, groupID, "resp_prev_busy", 21, time.Hour))

	selection, err := selectPreviousResponseForTest(svc, ctx, &groupID, "resp_prev_busy", "gpt-5.1", nil, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Provider)
	require.Equal(t, int64(21), selection.Provider.Record.ID, "busy previous_response sticky provider should remain selected")
	require.False(t, selection.Acquired)
	require.NotNil(t, selection.WaitPlan)
	require.Equal(t, int64(21), selection.WaitPlan.ProviderID)
}

// TestOpenAIGatewayService_SelectProviderByPreviousResponseID_CapabilityMismatchKeepsSticky
// requests embeddings from a response-bound text target. Selection returns no
// target while retaining the binding, protecting affinity from request-specific
// endpoint capability rejection.
func TestOpenAIGatewayService_SelectProviderByPreviousResponseID_CapabilityMismatchKeepsSticky(t *testing.T) {
	ctx := context.Background()
	groupID := int64(25)
	provider := gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 31,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{
				"model_whitelist":              []string{"*"},
				"openai_workload_capabilities": []any{"text_generation"},
			},
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
	}
	cache := &responseCacheFixture{}
	store := session.NewOpenAIWSStateStore(cache, gatewayprovider.LogOpenAIWSModeInfo)
	cfg := responseSelectionOptions()
	svc := NewCompatible(CompatibleDependencies{
		Reads: Reads{Providers: selectionProviderFixture{providers: []gatewayprovider.ExecutionProvider{provider}}},
		Shared: Shared{
			Cache: cache,
			Concurrency: schedulercore.NewConcurrencyService(selectionConcurrencyFixture{}, schedulercore.Diagnostics{
				Logf: logging.LegacyPrintf,

				Event: logging.Event,
			},
			),
			Parameters: responseSelectionParameters(),
		},
		Responses: store,
	}, cfg)

	require.NoError(t, store.BindResponseProvider(ctx, groupID, "resp_prev_capability", provider.Record.ID, time.Hour))

	selection, err := svc.selectProviderByPreviousResponseIDForCapability(
		ctx,
		&groupID,
		"resp_prev_capability",
		"text-embedding-3-small",
		nil,
		providercore.OpenAIEndpointCapabilityEmbeddings,
		false,
	)
	require.NoError(t, err)
	require.Nil(t, selection)
	boundProviderID, getErr := store.GetResponseProvider(ctx, groupID, "resp_prev_capability")
	require.NoError(t, getErr)
	require.Equal(t, provider.Record.ID, boundProviderID)
}
