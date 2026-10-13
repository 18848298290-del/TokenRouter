package provider_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
)

// duplicateProviderRepoStub records group-aware creation and operation-key
// recovery for duplicate tests.
type duplicateProviderRepoStub struct {
	*sparkShadowRepoStub // underlying shadow record fixture
	// atomicCreateErr stops group-aware fixture creation before any write. The
	// failure test uses it to assert that no duplicate record is added.
	atomicCreateErr  error
	providerGroupsOf map[int64][]providercore.GroupMembership // saved duplicate membership rows
}

// newDuplicateProviderRepoStub creates empty provider and membership indexes
// for duplicate fixtures.
func newDuplicateProviderRepoStub() *duplicateProviderRepoStub {
	return &duplicateProviderRepoStub{
		sparkShadowRepoStub: newSparkShadowRepoStub(),
		providerGroupsOf:    make(map[int64][]providercore.GroupMembership),
	}
}

// CreateWithProviderGroups simulates group-aware fixture creation after its
// injected failure check. It derives group IDs, creates the provider, then
// records memberships carrying the assigned provider identity.
//
// Membership rows are copied as values before their provider ID is filled. The
// final stored record is a shallow value copy retaining the created provider's
// referenced fields, and both fixture record indexes point to that copy. This
// path supplies controlled bookkeeping for tests and performs no database
// transaction.
func (s *duplicateProviderRepoStub) CreateWithProviderGroups(ctx context.Context, provider *providercore.Record, groups []providercore.GroupMembership) error {
	if s.atomicCreateErr != nil {
		return s.atomicCreateErr
	}
	groupIDs := make([]int64, 0, len(groups))
	for _, group := range groups {
		groupIDs = append(groupIDs, group.GroupID)
	}
	provider.GroupIDs = groupIDs
	if err := s.Create(ctx, provider); err != nil {
		return err
	}
	// Creation has assigned the duplicate ID. Fill it into copied membership
	// rows before retaining them so fixture group assertions refer to this
	// duplicate's identity.
	clonedGroups := make([]providercore.GroupMembership, len(groups))
	copy(clonedGroups, groups)
	for i := range clonedGroups {
		clonedGroups[i].ProviderID = provider.ID
	}
	provider.ProviderGroups = clonedGroups
	s.providerGroupsOf[provider.ID] = clonedGroups
	if len(groupIDs) > 0 {
		s.groupsOf[provider.ID] = append([]int64(nil), groupIDs...)
	}
	// Install one completed provider value in both fixture indexes after
	// membership bookkeeping so direct and batch reads observe the same created
	// duplicate record.
	stored := *provider
	s.providers[provider.ID] = &stored
	s.providersByID[provider.ID] = &stored
	return nil
}

// FindByExtraField returns fixture matches for a string-valued Extra key. Other
// value types produce no match; returned provider values retain referenced
// fields, and match order follows fixture map iteration.
func (s *duplicateProviderRepoStub) FindByExtraField(_ context.Context, key string, value any) ([]providercore.Record, error) {
	wanted, ok := value.(string)
	if !ok {
		return nil, nil
	}
	var matches []providercore.Record
	for _, provider := range s.providers {
		if actual, ok := provider.Extra[key].(string); ok && actual == wanted {
			matches = append(matches, *provider)
		}
	}
	return matches, nil
}

