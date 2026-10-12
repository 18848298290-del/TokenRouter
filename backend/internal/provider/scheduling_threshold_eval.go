package provider

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

const providerSchedulingThresholdCredentialKey = "provider_scheduling_threshold"

// ProviderSchedulingThresholdDecision describes a provider pause decision without applying it.
type ProviderSchedulingThresholdDecision struct {
	ShouldPause      bool
	Platform         string
	Window           string
	Scope            string
	ThresholdPercent int
	UsedPercent      float64
	Until            *time.Time
}

type SchedulingThresholdCandidate struct {
	Window      string
	Scope       string
	UsedPercent float64
	Until       *time.Time
}

// EvaluateProviderSchedulingThreshold evaluates observed usage against provider overrides and platform thresholds.
func EvaluateProviderSchedulingThreshold(provider *Record, thresholds map[string]int, now time.Time) ProviderSchedulingThresholdDecision {
	decision := ProviderSchedulingThresholdDecision{}
	if provider == nil {
		return decision
	}

	decision.Platform = strings.ToLower(strings.TrimSpace(provider.Platform))
	if decision.Platform == "" {
		return decision
	}
	if !isAllowedSchedulingThresholdPlatform(decision.Platform) {
		return decision
	}

	threshold, ok := resolveEffectiveProviderSchedulingThreshold(provider, thresholds, decision.Platform)
	decision.ThresholdPercent = threshold
	if !ok || threshold >= 100 {
		return decision
	}

	var winner *SchedulingThresholdCandidate
	switch decision.Platform {
	case PlatformOpenAI:
		winner = pickLatestResetSchedulingCandidate(openAIThresholdCandidates(provider, now), threshold, now)
	case PlatformAnthropic:
		winner = pickLatestResetSchedulingCandidate(anthropicThresholdCandidates(provider), threshold, now)
	case PlatformGrok:
		winner = pickLatestResetSchedulingCandidate(grokThresholdCandidates(provider), threshold, now)
	case PlatformKimi:
		winner = pickLatestResetSchedulingCandidate(CNProviderThresholdCandidates(provider, PlatformKimi), threshold, now)
	case PlatformZhipu:
		winner = pickLatestResetSchedulingCandidate(CNProviderThresholdCandidates(provider, PlatformZhipu), threshold, now)
	default:
		return decision
	}

	if winner == nil {
		return decision
	}

	decision.ShouldPause = true
	decision.Window = winner.Window
	decision.Scope = winner.Scope
	decision.UsedPercent = winner.UsedPercent
	decision.Until = winner.Until
	return decision
}

func EvaluateAnthropicFableSchedulingThreshold(provider *Record, thresholds map[string]int, now time.Time) ProviderSchedulingThresholdDecision {
	decision := ProviderSchedulingThresholdDecision{}
	if provider == nil || !strings.EqualFold(strings.TrimSpace(provider.Platform), PlatformAnthropic) {
		return decision
	}

	decision.Platform = PlatformAnthropic
	threshold, ok := resolveEffectiveProviderSchedulingThreshold(provider, thresholds, PlatformAnthropic)
	decision.ThresholdPercent = threshold
	if !ok || threshold >= 100 {
		return decision
	}

	candidate := anthropicFableThresholdCandidate(provider)
	if !CandidateMatchesThreshold(candidate, threshold, now) {
		return decision
	}

	decision.ShouldPause = true
	decision.Window = candidate.Window
	decision.Scope = candidate.Scope
	decision.UsedPercent = candidate.UsedPercent
	decision.Until = candidate.Until
	return decision
}

func isAllowedSchedulingThresholdPlatform(platform string) bool {
	return slices.Contains(AllowedSchedulingThresholdPlatforms, platform)
}

func resolveEffectiveProviderSchedulingThreshold(provider *Record, thresholds map[string]int, platform string) (int, bool) {
	if provider != nil {
		if threshold, ok := providerSchedulingThresholdOverride(provider); ok {
			return threshold, true
		}
	}
	return lookupProviderSchedulingThreshold(thresholds, platform)
}

func providerSchedulingThresholdOverride(provider *Record) (int, bool) {
	if provider == nil || len(provider.Credentials) == 0 {
		return 0, false
	}
	raw, ok := provider.Credentials[providerSchedulingThresholdCredentialKey]
	if !ok {
		return 0, false
	}
	return parseProviderSchedulingThresholdValue(raw)
}

func parseProviderSchedulingThresholdValue(raw any) (int, bool) {
	var value int
	switch v := raw.(type) {
	case int:
		value = v
	case int64:
		value = int(v)
	case float64:
		value = int(math.Round(v))
	case float32:
		value = int(math.Round(float64(v)))
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}
		value = int(math.Round(parsed))
	case string:
		raw := strings.TrimSpace(v)
		parsed, err := strconv.Atoi(raw)
		if err == nil {
			value = parsed
			break
		}
		parsedFloat, floatErr := strconv.ParseFloat(raw, 64)
		if floatErr != nil {
			return 0, false
		}
		value = int(math.Round(parsedFloat))
	default:
		return 0, false
	}
	if value < 1 || value > 100 {
		return 0, false
	}
	return value, true
}

