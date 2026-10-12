package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/httpapi/dto"
)

// Shadow parent hints are filled only for a loaded matching parent, leaving
// ordinary and orphaned provider views usable. The fixture in
// runtime_presenter.go's enrichment test separates upstream account identifiers
// from TokenRouter IDs and checks all five copied hint fields.

// TestEnrichShadowParentInfo supplies a matching parent, ordinary provider and
// orphaned shadow. Matching shadow hints are populated from parent credentials
// and Extra, while the other two response objects retain empty parent email.
func TestEnrichShadowParentInfo(t *testing.T) {
	pid := int64(100)
	parent := &provider.Record{
		ID: 100,
		Credentials: map[string]any{
			"email":                   "owner@example.com",
			"plan_type":               "pro",
			"subscription_expires_at": "2026-12-31T00:00:00Z",
			"chatgpt_account_id":      "acct_123",
		},
		Extra: map[string]any{"privacy_mode": "training_off"},
	}
	parents := map[int64]*provider.Record{100: parent}

	shadow := ProviderWithConcurrency{Provider: &dto.Provider{ID: 200, ParentProviderID: &pid}}
	normal := ProviderWithConcurrency{Provider: &dto.Provider{ID: 1}}
	orphan := ProviderWithConcurrency{Provider: &dto.Provider{ID: 201, ParentProviderID: ptrInt64(999)}}
	items := []ProviderWithConcurrency{shadow, normal, orphan}

	EnrichShadowParentInfo(items, parents)

	require.Equal(t, "owner@example.com", items[0].ParentEmail, "影子回填母提供商邮箱")
	require.Equal(t, "pro", items[0].ParentPlanType)
	require.Equal(t, "training_off", items[0].ParentPrivacyMode)
	require.Equal(t, "2026-12-31T00:00:00Z", items[0].ParentSubscriptionExpiresAt)
	require.Equal(t, "acct_123", items[0].ParentChatGPTAccountID)

	require.Empty(t, items[1].ParentEmail, "非影子不回填")
	require.Empty(t, items[2].ParentEmail, "母提供商缺失时优雅留空")
}

// ptrInt64 returns an independently allocated pointer to a fixture provider
// identifier.
func ptrInt64(v int64) *int64 { return &v }