// TestDuplicateProviderCopiesConfigurationAndResetsRuntimeState duplicates
// static credentials carrying configuration and historical runtime state. The
// copy retains groups and configured values, starts unschedulable with clean
// observations, and isolates tested nested JSON containers from the source.
func TestDuplicateProviderCopiesConfigurationAndResetsRuntimeState(t *testing.T) {
	ctx := context.Background()
	repo := newDuplicateProviderRepoStub()
	svc := newProviderEditorForTest(repo)

	notes := "keep this note"
	proxyID := int64(17)
	originalProxyID := int64(11)
	rateMultiplier := 1.25
	loadFactor := 9
	expiresAt := time.Date(2027, time.March, 4, 5, 6, 7, 0, time.UTC)
	rateLimitedAt := time.Now().Add(-time.Minute)
	rateLimitResetAt := time.Now().Add(time.Hour)
	overloadUntil := time.Now().Add(2 * time.Hour)
	tempUnschedulableUntil := time.Now().Add(3 * time.Hour)
	sessionWindowStart := time.Now().Add(-2 * time.Hour)
	sessionWindowEnd := time.Now().Add(2 * time.Hour)

	source := &providercore.Record{
		Name:                  "primary",
		Notes:                 &notes,
		Platform:              capability.PlatformOpenAI,
		Type:                  capability.ProviderTypeAPIKey,
		ProxyID:               &proxyID,
		ProxyFallbackOriginID: &originalProxyID,
		Concurrency:           6,
		Priority:              40,
		RateMultiplier:        &rateMultiplier,
		LoadFactor:            &loadFactor,
		Status:                providercore.StatusError,
		Schedulable:           true,
		ErrorMessage:          "upstream unavailable",
		ExpiresAt:             &expiresAt,
		AutoPauseOnExpired:    false,
		Credentials: map[string]any{
			"api_key":                      "secret",
			"nested":                       map[string]any{"token": "source-token"},
			"openai_workload_capabilities": []any{"text_generation"},
		},
		Extra: map[string]any{
			"config":                                  map[string]any{"region": "us-east-1"},
			"items":                                   []any{map[string]any{"enabled": true}},
			"responses_ws_connection_mode":            "pooled",
			"quota_limit":                             1000,
			"quota_used":                              450,
			"quota_daily_used":                        25,
			"quota_daily_start":                       "2026-07-15T00:00:00Z",
			"model_rate_limits":                       map[string]any{"gpt-5": "2099-01-01T00:00:00Z"},
			"codex_5h_used_percent":                   80,
			"codex_cli_only":                          true,
			"grok_usage_snapshot":                     map[string]any{"status_code": 429},
			"openai_responses_probe_status":           "unsupported",
			"openai_text_route_mode":                  "force_responses",
			"openai_responses_continuation_supported": true,
			"openai_native_compaction_v2_mode":        providercore.OpenAICompactModeForceOn,
			"openai_native_compaction_v2_supported":   true,
			"openai_native_compaction_v2_checked_at":  "2026-07-15T00:00:00Z",
			"openai_native_compaction_v2_last_status": 200,
			"openai_native_compaction_v2_last_error":  "",
			"openai_compact_checked_at":               "2026-07-15T00:00:00Z",
			"qoder_quota_snapshot":                    map[string]any{"used": 80},
			"qoder_quota_updated_at":                  "2026-07-15T00:00:00Z",
			"session_window_utilization":              0.8,
			"passive_usage_sampled_at":                "2026-07-15T00:00:00Z",
			"antigravity_force_token_refresh":         true,
			"antigravity_credits_overages":            map[string]any{"enabled": true},
			"crs_account_id":                          "remote-42",
			"crs_kind":                                "openai-api-key",
			"crs_synced_at":                           "2026-07-15T00:00:00Z",
		},
		GroupIDs:                []int64{7, 3},
		ProviderGroups:          []providercore.GroupMembership{{GroupID: 7}, {GroupID: 3}},
		RateLimitedAt:           &rateLimitedAt,
		RateLimitResetAt:        &rateLimitResetAt,
		OverloadUntil:           &overloadUntil,
		TempUnschedulableUntil:  &tempUnschedulableUntil,
		TempUnschedulableReason: "maintenance",
		SessionWindowStart:      &sessionWindowStart,
		SessionWindowEnd:        &sessionWindowEnd,
		SessionWindowStatus:     "active",
	}
	source.Extra["upstream_billing_probe_enabled"] = true
	source.Extra["upstream_billing_probe"] = map[string]any{"status": "ok"}
	require.NoError(t, repo.Create(ctx, source))

	duplicate, err := svc.DuplicateProvider(ctx, source.ID, "admin:1", "")

	require.NoError(t, err)
	require.NotEqual(t, source.ID, duplicate.ID)
	require.Equal(t, "primary (Copy)", duplicate.Name)
	require.Equal(t, source.Platform, duplicate.Platform)
	require.Equal(t, source.Type, duplicate.Type)
	require.Equal(t, source.Concurrency, duplicate.Concurrency)
	require.Equal(t, source.Priority, duplicate.Priority)
	require.Equal(t, source.AutoPauseOnExpired, duplicate.AutoPauseOnExpired)
	require.Equal(t, source.GroupIDs, duplicate.GroupIDs)
	require.Equal(t, "secret", duplicate.Credentials["api_key"])
	require.Equal(t, map[string]any{"token": "source-token"}, duplicate.Credentials["nested"])
	require.Contains(t, duplicate.UpstreamProtocols(), protocol.ProtocolOpenAIResponses)
	require.NotContains(t, duplicate.UpstreamProtocols(), protocol.ProtocolOpenAIChatCompletions)
	require.Equal(t, map[string]any{
		"config":                       map[string]any{"region": "us-east-1"},
		"items":                        []any{map[string]any{"enabled": true}},
		"responses_ws_connection_mode": "pooled",
		"quota_limit":                  float64(1000),
		"codex_cli_only":               true,
		"openai_compact_mode":          "force_on",
		"openai_responses_continuation_supported": true,
		"openai_native_compaction_v2_mode":        providercore.OpenAICompactModeForceOn,
	}, duplicate.Extra)
	require.NotNil(t, duplicate.ExpiresAt)
	require.True(t, source.ExpiresAt.Equal(*duplicate.ExpiresAt))
	require.Equal(t, source.Notes, duplicate.Notes)
	require.Equal(t, source.ProxyFallbackOriginID, duplicate.ProxyID)
	require.Equal(t, source.RateMultiplier, duplicate.RateMultiplier)
	require.Equal(t, source.LoadFactor, duplicate.LoadFactor)
	require.Equal(t, source.GroupIDs, repo.groupsOf[duplicate.ID])
	require.Equal(t, []providercore.GroupMembership{
		{ProviderID: duplicate.ID, GroupID: 7},
		{ProviderID: duplicate.ID, GroupID: 3},
	}, repo.providerGroupsOf[duplicate.ID])

	require.Equal(t, billing.StatusActive, duplicate.Status)
	require.False(t, duplicate.Schedulable)
	require.Empty(t, duplicate.ErrorMessage)
	require.Nil(t, duplicate.LastUsedAt)
	require.Nil(t, duplicate.RateLimitedAt)
	require.Nil(t, duplicate.RateLimitResetAt)
	require.Nil(t, duplicate.OverloadUntil)
	require.Nil(t, duplicate.TempUnschedulableUntil)
	require.Empty(t, duplicate.TempUnschedulableReason)
	require.Nil(t, duplicate.SessionWindowStart)
	require.Nil(t, duplicate.SessionWindowEnd)
	require.Empty(t, duplicate.SessionWindowStatus)
	assertion.MustType[map[string]any](duplicate.Credentials["nested"])["token"] = "changed"
	assertion.MustType[map[string]any](duplicate.Extra["config"])["region"] = "changed"
	assertion.MustType[map[string]any](assertion.MustType[[]any](duplicate.Extra["items"])[0])["enabled"] = false
	storedSource, getErr := repo.GetByID(ctx, source.ID)
	require.NoError(t, getErr)
	require.Equal(t, "source-token", assertion.MustType[map[string]any](storedSource.Credentials["nested"])["token"])
	require.Equal(t, "us-east-1", assertion.MustType[map[string]any](storedSource.Extra["config"])["region"])
	require.Equal(t, true, assertion.MustType[map[string]any](assertion.MustType[[]any](storedSource.Extra["items"])[0])["enabled"])
	require.Equal(t, "remote-42", storedSource.Extra["crs_account_id"])
}

