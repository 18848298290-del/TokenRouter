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

// Provider usage thresholds require both a reached percentage and a future
// reset before a pause can be proposed. A valid credential override takes
// precedence over its platform setting, and a threshold of one hundred
// disables evaluation. Matching windows compete by latest reset, then by
// greater usage when their deadlines are equal.
//
// Platform observations need different interpretation before that comparison.
// OpenAI percentages remain percentage points and its OAuth snapshot can be
// rejected for conflicting account identifiers, a reached reset, or known
// staleness. Anthropic floating ratios can become percentages, while Grok
// uses its recorded header quota window and Coding Plan providers require a
// matching monitor snapshot. Fable's observation is evaluated separately with
// its model scope so ordinary Anthropic windows and the Fable window retain
// separate decisions.

// providerSchedulingThresholdCredentialKey selects the per-provider
// credential override that takes precedence over configured platform
// percentages during pause evaluation.
const providerSchedulingThresholdCredentialKey = "provider_scheduling_threshold"

// ProviderSchedulingThresholdDecision describes a proposed usage pause and
// the settings considered while evaluating it.
type ProviderSchedulingThresholdDecision struct {
	// ShouldPause selects when Window, Scope, UsedPercent, and Until describe a
	// matched observation. Platform and ThresholdPercent can also identify
	// evaluated settings in a nonmatching result.
	ShouldPause      bool       // qualifying usage-window result
	Platform         string     // normalized provider platform name
	Window           string     // selected usage-window label
	Scope            string     // selected window scope label
	ThresholdPercent int        // percentage
	UsedPercent      float64    // percentage
	Until            *time.Time // selected reset deadline pointer
}

// SchedulingThresholdCandidate holds one normalized usage percentage with its
// window, scope, and reset deadline.
type SchedulingThresholdCandidate struct {
	Window      string  // observed usage-window label
	Scope       string  // observed window scope label
	UsedPercent float64 // percentage
	// Until must be present and strictly future for CandidateMatchesThreshold to
	// accept the observation. Callers keep pointed-to deadline values stable
	// while evaluating candidates.
	Until *time.Time // observed reset deadline pointer
}

// EvaluateProviderSchedulingThreshold selects a pause window for a supported
// provider without changing its state. A valid credential override precedes
// the platform map, whose values callers supply from validated settings;
// missing thresholds and values at least one hundred skip evaluation.
//
// Candidates are built from each platform's supported observations, then
// require usage at least the threshold and a reset strictly after now. The
// latest matching reset wins, with greater usage breaking equal-reset ties.
// Nonmatching results can still report normalized Platform and the resolved
// ThresholdPercent. HealthService decides whether to persist or publish a
// returned pause.
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
	// Keep the resolved threshold in this nonmatching result so callers can
	// distinguish a disabled configured gate from missing window observations.
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

