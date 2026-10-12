package provider

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// updateExtraSpyRepo records snapshot writes to check the shadow-provider guard.
type updateExtraSpyRepo struct {
	*openAI429SnapshotRepo
	updateExtraCalled bool
}

type openAI429SnapshotRepo struct {
	providercore.HealthStore
	rateLimitedID      int64
	updatedExtra       map[string]any
	bulkUpdatedIDs     []int64
	bulkUpdatedPayload providercore.ProviderBulkUpdate
}

// openAI429Store supplies writes for tests that use production health rules and parsers.
type openAI429Store interface {
	providercore.HealthStore
	providercore.SessionWindowStore
	providercore.OpenAIPlanWriter
}

type rateLimit429ProviderRepoStub struct {
	providercore.HealthStore
	rateLimitCalls     int
	lastRateLimitID    int64
	lastRateLimitReset time.Time
}

// TestPersistOpenAI429PlanType_SkipsShadow checks that 429 plan_type observations skip shadows.
// Ordinary providers persist the observed plan in credentials.
func TestPersistOpenAI429PlanType_SkipsShadow(t *testing.T) {
	ctx := context.Background()
	body := []byte(`{"error":{"type":"usage_limit_reached","plan_type":"pro"}}`)
	parentID := int64(1)

	t.Run("shadow_skipped", func(t *testing.T) {
		repo := &openAI429SnapshotRepo{}
		shadow := &providercore.Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{}, ParentProviderID: &parentID}
		providercore.PersistOpenAIObservedPlan(ctx, repo, shadow, openai.ParseUsageLimitPlanType(body), slog.Info, slog.Warn)
		require.Empty(t, shadow.Credentials, "影子不可被写入 plan_type 凭据")
	})

	t.Run("normal_provider_writes", func(t *testing.T) {
		repo := &openAI429SnapshotRepo{}
		normal := &providercore.Record{ID: 9, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{}}
		providercore.PersistOpenAIObservedPlan(ctx, repo, normal, openai.ParseUsageLimitPlanType(body), slog.Info, slog.Warn)
		require.Equal(t, "pro", normal.Credentials["plan_type"], "普通提供商应写入 plan_type(反向对照,证明 body 有效、写路径通)")
	})
}

// TestPersistOpenAICodexSnapshot_SkipsShadow checks that global quota headers skip shadows.
// QueryUsage updates shadow codex_* snapshots from /wham/usage bengalfox data.
func TestPersistOpenAICodexSnapshot_SkipsShadow(t *testing.T) {
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "50")
	parentID := int64(1)

	t.Run("shadow_skipped", func(t *testing.T) {
		spy := &updateExtraSpyRepo{openAI429SnapshotRepo: &openAI429SnapshotRepo{}}
		s := newOpenAI429Observer(spy)
		shadow := &providercore.Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, ParentProviderID: &parentID}
		s.PersistCodexSnapshot(context.Background(), shadow, headers)
		require.False(t, spy.updateExtraCalled, "影子不应写 codex_* 头快照")
	})

	t.Run("normal_provider_writes", func(t *testing.T) {
		spy := &updateExtraSpyRepo{openAI429SnapshotRepo: &openAI429SnapshotRepo{}}
		s := newOpenAI429Observer(spy)
		normal := &providercore.Record{ID: 9, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}
		s.PersistCodexSnapshot(context.Background(), normal, headers)
		require.True(t, spy.updateExtraCalled, "普通提供商应写 codex_* 头快照(反向对照)")
	})
}

func TestHandle429_OpenAIPersistsCodexSnapshotImmediately(t *testing.T) {
	repo := &openAI429SnapshotRepo{}
	svc := newOpenAI429Observer(repo)
	provider := &providercore.Record{ID: 123, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}

	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "100")
	headers.Set("x-codex-primary-reset-after-seconds", "604800")
	headers.Set("x-codex-primary-window-minutes", "10080")
	headers.Set("x-codex-secondary-used-percent", "100")
	headers.Set("x-codex-secondary-reset-after-seconds", "18000")
	headers.Set("x-codex-secondary-window-minutes", "300")

	svc.Observe429(context.Background(), provider, headers, nil)

	if repo.rateLimitedID != provider.ID {
		t.Fatalf("rateLimitedID = %d, want %d", repo.rateLimitedID, provider.ID)
	}
	if len(repo.updatedExtra) == 0 {
		t.Fatal("expected codex snapshot to be persisted on 429")
	}
	if got := repo.updatedExtra["codex_5h_used_percent"]; got != 100.0 {
		t.Fatalf("codex_5h_used_percent = %v, want 100", got)
	}
	if got := repo.updatedExtra["codex_7d_used_percent"]; got != 100.0 {
		t.Fatalf("codex_7d_used_percent = %v, want 100", got)
	}
}

