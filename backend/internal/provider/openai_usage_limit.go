package provider

import (
	"context"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// OpenAIPlanWriter applies partial provider updates for observed plan information.
type OpenAIPlanWriter interface {
	BulkUpdate(context.Context, []int64, ProviderBulkUpdate) (int64, error)
}

// OpenAI429ResetTime selects a recovery time from observed rate-limit windows.
// It returns nil when the observation supplies no usable reset time.
func OpenAI429ResetTime(snapshot *openai.OpenAICodexUsageSnapshot, clock func() time.Time, info func(string, ...any)) *time.Time {
	if snapshot == nil {
		return nil
	}

	normalized := snapshot.Normalize()
	if normalized == nil {
		return nil
	}

	now := clock()

	// Treat utilization of at least 100 percent as exhaustion.
	is7dExhausted := normalized.Used7dPercent != nil && *normalized.Used7dPercent >= 100
	is5hExhausted := normalized.Used5hPercent != nil && *normalized.Used5hPercent >= 100

	// Prefer an exhausted window's reset, checking seven days before five hours.
	if is7dExhausted && normalized.Reset7dSeconds != nil {
		resetAt := now.Add(time.Duration(*normalized.Reset7dSeconds) * time.Second)
		info("openai_429_7d_limit_exhausted", "reset_after_seconds", *normalized.Reset7dSeconds, "reset_at", resetAt)
		return &resetAt
	}
	if is5hExhausted && normalized.Reset5hSeconds != nil {
		resetAt := now.Add(time.Duration(*normalized.Reset5hSeconds) * time.Second)
		info("openai_429_5h_limit_exhausted", "reset_after_seconds", *normalized.Reset5hSeconds, "reset_at", resetAt)
		return &resetAt
	}

	// If no exhausted window supplies a reset, use the longest positive reset observed.
	var maxResetSecs int
	if normalized.Reset7dSeconds != nil && *normalized.Reset7dSeconds > maxResetSecs {
		maxResetSecs = *normalized.Reset7dSeconds
	}
	if normalized.Reset5hSeconds != nil && *normalized.Reset5hSeconds > maxResetSecs {
		maxResetSecs = *normalized.Reset5hSeconds
	}
	if maxResetSecs > 0 {
		resetAt := now.Add(time.Duration(maxResetSecs) * time.Second)
		info("openai_429_using_max_reset", "max_reset_seconds", maxResetSecs, "reset_at", resetAt)
		return &resetAt
	}

	return nil
}

// PersistOpenAIObservedPlan writes the plan type observed in a 429 response to provider credentials.
func PersistOpenAIObservedPlan(ctx context.Context, repo OpenAIPlanWriter, provider *Record, planType string, info, warn func(string, ...any)) {
	if repo == nil || provider == nil || provider.Platform != capability.PlatformOpenAI {
		return
	}
	// Credential shadows inherit plan metadata from the parent, so exclude them before the bulk write.
	// The parent's own requests maintain its plan_type.
	if provider.IsCredentialShadow() {
		return
	}

	if planType == "" {
		return
	}

	current := strings.TrimSpace(provider.GetCredential("plan_type"))
	if strings.EqualFold(current, planType) {
		return
	}

	if _, err := repo.BulkUpdate(ctx, []int64{provider.ID}, ProviderBulkUpdate{
		Credentials: map[string]any{"plan_type": planType},
	}); err != nil {
		warn("openai_429_plan_type_sync_failed", "provider_id", provider.ID, "plan_type", planType, "error", err)
		return
	}

	if provider.Credentials == nil {
		provider.Credentials = make(map[string]any, 1)
	}
	provider.Credentials["plan_type"] = planType
	info("openai_429_plan_type_synced", "provider_id", provider.ID, "previous_plan_type", current, "plan_type", planType)
}