// EvaluateAnthropicFableSchedulingThreshold evaluates the separate Anthropic
// Fable weekly observation for its model scope. It uses the same credential
// override and platform map precedence, but accepts only a reached percentage
// with a reset strictly after now.
//
// A positive result carries AnthropicFableRateLimitKey for the health
// caller's model pause. The provider-wide evaluator uses other Anthropic
// observations, allowing Fable usage to block its own scope while
// provider-wide usage remains below threshold. Supply validated platform
// values; one hundred or greater disables this decision.
func EvaluateAnthropicFableSchedulingThreshold(provider *Record, thresholds map[string]int, now time.Time) ProviderSchedulingThresholdDecision {
	decision := ProviderSchedulingThresholdDecision{}
	if provider == nil || !strings.EqualFold(strings.TrimSpace(provider.Platform), PlatformAnthropic) {
		return decision
	}

	decision.Platform = PlatformAnthropic
	threshold, ok := resolveEffectiveProviderSchedulingThreshold(provider, thresholds, PlatformAnthropic)
	decision.ThresholdPercent = threshold
	// Keep the resolved threshold in this nonmatching result so callers can
	// distinguish a disabled configured gate from missing window observations.
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

// isAllowedSchedulingThresholdPlatform reports whether the current threshold
// allowlist includes the supplied platform.
func isAllowedSchedulingThresholdPlatform(platform string) bool {
	return slices.Contains(AllowedSchedulingThresholdPlatforms, platform)
}

// resolveEffectiveProviderSchedulingThreshold prefers a valid credential
// override, then looks up the supplied platform map. Invalid or absent
// overrides permit inheritance; map values pass through with their presence
// flag and expect prior validation.
func resolveEffectiveProviderSchedulingThreshold(provider *Record, thresholds map[string]int, platform string) (int, bool) {
	// A valid provider choice precedes platform inheritance; unavailable or
	// invalid credential values leave the platform map eligible for selection.
	if provider != nil {
		if threshold, ok := providerSchedulingThresholdOverride(provider); ok {
			return threshold, true
		}
	}
	return lookupProviderSchedulingThreshold(thresholds, platform)
}

// providerSchedulingThresholdOverride reads and validates the provider's
// credential override. Nil records, absent credentials, missing keys, and
// unsupported or out-of-range values return false so the platform setting can
// remain effective.
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

// parseProviderSchedulingThresholdValue accepts supported numeric forms and
// returns a whole percentage from one through one hundred. Floating
// representations round to the nearest integer; unsupported, unparseable, or
// out-of-range converted values return false.
func parseProviderSchedulingThresholdValue(raw any) (int, bool) {
	var value int
	switch v := raw.(type) {
	case int:
		value = v
	case int64:
		value = int(v)
	case float64:
		// Credential decimals round before range validation, so 75.5 selects
		// seventy-six rather than truncating the provider's intended percentage.
		value = int(math.Round(v))
	case float32:
		// Match float64 override rounding before applying whole-percent bounds.
		value = int(math.Round(float64(v)))
	case json.Number:
		// A failed numeric override permits platform inheritance instead of
		// selecting a converted percentage from unusable credential data.
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}
		// JSON numeric overrides follow the same nearest-integer credential rule.
		value = int(math.Round(parsed))
	case string:
		raw := strings.TrimSpace(v)
		parsed, err := strconv.Atoi(raw)
		if err == nil {
			value = parsed
			break
		}
		// A decimal credential string cannot use integer parsing. Try its floating
		// representation before leaving the override unavailable for platform
		// inheritance.
		parsedFloat, floatErr := strconv.ParseFloat(raw, 64)
		// Both parsers rejected this representation. Leave the override unavailable
		// so the platform map can supply its configured threshold.
		if floatErr != nil {
			return 0, false
		}
		// Decimal string overrides use the numeric credential's rounding rule.
		value = int(math.Round(parsedFloat))
	default:
		return 0, false
	}
	if value < 1 || value > 100 {
		return 0, false
	}
	return value, true
}

// lookupProviderSchedulingThreshold returns the platform map value with its
// exact presence flag. Empty maps return false; present values are passed
// through directly for callers supplying validated settings.
func lookupProviderSchedulingThreshold(thresholds map[string]int, platform string) (int, bool) {
	if len(thresholds) == 0 {
		return 0, false
	}
	value, ok := thresholds[platform]
	return value, ok
}

// openAIThresholdCandidates builds five-hour and seven-day observations after
// checking trusted snapshot identity. Nil providers or conflicting OAuth
// identity fields return nil; individual unusable windows remain nil entries
// for the selector to skip.
func openAIThresholdCandidates(provider *Record, now time.Time) []*SchedulingThresholdCandidate {
	if provider == nil {
		return nil
	}
	// Reject observations tied to a conflicting nonempty account identifier
	// before reading either window, so another account's usage cannot select
	// this OAuth provider's pause.
	if !openAICodexSnapshotIdentityTrusted(provider) {
		return nil
	}
	return []*SchedulingThresholdCandidate{
		openAIThresholdCandidate(provider.Extra, "5h", now),
		openAIThresholdCandidate(provider.Extra, "7d", now),
	}
}

// openAICodexSnapshotIdentityTrusted checks the current OpenAI OAuth identity
// against the identity stored with Codex usage. Nil records, other provider
// types, and empty Extra pass this check; missing identifiers also leave the
// snapshot usable.
//
// Email, account, and workspace identifiers are compared separately, using
// each map's first usable alias. Nonempty values are trimmed and compared
// without case, and any conflict rejects both quota windows before their
// percentages are read. Partial identities therefore permit evaluation, while
// a conflicting supplied identity prevents another account's observation from
// selecting this provider's scheduling pause.
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

// identityValuesConflict reports differing nonempty identity strings after
// trimming and case-insensitive comparison. An unavailable identifier on
// either side permits the snapshot comparison to continue rather than
// establishing a conflict.
func identityValuesConflict(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	return left != "" && right != "" && !strings.EqualFold(left, right)
}

// firstStringValue returns the first usable string or JSON-encoded identity
// in key order. Blank, nil, unsupported-to-JSON, and null values leave later
// keys eligible; surrounding JSON quotes are removed from encoded results.
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
			// Unencodable values leave later aliases eligible, so identity comparison
			// can still use a supported account identifier from the same credential or
			// observation map.
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

