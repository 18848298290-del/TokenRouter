package provider

import (
	"context"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// openai_usage_limit.go selects OpenAI cooldown deadlines and persists
// observed subscription plans.
// OpenAI 429 observations combine utilization with relative reset timing to
// choose a provider cooldown. An exhausted weekly window takes precedence over
// an exhausted short window even when its reset count is zero or negative;
// only the fallback choice requires a positive interval.
//
// The same response can contribute plan metadata through a separate durable
// update. Credential shadows skip that write because their effective metadata
// comes from the parent. Stored plan comparison trims existing text and
// ignores case, while accepted incoming text is retained as supplied.
// Persistence precedes local credential mutation, but a nil-error bulk write
// is accepted regardless of affected count. Callers keep mutable observations
// and records stable, synchronize local credential sharing, and provide
// reached diagnostics.

// OpenAIPlanWriter applies partial provider credential updates for observed
// account plans.
type OpenAIPlanWriter interface {
	// BulkUpdate applies the supplied partial update to provider identifiers.
	// This plan observer treats nil error as acceptance regardless of affected
	// count, then mutates its caller-owned record locally.
	BulkUpdate(context.Context, []int64, ProviderBulkUpdate) (int64, error)
}

// OpenAI429ResetTime chooses a relative cooldown from a stable optional quota
// snapshot. Nil observation returns nil; reached clock and info callbacks are
// required, and reset intervals must fit time.Duration arithmetic.
//
// Normalize keeps each used/reset pair associated while assigning short and
// weekly categories. Exhaustion means at least one hundred percent, and the
// exhausted weekly reset wins whenever present, including zero/negative
// seconds. Otherwise an exhausted short reset wins with the same presence
// rule. Only when neither exhausted window supplies timing does the longest
// positive reset win. No usable fallback returns nil; this selection does not
// validate an exhausted reset as a future deadline.
func OpenAI429ResetTime(snapshot *openai.OpenAICodexUsageSnapshot, clock func() time.Time, info func(string, ...any)) *time.Time {
	if snapshot == nil {
		return nil
	}

	normalized := snapshot.Normalize()
	if normalized == nil {
		return nil
	}

	now := clock()

	// One hundred percent marks exhaustion in the observed upstream window.
	is7dExhausted := normalized.Used7dPercent != nil && *normalized.Used7dPercent >= 100
	is5hExhausted := normalized.Used5hPercent != nil && *normalized.Used5hPercent >= 100

	// Let exhausted weekly timing precede exhausted short timing when a reset
	// pointer exists. Zero/negative observations still select their deadline here,
	// preserving presence rather than applying the positive-only fallback rule.
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

	// Without exhausted-window timing, use the longest positive observed interval
	// so the fallback waits for both available windows; nonpositive observations
	// cannot supply a fallback cooldown.
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

// PersistOpenAIObservedPlan writes observed plan text before changing a
// caller-owned record. Missing repository/record, other platform, credential
// shadow, or exactly empty input skips work; reached info/warn callbacks are
// required.
//
// Comparison trims current credential text and ignores case against the
// incoming text as supplied. A different input is persisted unchanged through
// BulkUpdate, then copied into local credentials only after nil error;
// affected-row count is ignored, including zero. Storage failure logs and
// leaves the local record unchanged, while accepted writes retain their local
// mutation. Callers synchronize shared credentials and keep the record stable
// through the repository call and diagnostics.
func PersistOpenAIObservedPlan(ctx context.Context, repo OpenAIPlanWriter, provider *Record, planType string, info, warn func(string, ...any)) {
	if repo == nil || provider == nil || provider.Platform != capability.PlatformOpenAI {
		return
	}
	// Skip the shadow row before writing because its credentials are inherited
	// from the parent. Its own observed plan must not become a competing local
	// credential owner through this update.
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

	// Persist before local credential mutation so a rejected write leaves the
	// caller record unchanged. Nil error accepts even zero affected rows, matching
	// this observer's success criterion rather than proving durable row change.
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
