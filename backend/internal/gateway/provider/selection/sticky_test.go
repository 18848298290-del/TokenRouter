package selection

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
)

func guardianAffinityTestContext(t *testing.T, model, subagent, parentHeader, metadata string) context.Context {
	t.Helper()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Request.Header.Set(clientmeta.OpenAISubagentHeader, subagent)
	if parentHeader != "" {
		c.Request.Header.Set(clientmeta.CodexParentThreadIDHeader, parentHeader)
	}
	if metadata != "" {
		c.Request.Header.Set(clientmeta.CodexTurnMetadataHeader, metadata)
	}
	return gatewayhttp.WithOpenAIGuardianParentAffinity(context.Background(), c, nil, model)
}

func TestOpenAIProviderSchedulerGuardianAffinitySelectsParent(t *testing.T) {
	parentID := "22222222-2222-4222-8222-222222222222"
	parentHash, _ := schedulercore.DeriveSessionHashes(parentID)
	groupID := int64(102001)
	providers := []gatewayprovider.ExecutionProvider{
		{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 39001, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 10, GroupIDs: []int64{groupID}, Credentials: map[string]any{"model_whitelist": []string{"*"}, "access_token": "parent", "plan_type": "team"}}},
		{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 39002, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}, Credentials: map[string]any{"model_whitelist": []string{"*"}, "access_token": "fallback", "plan_type": "team"}}},
	}
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:" + parentHash: 39001}, deletedSessions: map[string]int{}}
	svc := newCompatibleSelectionForTest(CompatibleDependencies{
		Reads: Reads{Providers: schedulerGroupAwareOpenAIProviderRepo{schedulerTestOpenAIProviderRepo{providers: providers}}},
		Shared: Shared{
			Cache: cache,
			Concurrency: schedulercore.NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{39001: true, 39002: true}}, schedulercore.Diagnostics{
				Logf:  logging.LegacyPrintf,
				Event: logging.Event,
			}),
		},
	}, newSchedulerTestOpenAIWSV2Config())

	ctx := guardianAffinityTestContext(t, clientmeta.CodexAutoReviewModel, "guardian", parentID, "")
	ctx = withAdvancedSchedulerTestGroup(ctx, groupID)

	selection, decision, err := svc.SelectProviderWithScheduler(ctx, &groupID, "", "child-session", clientmeta.CodexAutoReviewModel, nil, egress.OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, int64(39001), selection.Provider.Record.ID)
	require.Equal(t, openAIProviderScheduleLayerGuardianParent, decision.Layer)
	require.Zero(t, cache.deletedSessions["openai:"+parentHash])
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}

	// A child request must leave the parent's binding intact.
	require.NoError(t, svc.BindStickySession(ctx, &groupID, parentHash, 39002))
	require.Equal(t, int64(39001), cache.sessionBindings["openai:"+parentHash])
}

func TestGetStickySessionProviderID_FallbackToLegacyKey(t *testing.T) {
	stats := &schedulercore.StickyStats{}
	beforeFallbackTotal, beforeFallbackHit, _ := stats.Snapshot()

	cache := &stickyCacheFixture{
		sessionBindings: map[string]int64{
			"openai:legacy-hash": 42,
		},
	}
	svc := NewCompatible(CompatibleDependencies{Shared: Shared{
		Cache: cache,
	}, StickyStats: stats}, Options{StickyTTL: time.Hour, ReadLegacySticky: true, WriteLegacySticky: false})

	ctx := requeststate.WithOpenAILegacySessionHash(context.Background(), "legacy-hash")

	providerID, err := svc.getStickySessionProviderID(ctx, nil, "new-hash")
	require.NoError(t, err)
	require.Equal(t, int64(42), providerID)

	afterFallbackTotal, afterFallbackHit, _ := stats.Snapshot()
	require.Equal(t, beforeFallbackTotal+1, afterFallbackTotal)
	require.Equal(t, beforeFallbackHit+1, afterFallbackHit)
}

func TestSetStickySessionProviderID_DualWriteOldEnabled(t *testing.T) {
	stats := &schedulercore.StickyStats{}
	_, _, beforeDualWriteTotal := stats.Snapshot()

	cache := &stickyCacheFixture{sessionBindings: map[string]int64{}}
	svc := NewCompatible(CompatibleDependencies{Shared: Shared{
		Cache: cache,
	}, StickyStats: stats}, Options{StickyTTL: time.Hour, ReadLegacySticky: false, WriteLegacySticky: true})

	ctx := requeststate.WithOpenAILegacySessionHash(context.Background(), "legacy-hash")

	err := svc.setStickySessionProviderID(ctx, nil, "new-hash", 9, openaiStickySessionTTL)
	require.NoError(t, err)
	require.Equal(t, int64(9), cache.sessionBindings["openai:new-hash"])
	require.Equal(t, int64(9), cache.sessionBindings["openai:legacy-hash"])

	_, _, afterDualWriteTotal := stats.Snapshot()
	require.Equal(t, beforeDualWriteTotal+1, afterDualWriteTotal)
}

func TestSetStickySessionProviderID_DualWriteOldDisabled(t *testing.T) {
	cache := &stickyCacheFixture{sessionBindings: map[string]int64{}}
	svc := NewCompatible(CompatibleDependencies{Shared: Shared{
		Cache: cache,
	}, StickyStats: nil}, Options{StickyTTL: time.Hour, ReadLegacySticky: false, WriteLegacySticky: false})

	ctx := requeststate.WithOpenAILegacySessionHash(context.Background(), "legacy-hash")
	err := svc.setStickySessionProviderID(ctx, nil, "new-hash", 9, openaiStickySessionTTL)
	require.NoError(t, err)
	require.Equal(t, int64(9), cache.sessionBindings["openai:new-hash"])
	_, exists := cache.sessionBindings["openai:legacy-hash"]
	require.False(t, exists)
}