// openAIThresholdCandidate pairs a five-hour or seven-day Codex percentage
// with its parsed absolute reset deadline. Absent percentage keys and
// unsupported windows return nil, as do windows the shared helpers classify as
// reset or stale. Missing or malformed observation times leave the age check
// undecided.
//
// Reset classification and candidate deadline parsing accept different stored
// representations. A numeric or time-valued absolute deadline can reach the
// candidate even when the reset helper cannot parse it.
// CandidateMatchesThreshold then requires that parsed deadline to be strictly
// future. Missing or elapsed deadlines cannot select a pause despite a high
// percentage.
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
	// A reached reset or known-old observation no longer supports this pause.
	// Missing timestamps remain eligible under the shared Codex staleness rule.
	if OpenAIQuotaWindowReset(extra, window, now) || OpenAICodexSnapshotStaleForPause(extra, now) {
		return nil
	}
	return &SchedulingThresholdCandidate{
		Window:      window,
		UsedPercent: schedulingPercentValue(usedPercent),
		Until:       parseSchedulingResetAt(extra[resetAtKey]),
	}
}

// anthropicThresholdCandidates builds positive five-hour and weekly provider
// usage observations. The current session end supplies the five-hour reset,
// copied into the candidate; reset validity and threshold matching are
// deferred to the selector.
func anthropicThresholdCandidates(provider *Record) []*SchedulingThresholdCandidate {
	if provider == nil {
		return nil
	}

	var candidates []*SchedulingThresholdCandidate
	if usedPercent := utilizationAsPercent(provider.Extra["session_window_utilization"]); usedPercent > 0 {
		candidates = append(candidates, &SchedulingThresholdCandidate{
			Window:      "5h",
			UsedPercent: usedPercent,
			// Copy the record's bound so this candidate retains a separate deadline
			// value while later health handling owns its pause state.
			Until: CloneThresholdTime(provider.SessionWindowEnd),
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

// anthropicFableThresholdCandidate builds a weekly Fable observation tagged
// with its model scope. Nil records or nonpositive utilization return nil;
// parsed NaN can form a candidate, whose comparison later fails the
// reached-threshold check. The separate evaluator validates the reset
// deadline.
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

// grokThresholdCandidates reads the recorded rolling header-quota percentage
// and reset for a provider. Nil input returns nil; missing values can produce
// an unusable candidate, while official billing periods stay outside this
// selection.
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

// CNProviderThresholdCandidates builds Coding Plan limit candidates from a
// monitor snapshot accepted by ValidCNUsageMonitorSnapshot's version,
// adapter, and current-fingerprint checks. The record platform must match
// exactly, the snapshot must use limits mode with an observation timestamp,
// and each retained limit needs usage and reset pointers. Deadline and
// threshold checks occur later; the observation's age is not checked here.
func CNProviderThresholdCandidates(provider *Record, platform string) []*SchedulingThresholdCandidate {
	if provider == nil || provider.Platform != platform || !provider.IsCodingPlan() {
		return nil
	}
	// Compare the monitor's version, adapter, and current query fingerprint
	// before taking any limits; a rejected stored snapshot contributes no
	// candidates to this provider's scheduling threshold.
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

// pickLatestResetSchedulingCandidate chooses a qualifying window with the
// latest reset, returning nil when none matches. Callers keep candidates
// stable during selection; qualifying windows need a future deadline and
// usage at least the supplied threshold.
//
// When reset times tie, the candidate with greater usage wins; equal usage
// keeps the earlier input candidate. Returning the original candidate pointer
// preserves its window and scope with the deadline that decided selection.
// The enclosing evaluator copies those fields into its proposed pause result.
func pickLatestResetSchedulingCandidate(candidates []*SchedulingThresholdCandidate, threshold int, now time.Time) *SchedulingThresholdCandidate {
	var winner *SchedulingThresholdCandidate
	for _, candidate := range candidates {
		if !CandidateMatchesThreshold(candidate, threshold, now) {
			continue
		}
		// The last qualifying reset holds scheduling until every matched window has
		// elapsed; greater usage resolves equal-deadline choices without changing
		// that common recovery time.
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

// CandidateMatchesThreshold reports whether usage reaches the supplied
// percentage and its reset is strictly after now. Nil candidates, absent
// deadlines, and reached resets return false even when usage is high.
//
// Both observations are required together: exhausted usage stops selecting a
// pause once its recorded window has reset. Threshold is used as supplied,
// so callers validate platform settings before evaluation; credential
// overrides are checked by the enclosing evaluators.
// Keep candidate fields and the pointed-to deadline stable during comparison;
// parsed NaN usage fails the percentage comparison rather than selecting a
// window.
func CandidateMatchesThreshold(candidate *SchedulingThresholdCandidate, threshold int, now time.Time) bool {
	if candidate == nil || candidate.Until == nil || !candidate.Until.After(now) {
		return false
	}
	return candidate.UsedPercent >= float64(threshold)
}

// utilizationAsPercent interprets Anthropic utilization according to its
// stored representation. Float32 and float64 values from zero through one are
// multiplied by one hundred, while integer values are already percentage
// points.
//
// String and json.Number values use fractional scaling in that range only
// when their text contains a decimal point. Thus a floating one means one
// hundred percent, while integer one and text "1" mean one percent.
// Unsupported or failed parsing returns zero; other parsed values pass
// through, including nonfinite values.
func utilizationAsPercent(raw any) float64 {
	switch v := raw.(type) {
	case float64:
		// Floating Anthropic observations encode a fraction, unlike Codex
		// percentage points and integer legacy values read by other branches.
		if v >= 0 && v <= 1 {
			return v * 100
		}
		return v
	case float32:
		value := float64(v)
		// Compare the float32 fraction against whole platform percentage points.
		if value >= 0 && value <= 1 {
			return value * 100
		}
		return value
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		// Failed observation parsing supplies zero, which cannot reach a validated
		// positive threshold or create a positive Anthropic window.
		value, err := v.Float64()
		if err != nil {
			return 0
		}
		// Decimal text distinguishes fraction-shaped JSON observations from integer
		// percentage points, so text "1" remains one percent here.
		if strings.Contains(v.String(), ".") && value >= 0 && value <= 1 {
			return value * 100
		}
		return value
	case string:
		trimmed := strings.TrimSpace(v)
		// A malformed Anthropic string contributes no observed usage, so its owning
		// window builder omits it or its threshold comparison fails.
		value, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return 0
		}
		// Apply the same textual distinction to string observations so an
		// integral-looking value does not accidentally become fully used quota.
		if strings.Contains(trimmed, ".") && value >= 0 && value <= 1 {
			return value * 100
		}
		return value
	default:
		return 0
	}
}

// schedulingPercentValue reads supported numeric forms as percentage points,
// returning zero for unsupported or unparseable values. It applies no
// fractional scaling or finite-value validation, so OpenAI one remains one
// percent.
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
		// Failed percentage parsing supplies zero, leaving OpenAI and Grok
		// observations below every validated positive scheduling threshold.
		value, err := v.Float64()
		if err != nil {
			return 0
		}
		return value
	case string:
		// Malformed percentage text contributes zero to a future-window check,
		// keeping it below every validated positive platform threshold.
		value, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0
		}
		return value
	default:
		return 0
	}
}

// parseSchedulingResetAt returns a copied reset time or parses a supported
// stored representation. Timestamp strings are trimmed; positive numeric
// inputs are interpreted as Unix seconds. In-range finite floating values
// discard fractional parts, and casts perform no range or finite-value
// validation. Absent, nonpositive, unsupported, and unparseable numeric
// values return nil.
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
		// An unparseable timestamp leaves the candidate without a deadline, which
		// prevents CandidateMatchesThreshold from selecting a provider hold.
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
		// Stored numeric deadlines use whole Unix seconds; fractional values retain
		// that representation by discarding their subsecond remainder.
		if value, err := v.Float64(); err == nil && value > 0 {
			ts := time.Unix(int64(value), 0)
			return &ts
		}
	case float64:
		// Floating numeric deadlines share the stored whole-Unix-second format.
		if v > 0 {
			ts := time.Unix(int64(v), 0)
			return &ts
		}
	case float32:
		// Float32 deadlines use the same whole-second scheduling comparison unit.
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

// ParseSchedulingTime delegates supported RFC3339 timestamp parsing to the
// timezone helper. Input is passed through without trimming, and unsupported
// formats return the shared syntax error for caller handling.
func ParseSchedulingTime(raw string) (time.Time, error) { return timezone.ParseFlexibleTimestamp(raw) }

// CloneThresholdTime returns a separately allocated time value or nil for an
// absent source. Keep the source stable during the scalar copy; modifying the
// returned value leaves the source unchanged.
func CloneThresholdTime(src *time.Time) *time.Time {
	if src == nil {
		return nil
	}
	value := *src
	return &value
}