func TestHandle429_OpenAISyncsObservedPlanType(t *testing.T) {
	repo := &openAI429SnapshotRepo{}
	svc := newOpenAI429Observer(repo)
	provider := &providercore.Record{
		ID:          124,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Credentials: map[string]any{"plan_type": "plus"},
	}
	body := []byte(`{"error":{"type":"usage_limit_reached","message":"limit reached","plan_type":"free","resets_at":1777283883}}`)

	svc.Observe429(context.Background(), provider, http.Header{}, body)

	require.Equal(t, []int64{provider.ID}, repo.bulkUpdatedIDs)
	require.Equal(t, "free", repo.bulkUpdatedPayload.Credentials["plan_type"])
	require.Equal(t, "free", provider.Credentials["plan_type"])
	require.Equal(t, provider.ID, repo.rateLimitedID)
}

// TestHandle429_SkipsSparkShadow keeps /responses global x-codex-* observations
// from writing a shadow's rate limit. Spark quotas come from codex_bengalfox in /wham/usage,
// so a global reset must not determine the shadow's cooldown.
func TestHandle429_SkipsSparkShadow(t *testing.T) {
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "100")
	headers.Set("x-codex-primary-reset-after-seconds", "604800")
	headers.Set("x-codex-primary-window-minutes", "10080")
	headers.Set("x-codex-secondary-used-percent", "100")
	headers.Set("x-codex-secondary-reset-after-seconds", "18000")
	headers.Set("x-codex-secondary-window-minutes", "300")

	parentID := int64(900)
	shadowRepo := &openAI429SnapshotRepo{}
	shadowSvc := newOpenAI429Observer(shadowRepo)
	shadow := &providercore.Record{
		ID:               901,
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		ParentProviderID: &parentID,
		QuotaDimension:   providercore.QuotaDimensionSpark,
	}

	shadowSvc.Observe429(context.Background(), shadow, headers, nil)

	require.Zero(t, shadowRepo.rateLimitedID, "spark shadow must not be SetRateLimited from /responses global 429")
	require.Empty(t, shadowRepo.updatedExtra, "spark shadow must not get a codex snapshot from /responses 429")

	// Ordinary OpenAI OAuth providers still use the global 429 window.
	normalRepo := &openAI429SnapshotRepo{}
	normalSvc := newOpenAI429Observer(normalRepo)
	normal := &providercore.Record{ID: 902, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}

	normalSvc.Observe429(context.Background(), normal, headers, nil)

	require.Equal(t, normal.ID, normalRepo.rateLimitedID, "normal OpenAI OAuth provider should still be rate limited")
}

func TestHandle429_FallbackUsesDBSeconds(t *testing.T) {
	providerRepo := &rateLimit429ProviderRepoStub{}
	settingRepo := newCooldownSettingsStore()
	data, err := json.Marshal(providercore.RateLimit429CooldownSettings{Enabled: true, CooldownSeconds: 12})
	require.NoError(t, err)
	settingRepo.data[providercore.SettingKeyRateLimit429CooldownSettings] = string(data)

	settingSvc := providercore.NewRuntimeSettings(settingRepo, errCooldownSettingMissing)
	svc := &RateLimitObserver{Health: providercore.NewHealthService(providerRepo, nil, providercore.HealthOptions{RateLimit429Settings: settingSvc.GetRateLimit429CooldownSettings})}

	provider := &providercore.Record{ID: 42, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}
	before := time.Now()
	svc.Observe429(context.Background(), provider, http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
	after := time.Now()

	require.Equal(t, 1, providerRepo.rateLimitCalls)
	require.Equal(t, int64(42), providerRepo.lastRateLimitID)
	require.True(t, !providerRepo.lastRateLimitReset.Before(before.Add(12*time.Second)) && !providerRepo.lastRateLimitReset.After(after.Add(12*time.Second)))
}

func TestHandle429_FallbackDisabledSkipsLocalMark(t *testing.T) {
	providerRepo := &rateLimit429ProviderRepoStub{}
	settingRepo := newCooldownSettingsStore()
	data, err := json.Marshal(providercore.RateLimit429CooldownSettings{Enabled: false, CooldownSeconds: 12})
	require.NoError(t, err)
	settingRepo.data[providercore.SettingKeyRateLimit429CooldownSettings] = string(data)

	settingSvc := providercore.NewRuntimeSettings(settingRepo, errCooldownSettingMissing)
	svc := &RateLimitObserver{Health: providercore.NewHealthService(providerRepo, nil, providercore.HealthOptions{RateLimit429Settings: settingSvc.GetRateLimit429CooldownSettings})}

	provider := &providercore.Record{ID: 43, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}
	svc.Observe429(context.Background(), provider, http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))

	require.Zero(t, providerRepo.rateLimitCalls)
}