// TestDuplicateProviderRejectsCredentialShadow attempts duplication of a linked
// Spark shadow. The call returns the shadow-specific bad-request reason and
// leaves the fixture with its existing record count.
func TestDuplicateProviderRejectsCredentialShadow(t *testing.T) {
	ctx := context.Background()
	repo := newDuplicateProviderRepoStub()
	svc := newProviderEditorForTest(repo)
	parentID := int64(99)
	shadow := &providercore.Record{
		Name:             "shadow",
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		ParentProviderID: &parentID,
		QuotaDimension:   providercore.QuotaDimensionSpark,
	}
	require.NoError(t, repo.Create(ctx, shadow))

	_, err := svc.DuplicateProvider(ctx, shadow.ID, "admin:1", "")

	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
	require.Equal(t, "PROVIDER_DUPLICATE_SHADOW_UNSUPPORTED", apperror.Reason(err))
	require.Len(t, repo.providers, 1)
}

// TestDuplicateProviderRejectsRotatingOrUnknownCredentialTypes tries OAuth,
// Setup Token, and an unknown credential kind. Each attempt returns the
// unsupported-type bad request and creates no additional fixture record.
func TestDuplicateProviderRejectsRotatingOrUnknownCredentialTypes(t *testing.T) {
	for _, providerType := range []string{capability.ProviderTypeOAuth, capability.ProviderTypeSetupToken, "legacy-cookie"} {
		t.Run(providerType, func(t *testing.T) {
			ctx := context.Background()
			repo := newDuplicateProviderRepoStub()
			svc := newProviderEditorForTest(repo)
			source := &providercore.Record{
				Name:        "rotating-credential-provider",
				Platform:    capability.PlatformOpenAI,
				Type:        providerType,
				Credentials: map[string]any{"refresh_token": "shared-token"},
			}
			require.NoError(t, repo.Create(ctx, source))

			_, err := svc.DuplicateProvider(ctx, source.ID, "admin:1", "")

			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
			require.Equal(t, "PROVIDER_DUPLICATE_CREDENTIAL_TYPE_UNSUPPORTED", apperror.Reason(err))
			require.Len(t, repo.providers, 1)
		})
	}
}