func lookupProviderSchedulingThreshold(thresholds map[string]int, platform string) (int, bool) {
	if len(thresholds) == 0 {
		return 0, false
	}
	value, ok := thresholds[platform]
	return value, ok
}

func openAIThresholdCandidates(provider *Record, now time.Time) []*SchedulingThresholdCandidate {
	if provider == nil {
		return nil
	}
	if !openAICodexSnapshotIdentityTrusted(provider) {
		return nil
	}
	return []*SchedulingThresholdCandidate{
		openAIThresholdCandidate(provider.Extra, "5h", now),
		openAIThresholdCandidate(provider.Extra, "7d", now),
	}
}

func openAICodexSnapshotIdentityTrusted(provider *Record) bool {
	if provider == nil || !provider.IsOpenAIOAuth() || len(provider.Extra) == 0 {
		return true
	}

	if identityValuesConflict(
		firstStringValue(provider.Credentials, "email"),
		firstStringValue(provider.Extra, "email", "email_address"),
	) {
		return false
	}
	if identityValuesConflict(
		firstStringValue(provider.Credentials, "chatgpt_account_id"),
		firstStringValue(provider.Extra, "chatgpt_account_id", "account_id"),
	) {
		return false
	}
	if identityValuesConflict(
		firstStringValue(provider.Credentials, "workspace_id", "chatgpt_workspace_id", "organization_id", "org_id"),
		firstStringValue(provider.Extra, "workspace_id", "chatgpt_workspace_id", "organization_id", "org_id"),
	) {
		return false
	}
	return true
}

func identityValuesConflict(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	return left != "" && right != "" && !strings.EqualFold(left, right)
}

// firstStringValue returns the first nonempty string or JSON-encoded value for Codex snapshot identity comparison.
func firstStringValue(values map[string]any, keys ...string) string {
	if len(values) == 0 {
		return ""
	}
	for _, key := range keys {
		raw, ok := values[key]
		if !ok || raw == nil {
			continue
		}
		switch typed := raw.(type) {
		case string:
			if v := strings.TrimSpace(typed); v != "" {
				return v
			}
		default:
			encoded, err := json.Marshal(raw)
			if err == nil {
				v := strings.Trim(strings.TrimSpace(string(encoded)), `"`)
				if v != "" && v != "null" {
					return v
				}
			}
		}
	}
	return ""
}

func openAIThresholdCandidate(extra map[string]any, window string, now time.Time) *SchedulingThresholdCandidate {
	if len(extra) == 0 {
		return nil
	}

	var (
		usedPercentKey string
		resetAtKey     string
	)
	switch window {
	case "5h":
		usedPercentKey = "codex_5h_used_percent"
		resetAtKey = "codex_5h_reset_at"
	case "7d":
		usedPercentKey = "codex_7d_used_percent"
		resetAtKey = "codex_7d_reset_at"
	default:
		return nil
	}

	usedPercent, ok := extra[usedPercentKey]
	if !ok {
		return nil
	}
	if OpenAIQuotaWindowReset(extra, window, now) || OpenAICodexSnapshotStaleForPause(extra, now) {
		return nil
	}
	return &SchedulingThresholdCandidate{
		Window:      window,
		UsedPercent: schedulingPercentValue(usedPercent),
		Until:       parseSchedulingResetAt(extra[resetAtKey]),
	}
}

func anthropicThresholdCandidates(provider *Record) []*SchedulingThresholdCandidate {
	if provider == nil {
		return nil
	}

	var candidates []*SchedulingThresholdCandidate
	if usedPercent := utilizationAsPercent(provider.Extra["session_window_utilization"]); usedPercent > 0 {
		candidates = append(candidates, &SchedulingThresholdCandidate{
			Window:      "5h",
			UsedPercent: usedPercent,
			Until:       CloneThresholdTime(provider.SessionWindowEnd),
		})
	}
	if usedPercent := utilizationAsPercent(provider.Extra["passive_usage_7d_utilization"]); usedPercent > 0 {
		candidates = append(candidates, &SchedulingThresholdCandidate{
			Window:      "7d",
			UsedPercent: usedPercent,
			Until:       parseSchedulingResetAt(provider.Extra["passive_usage_7d_reset"]),
		})
	}
	return candidates
}

func anthropicFableThresholdCandidate(provider *Record) *SchedulingThresholdCandidate {
	if provider == nil {
		return nil
	}
	usedPercent := utilizationAsPercent(provider.Extra["passive_usage_7d_oi_utilization"])
	if usedPercent <= 0 {
		return nil
	}
	return &SchedulingThresholdCandidate{
		Window:      "7d_oi",
		Scope:       AnthropicFableRateLimitKey,
		UsedPercent: usedPercent,
		Until:       parseSchedulingResetAt(provider.Extra["passive_usage_7d_oi_reset"]),
	}
}