// TestHandle429_AnthropicNoResetTimeUsesFallbackCooldown checks the configured cooldown when reset headers are
// absent.
func TestHandle429_AnthropicNoResetTimeUsesFallbackCooldown(t *testing.T) {
	providerRepo := &rateLimit429ProviderRepoStub{}
	settingRepo := newCooldownSettingsStore()
	data, err := json.Marshal(providercore.RateLimit429CooldownSettings{Enabled: true, CooldownSeconds: 12})
	require.NoError(t, err)
	settingRepo.data[providercore.SettingKeyRateLimit429CooldownSettings] = string(data)

	settingSvc := providercore.NewRuntimeSettings(settingRepo, errCooldownSettingMissing)
	svc := &RateLimitObserver{Health: providercore.NewHealthService(providerRepo, nil, providercore.HealthOptions{RateLimit429Settings: settingSvc.GetRateLimit429CooldownSettings})}

	provider := &providercore.Record{ID: 45, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth}
	before := time.Now()
	svc.Observe429(context.Background(), provider, http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"Extra usage required"}}`))
	after := time.Now()

	require.Equal(t, 1, providerRepo.rateLimitCalls)
	require.Equal(t, int64(45), providerRepo.lastRateLimitID)
	require.True(t, !providerRepo.lastRateLimitReset.Before(before.Add(12*time.Second)) && !providerRepo.lastRateLimitReset.After(after.Add(12*time.Second)))
}

// TestHandle429_AnthropicNoResetTimeFallbackDisabledSkipsMark checks that disabling the cooldown leaves health
// unchanged.
func TestHandle429_AnthropicNoResetTimeFallbackDisabledSkipsMark(t *testing.T) {
	providerRepo := &rateLimit429ProviderRepoStub{}
	settingRepo := newCooldownSettingsStore()
	data, err := json.Marshal(providercore.RateLimit429CooldownSettings{Enabled: false, CooldownSeconds: 12})
	require.NoError(t, err)
	settingRepo.data[providercore.SettingKeyRateLimit429CooldownSettings] = string(data)

	settingSvc := providercore.NewRuntimeSettings(settingRepo, errCooldownSettingMissing)
	svc := &RateLimitObserver{Health: providercore.NewHealthService(providerRepo, nil, providercore.HealthOptions{RateLimit429Settings: settingSvc.GetRateLimit429CooldownSettings})}

	provider := &providercore.Record{ID: 46, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth}
	svc.Observe429(context.Background(), provider, http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"Extra usage required"}}`))

	require.Zero(t, providerRepo.rateLimitCalls)
}

func TestHandle429_FallbackUsesDefaultSecondsWhenSettingServiceMissing(t *testing.T) {
	providerRepo := &rateLimit429ProviderRepoStub{}
	svc := &RateLimitObserver{Health: providercore.NewHealthService(providerRepo, nil, providercore.HealthOptions{})}

	provider := &providercore.Record{ID: 44, Platform: capability.PlatformGemini, Type: capability.ProviderTypeAPIKey}
	before := time.Now()
	svc.Observe429(context.Background(), provider, http.Header{}, []byte(`{"error":{"message":"slow down"}}`))
	after := time.Now()

	require.Equal(t, 1, providerRepo.rateLimitCalls)
	require.Equal(t, int64(44), providerRepo.lastRateLimitID)
	require.True(t, !providerRepo.lastRateLimitReset.Before(before.Add(5*time.Second)) && !providerRepo.lastRateLimitReset.After(after.Add(5*time.Second)))
}

func (r *updateExtraSpyRepo) UpdateExtra(_ context.Context, _ int64, _ map[string]any) error {
	r.updateExtraCalled = true
	return nil
}

func (r *openAI429SnapshotRepo) SetRateLimited(_ context.Context, id int64, _ time.Time) error {
	r.rateLimitedID = id
	return nil
}

func (r *openAI429SnapshotRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.updatedExtra = updates
	return nil
}

func (r *openAI429SnapshotRepo) BulkUpdate(_ context.Context, ids []int64, updates providercore.ProviderBulkUpdate) (int64, error) {
	r.bulkUpdatedIDs = append([]int64(nil), ids...)
	r.bulkUpdatedPayload = updates
	return int64(len(ids)), nil
}

func newOpenAI429Observer(repo openAI429Store) *RateLimitObserver {
	return &RateLimitObserver{Health: providercore.NewHealthService(repo, nil, providercore.HealthOptions{SessionWindows: repo}), Plans: repo}
}

func (*openAI429SnapshotRepo) UpdateSessionWindow(context.Context, int64, *time.Time, *time.Time, string) error {
	panic("unexpected session window")
}

func (r *rateLimit429ProviderRepoStub) SetRateLimited(_ context.Context, id int64, resetAt time.Time) error {
	r.rateLimitCalls++
	r.lastRateLimitID = id
	r.lastRateLimitReset = resetAt
	return nil
}
