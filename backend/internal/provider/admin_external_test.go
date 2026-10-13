package provider_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// openAICodexExtraListRepo observes possible rate-limit writes during ordinary
// provider listing.
type openAICodexExtraListRepo struct {
	codexListRecordsFixture // provider list record fixture
	// rateLimitCh receives possible rate-limit writes during listing. Nil disables
	// recording; the test supplies buffered storage for the observation window.
	rateLimitCh chan time.Time
}

// codexListRecordsFixture supplies retained provider records for paginated
// administrator list tests.
type codexListRecordsFixture struct {
	provider.AdminStore // unused administrative store methods
	// providers is returned as the fixture's retained slice. Listing tests inspect
	// its records and watch the separate write channel for persisted side effects.
	providers []provider.Record
}

// SetRateLimited sends the supplied deadline to the configured observation
// channel. A nil channel skips observation; tests provision a buffer so the
// write can be detected without blocking its caller.
func (r *openAICodexExtraListRepo) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	if r.rateLimitCh != nil {
		r.rateLimitCh <- resetAt
	}
	return nil
}

// ListWithFilters returns the retained provider slice and pagination derived
// from the request. It ignores filters and sorting, so the list test focuses on
// whether administration introduces a rate-limit write.
func (r *openAICodexExtraListRepo) ListWithFilters(_ context.Context, params pagination.PaginationParams, platform, providerType, status, search string, groupID int64, privacyMode string) ([]provider.Record, *pagination.PaginationResult, error) {
	_ = platform
	_ = providerType
	_ = status
	_ = search
	_ = groupID
	_ = privacyMode
	return r.providers, &pagination.PaginationResult{Total: int64(len(r.providers)), Page: params.Page, PageSize: params.PageSize}, nil
}

// TestAdminService_ListProviders_ExhaustedCodexExtraDoesNotSetRateLimit lists a
// provider with exhausted saved Codex weekly usage. The returned record keeps
// no rate-limit deadline, and no rate-limit write reaches the fixture channel
// during the observation window.
func TestAdminService_ListProviders_ExhaustedCodexExtraDoesNotSetRateLimit(t *testing.T) {
	resetAt := time.Now().Add(4 * 24 * time.Hour)
	repo := &openAICodexExtraListRepo{
		codexListRecordsFixture: codexListRecordsFixture{providers: []provider.Record{{
			ID:          702,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Extra: map[string]any{
				"codex_7d_used_percent": 100.0,
				"codex_7d_reset_at":     resetAt.UTC().Format(time.RFC3339),
			},
		}}},
		rateLimitCh: make(chan time.Time, 1),
	}
	svc := newProviderEditorForTest(repo)

	providers, total, err := svc.ListProviders(context.Background(), 1, 20, capability.PlatformOpenAI, capability.ProviderTypeOAuth, "", "", 0, "", "", "")
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, providers, 1)
	require.Nil(t, providers[0].RateLimitResetAt)
	// Observe possible background writes during the bounded deadline used by
	// this scenario.
	select {
	case persisted := <-repo.rateLimitCh:
		t.Fatalf("不应在提供商列表查询时将 codex extra 持久化为运行时限流状态: %v", persisted)
	case <-time.After(2 * time.Second):
	}
}

// TestResetProviderQuota_RejectsShadow creates a parent and its Spark shadow
// before requesting quota resets. The shadow reset returns HTTP 400, while
// resetting the parent passes its credential-ownership check.
func TestResetProviderQuota_RejectsShadow(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	svc := newProviderEditorForTest(repo)
	parent := &provider.Record{
		Name: "rq-parent", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive,
		Credentials: map[string]any{"chatgpt_account_id": "o"},
	}
	require.NoError(t, repo.Create(ctx, parent))
	shadow, err := svc.CreateShadow(ctx, parent.ID, provider.ShadowOptions{Name: "rq-shadow"})
	require.NoError(t, err)

	err = svc.ResetProviderQuota(ctx, shadow.ID)
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err), "影子 reset-quota 应 400")

	require.NoError(t, svc.ResetProviderQuota(ctx, parent.ID), "母提供商 reset-quota 应放行")
}

// TestDeleteProvider_CascadeToShadow deletes a parent after creating its Spark
// shadow. Both provider identifiers disappear from the fixture record map when
// administrative cascade deletion completes successfully.
func TestDeleteProvider_CascadeToShadow(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	svc := newProviderEditorForTest(repo)

	parent := &provider.Record{
		Name:        "cascade-parent",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Credentials: map[string]any{"chatgpt_account_id": "org-cascade"},
	}
	require.NoError(t, repo.Create(ctx, parent))

	shadow, err := svc.CreateShadow(ctx, parent.ID, provider.ShadowOptions{Name: "cascade-shadow"})
	require.NoError(t, err)
	shadowID := shadow.ID

	_, ok := repo.providers[parent.ID]
	require.True(t, ok)
	_, ok = repo.providers[shadowID]
	require.True(t, ok)

	require.NoError(t, svc.DeleteProvider(ctx, parent.ID))

	_, ok = repo.providers[parent.ID]
	require.False(t, ok, "parent provider should be deleted")

	_, ok = repo.providers[shadowID]
	require.False(t, ok, "shadow provider should be cascade-deleted")
}