// TestDuplicateProviderPreservesUngroupedState copies an ungrouped API Key
// provider. The returned group selection remains empty, and the fixture
// receives no membership entry for the duplicate identifier.
func TestDuplicateProviderPreservesUngroupedState(t *testing.T) {
	ctx := context.Background()
	repo := newDuplicateProviderRepoStub()
	svc := newProviderEditorForTest(repo)
	source := &providercore.Record{
		Name:        "ungrouped",
		Platform:    capability.PlatformAnthropic,
		Type:        capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "secret"},
		GroupIDs:    nil,
	}
	require.NoError(t, repo.Create(ctx, source))

	duplicate, err := svc.DuplicateProvider(ctx, source.ID, "admin:1", "")

	require.NoError(t, err)
	require.Empty(t, duplicate.GroupIDs)
	require.NotContains(t, repo.groupsOf, duplicate.ID)
}

// TestDuplicateProviderAtomicCreateFailureLeavesNoOrphan injects failure into
// group-aware creation before its fixture writes begin. Duplication reports
// that failure and leaves the source as the only saved provider.
func TestDuplicateProviderAtomicCreateFailureLeavesNoOrphan(t *testing.T) {
	ctx := context.Background()
	repo := newDuplicateProviderRepoStub()
	svc := newProviderEditorForTest(repo)
	source := &providercore.Record{
		Name:           "source",
		Platform:       capability.PlatformAnthropic,
		Type:           capability.ProviderTypeAPIKey,
		Credentials:    map[string]any{"api_key": "secret"},
		GroupIDs:       []int64{7},
		ProviderGroups: []providercore.GroupMembership{{GroupID: 7}},
	}
	require.NoError(t, repo.Create(ctx, source))
	repo.atomicCreateErr = errors.New("group binding failed")

	_, err := svc.DuplicateProvider(ctx, source.ID, "admin:1", "")

	require.ErrorContains(t, err, "group binding failed")
	require.Len(t, repo.providers, 1)
}

// TestDuplicateProviderReturnsExistingCopyForSameOperationKey repeats
// duplication with the same administrator and operation key. Recovery and retry
// return the same copy, while a different administrator receives a distinct
// scoped operation identity.
func TestDuplicateProviderReturnsExistingCopyForSameOperationKey(t *testing.T) {
	ctx := context.Background()
	repo := newDuplicateProviderRepoStub()
	svc := newProviderEditorForTest(repo)
	source := &providercore.Record{
		Name:        "source",
		Platform:    capability.PlatformAnthropic,
		Type:        capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "secret"},
	}
	require.NoError(t, repo.Create(ctx, source))

	first, err := svc.DuplicateProvider(ctx, source.ID, "admin:7", "stable-operation-key")
	require.NoError(t, err)
	second, err := svc.DuplicateProvider(ctx, source.ID, "admin:7", "stable-operation-key")
	require.NoError(t, err)
	recovered, err := svc.RecoverDuplicateProvider(ctx, source.ID, "admin:7", "stable-operation-key")
	require.NoError(t, err)
	otherAdminRecovery, err := svc.RecoverDuplicateProvider(ctx, source.ID, "admin:8", "stable-operation-key")
	require.NoError(t, err)
	otherAdminCopy, err := svc.DuplicateProvider(ctx, source.ID, "admin:8", "stable-operation-key")
	require.NoError(t, err)

	require.Equal(t, first.ID, second.ID)
	require.Equal(t, first.ID, recovered.ID)
	require.Nil(t, otherAdminRecovery, "durable recovery identity must remain scoped to the initiating admin")
	require.NotEqual(t, first.ID, otherAdminCopy.ID)
	require.Len(t, repo.providers, 3)
	require.NotEmpty(t, first.Extra["duplicate_operation_id"])
}