// grokThresholdCandidates reads the rolling quota window recorded from response headers.
// Official seven-day and thirty-day billing windows do not trigger this scheduling pause.
func grokThresholdCandidates(provider *Record) []*SchedulingThresholdCandidate {
	if provider == nil {
		return nil
	}
	return []*SchedulingThresholdCandidate{
		{
			Window:      "quota",
			Scope:       "grok",
			UsedPercent: schedulingPercentValue(provider.Extra["grok_sched_utilization"]),
			Until:       parseSchedulingResetAt(provider.Extra["grok_sched_reset_at"]),
		},
	}
}

// CNProviderThresholdCandidates reads monitoring windows whose snapshot identity matches
// the current credentials, endpoint, proxy, and TLS settings.
func CNProviderThresholdCandidates(provider *Record, platform string) []*SchedulingThresholdCandidate {
	if provider == nil || provider.Platform != platform || !provider.IsCodingPlan() {
		return nil
	}
	snapshot := ValidCNUsageMonitorSnapshot(provider)
	if snapshot == nil || snapshot.Mode != "limits" || snapshot.ObservedAt == nil {
		return nil
	}
	candidates := make([]*SchedulingThresholdCandidate, 0, len(snapshot.Limits))
	for _, limit := range snapshot.Limits {
		if limit.Used == nil || limit.ResetAt == nil {
			continue
		}
		candidates = append(candidates, &SchedulingThresholdCandidate{
			Window:      limit.Name,
			Scope:       platform,
			UsedPercent: *limit.Used,
			Until:       CloneThresholdTime(limit.ResetAt),
		})
	}
	return candidates
}

func pickLatestResetSchedulingCandidate(candidates []*SchedulingThresholdCandidate, threshold int, now time.Time) *SchedulingThresholdCandidate {
	var winner *SchedulingThresholdCandidate
	for _, candidate := range candidates {
		if !CandidateMatchesThreshold(candidate, threshold, now) {
			continue
		}
		if winner == nil || candidate.Until.After(*winner.Until) {
			winner = candidate
			continue
		}
		if winner.Until.Equal(*candidate.Until) && candidate.UsedPercent > winner.UsedPercent {
			winner = candidate
		}
	}
	return winner
}

func CandidateMatchesThreshold(candidate *SchedulingThresholdCandidate, threshold int, now time.Time) bool {
	if candidate == nil || candidate.Until == nil || !candidate.Until.After(now) {
		return false
	}
	return candidate.UsedPercent >= float64(threshold)
}

func utilizationAsPercent(raw any) float64 {
	switch v := raw.(type) {
	case float64:
		if v >= 0 && v <= 1 {
			return v * 100
		}
		return v
	case float32:
		value := float64(v)
		if value >= 0 && value <= 1 {
			return value * 100
		}
		return value
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		value, err := v.Float64()
		if err != nil {
			return 0
		}
		if strings.Contains(v.String(), ".") && value >= 0 && value <= 1 {
			return value * 100
		}
		return value
	case string:
		trimmed := strings.TrimSpace(v)
		value, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return 0
		}
		if strings.Contains(trimmed, ".") && value >= 0 && value <= 1 {
			return value * 100
		}
		return value
	default:
		return 0
	}
}

func schedulingPercentValue(raw any) float64 {
	switch v := raw.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		value, err := v.Float64()
		if err != nil {
			return 0
		}
		return value
	case string:
		value, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0
		}
		return value
	default:
		return 0
	}
}

func parseSchedulingResetAt(raw any) *time.Time {
	switch v := raw.(type) {
	case nil:
		return nil
	case time.Time:
		ts := v
		return &ts
	case *time.Time:
		return CloneThresholdTime(v)
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil
		}
		ts, err := ParseSchedulingTime(trimmed)
		if err != nil {
			return nil
		}
		return &ts
	case json.Number:
		if value, err := v.Int64(); err == nil && value > 0 {
			ts := time.Unix(value, 0)
			return &ts
		}
		if value, err := v.Float64(); err == nil && value > 0 {
			ts := time.Unix(int64(value), 0)
			return &ts
		}
	case float64:
		if v > 0 {
			ts := time.Unix(int64(v), 0)
			return &ts
		}
	case float32:
		if v > 0 {
			ts := time.Unix(int64(v), 0)
			return &ts
		}
	case int:
		if v > 0 {
			ts := time.Unix(int64(v), 0)
			return &ts
		}
	case int64:
		if v > 0 {
			ts := time.Unix(v, 0)
			return &ts
		}
	}
	return nil
}

func ParseSchedulingTime(raw string) (time.Time, error) { return timezone.ParseFlexibleTimestamp(raw) }

func CloneThresholdTime(src *time.Time) *time.Time {
	if src == nil {
		return nil
	}
	value := *src
	return &value
}
