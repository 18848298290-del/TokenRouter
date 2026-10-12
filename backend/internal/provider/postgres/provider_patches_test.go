package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Usage display observations and route configuration have different
// scheduling-event requirements. These cases cover provider_patches.go's key
// classification: billing, reset credits, CN and Qoder usage remain neutral,
// while OpenAI compaction and text-route settings request the durable
// scheduler notification.

// TestGrokBillingSnapshotIsSchedulerNeutral classifies a Grok billing display
// snapshot through the exact-key and patch helpers. Both identify it as
// neutral, so that observation alone does not request a scheduler outbox
// event.
func TestGrokBillingSnapshotIsSchedulerNeutral(t *testing.T) {
	t.Parallel()

	require.True(t, IsSchedulerNeutralExtraKey("grok_billing_snapshot"))
	require.False(t, ShouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{
		"grok_billing_snapshot": map[string]any{"usage_percent": 50},
	}))
}

// TestOpenAIResetCreditSnapshotIsSchedulerNeutral supplies an OpenAI
// reset-credit display snapshot to both classification helpers. The key is
// neutral and its isolated patch leaves scheduler event selection false.
func TestOpenAIResetCreditSnapshotIsSchedulerNeutral(t *testing.T) {
	t.Parallel()

	require.True(t, IsSchedulerNeutralExtraKey("codex_reset_credit_snapshot"))
	require.False(t, ShouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{
		"codex_reset_credit_snapshot": map[string]any{"available_count": 1},
	}))
}

// TestCNUsageMonitorSnapshotIsSchedulerNeutralForGenericUpdates classifies a
// monitor snapshot through generic Extra updates. Its key remains neutral
// there, while the dedicated version-checked monitor write owns scheduling
// notification elsewhere.
func TestCNUsageMonitorSnapshotIsSchedulerNeutralForGenericUpdates(t *testing.T) {
	t.Parallel()
	require.True(t, IsSchedulerNeutralExtraKey("cn_usage_monitor_snapshot"))
	require.False(t, ShouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{
		"cn_usage_monitor_snapshot": map[string]any{"version": 1},
	}))
}

// TestShouldEnqueueSchedulerOutboxForExtraUpdates_CompactConfigurationKeysAreRelevant
// supplies the legacy and native compaction mode keys together. The patch
// requests a scheduler event so routing capability configuration is
// refreshed.
func TestShouldEnqueueSchedulerOutboxForExtraUpdates_CompactConfigurationKeysAreRelevant(t *testing.T) {
	updates := map[string]any{
		"openai_compact_mode":              "force_off",
		"openai_native_compaction_v2_mode": "force_on",
	}

	if !ShouldEnqueueSchedulerOutboxForExtraUpdates(updates) {
		t.Fatalf("expected compact capability updates to enqueue scheduler outbox")
	}
}

// TestShouldEnqueueSchedulerOutboxForExtraUpdates_OpenAITextRouteIsRelevant
// changes the configured OpenAI text route to Chat Completions.
// Classification requests a scheduler event rather than treating that route
// setting as display-only usage.
func TestShouldEnqueueSchedulerOutboxForExtraUpdates_OpenAITextRouteIsRelevant(t *testing.T) {
	updates := map[string]any{
		"openai_text_route_mode": "force_chat_completions",
	}

	if !ShouldEnqueueSchedulerOutboxForExtraUpdates(updates) {
		t.Fatalf("expected responses capability updates to enqueue scheduler outbox")
	}
}

// TestShouldEnqueueSchedulerOutboxForExtraUpdates_QoderQuotaSnapshotIsNeutral
// supplies Qoder quota details and their observation timestamp together. The
// patch stays neutral, protecting the distinction between display
// observations and scheduler configuration.
func TestShouldEnqueueSchedulerOutboxForExtraUpdates_QoderQuotaSnapshotIsNeutral(t *testing.T) {
	updates := map[string]any{
		"qoder_quota_snapshot": map[string]any{
			"user_type": "teams",
			"user_quota": map[string]any{
				"total":     2940,
				"used":      2,
				"remaining": 2938,
			},
		},
		"qoder_quota_updated_at": "2026-07-05T10:00:00Z",
	}

	if ShouldEnqueueSchedulerOutboxForExtraUpdates(updates) {
		t.Fatalf("expected qoder quota snapshot updates to skip scheduler outbox")
	}
}
