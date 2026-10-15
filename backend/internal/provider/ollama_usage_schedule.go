package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// ollama_usage_schedule.go calculates Ollama usage refresh deadlines and
// validates persisted refresh settings.
// Ollama periodic usage queries require activity after the snapshot's reference
// attempt. Quiet activity schedules a debounce deadline, while continuous
// activity is bounded by maximum wait; successful snapshots additionally
// enforce fifteen-minute fetch spacing and ignore their stored NextRefreshAt.
//
// Failure snapshots combine the same activity rule with a retry floor derived
// from accumulated failures, Retry-After, and jitter. That floor can exceed the
// pre-jitter twenty-four-hour growth cap. Stored settings decoding restores
// defaults and clamps values, whereas administrator encoding rejects
// unsupported ranges and a debounce that can never precede maximum wait. These
// distinct paths make read-time recovery different from input validation.

// OllamaCloudUsage timing limits bound administrator settings, activity
// debounce, and pre-jitter retry growth. Default settings disable scanning
// while selecting a one-hour maximum activity wait and one-minute quiet period.
const (
	OllamaCloudUsageDefaultIntervalMinutes = 60             // default maximum wait in minutes
	OllamaCloudUsageMinIntervalMinutes     = 15             // minimum permitted interval in minutes
	OllamaCloudUsageMaxIntervalMinutes     = 24 * 60        // maximum permitted interval in minutes
	OllamaCloudUsageDefaultDebounceMinutes = 1              // default quiet period in minutes
	OllamaCloudUsageMinDebounceMinutes     = 1              // minimum quiet period in minutes
	OllamaCloudUsageMaxDebounceMinutes     = 60             // maximum quiet period in minutes
	OllamaCloudUsageMaxDelay               = 24 * time.Hour // exponential delay cap before jitter
)

// DefaultOllamaCloudUsageSettings returns a fresh disabled settings value with
// a sixty-minute interval and one-minute debounce. Callers can modify it before
// decoding or normalization without sharing stored settings.
func DefaultOllamaCloudUsageSettings() *OllamaCloudUsageSettings {
	return &OllamaCloudUsageSettings{
		Enabled:         false,
		IntervalMinutes: OllamaCloudUsageDefaultIntervalMinutes,
		DebounceMinutes: OllamaCloudUsageDefaultDebounceMinutes,
	}
}

// NormalizeOllamaCloudUsageSettings clamps the supplied nonnil value to the
// supported interval and debounce ranges. Nonpositive debounce receives its
// default; interval zero clamps to fifteen minutes, and the cross-field
// debounce ordering is not checked here.
func NormalizeOllamaCloudUsageSettings(settings *OllamaCloudUsageSettings) {
	// Bound the activity wait using supported setting limits.
	if settings.IntervalMinutes < OllamaCloudUsageMinIntervalMinutes {
		settings.IntervalMinutes = OllamaCloudUsageMinIntervalMinutes
	}
	if settings.IntervalMinutes > OllamaCloudUsageMaxIntervalMinutes {
		settings.IntervalMinutes = OllamaCloudUsageMaxIntervalMinutes
	}
	if settings.DebounceMinutes <= 0 {
		settings.DebounceMinutes = OllamaCloudUsageDefaultDebounceMinutes
	}
	// Keep the quiet period within supported setting limits.
	if settings.DebounceMinutes < OllamaCloudUsageMinDebounceMinutes {
		settings.DebounceMinutes = OllamaCloudUsageMinDebounceMinutes
	}
	if settings.DebounceMinutes > OllamaCloudUsageMaxDebounceMinutes {
		settings.DebounceMinutes = OllamaCloudUsageMaxDebounceMinutes
	}
}

// OllamaCloudUsageDurations returns normalized debounce and maximum-wait
// durations from a copied settings value. Nil uses defaults; the caller's value
// is left unchanged, and both minute counts are converted to time.Duration.
func OllamaCloudUsageDurations(settings *OllamaCloudUsageSettings) (debounce, maxWait time.Duration) {
	normalized := DefaultOllamaCloudUsageSettings()
	if settings != nil {
		*normalized = *settings
	}
	NormalizeOllamaCloudUsageSettings(normalized)
	// Convert both stored minute counts to scheduler durations.
	return time.Duration(normalized.DebounceMinutes) * time.Minute,
		time.Duration(normalized.IntervalMinutes) * time.Minute
}

// OllamaCloudUsageIsAutoRefreshDue reports whether a scheduled activity-based
// deadline has arrived. The caller separately checks global and provider
// enablement; groupLastUsedAt represents the latest activity among providers
// sharing the API Key.
func OllamaCloudUsageIsAutoRefreshDue(
	snapshot *OllamaCloudUsageSnapshot,
	groupLastUsedAt *time.Time,
	now time.Time,
	debounce, maxWait time.Duration,
) bool {
	dueAt, ok := OllamaCloudUsageAutoRefreshDueAt(snapshot, groupLastUsedAt, debounce, maxWait)
	if !ok {
		return false
	}
	return !now.Before(dueAt)
}

// OllamaCloudUsageAutoRefreshDueAt combines new group activity with snapshot
// timing and returns a deadline plus eligibility. Missing snapshots, unknown
// statuses, and absent reference timestamps return zero and true for an initial
// fetch. Nonpositive debounce or maximum wait selects defaults.
//
// Successful snapshots require activity after FetchedAt, take the earlier
// debounce or maximum-wait deadline, then enforce fifteen-minute fetch spacing.
// Failed or unauthorized snapshots require activity after LastAttemptAt and
// take the later of their activity deadline and NextRefreshAt. No new activity
// returns false; successful snapshots ignore NextRefreshAt.
func OllamaCloudUsageAutoRefreshDueAt(
	snapshot *OllamaCloudUsageSnapshot,
	groupLastUsedAt *time.Time,
	debounce, maxWait time.Duration,
) (time.Time, bool) {
	if debounce <= 0 {
		debounce = time.Duration(OllamaCloudUsageDefaultDebounceMinutes) * time.Minute
	}
	if maxWait <= 0 {
		maxWait = time.Duration(OllamaCloudUsageDefaultIntervalMinutes) * time.Minute
	}
	if snapshot == nil {
		return time.Time{}, true
	}
	switch snapshot.Status {
	case OllamaCloudUsageStatusOK:
		if snapshot.FetchedAt == nil || snapshot.FetchedAt.IsZero() {
			return time.Time{}, true
		}
		fetchedAt := snapshot.FetchedAt.UTC()
		if groupLastUsedAt == nil || !groupLastUsedAt.After(fetchedAt) {
			return time.Time{}, false
		}
		lastUsed := groupLastUsedAt.UTC()
		dueAt := ollamaEarlierTime(lastUsed.Add(debounce), fetchedAt.Add(maxWait))
		// A short quiet period can make activity due immediately after
		// a fetch;
		// retain the minimum successful spacing before admitting
		// another query.
		if floor := fetchedAt.Add(OllamaCloudUsageMinFetchInterval); dueAt.Before(floor) {
			return floor, true
		}
		return dueAt, true
	case OllamaCloudUsageStatusFailed, OllamaCloudUsageStatusUnauthorized:
		if snapshot.LastAttemptAt.IsZero() {
			return time.Time{}, true
		}
		lastAttempt := snapshot.LastAttemptAt.UTC()
		if groupLastUsedAt == nil || !groupLastUsedAt.After(lastAttempt) {
			return time.Time{}, false
		}
		lastUsed := groupLastUsedAt.UTC()
		activityDue := ollamaEarlierTime(lastUsed.Add(debounce), lastAttempt.Add(maxWait))
		// Failure backoff and Retry-After remain a floor even when new
		// group
		// activity would otherwise make this snapshot eligible sooner.
		if !snapshot.NextRefreshAt.IsZero() && snapshot.NextRefreshAt.UTC().After(activityDue) {
			return snapshot.NextRefreshAt.UTC(), true
		}
		return activityDue, true
	default:
		return time.Time{}, true
	}
}

// MaxOllamaCloudUsageGroupLastUsed returns a copied UTC timestamp for the
// latest nonzero LastUsedAt among group records. Missing or zero activity
// across the whole group returns nil for due-time calculation.
func MaxOllamaCloudUsageGroupLastUsed(providers []Record) *time.Time {
	var latest *time.Time
	for i := range providers {
		candidate := providers[i].LastUsedAt
		if candidate == nil || candidate.IsZero() {
			continue
		}
		if latest == nil || candidate.After(*latest) {
			ts := candidate.UTC()
			latest = &ts
		}
	}
	return latest
}

// NextOllamaCloudUsageDelay combines a configured interval, consecutive
// failures, Retry-After, and injected jitter into the next attempt delay.
// Callers supply a usable jitter callback returning an offset within its
// nanosecond bound; results are used directly.
//
// Failure growth doubles the base up to sixty-four interval multiples, then
// caps that base at twenty-four hours before considering Retry-After. Symmetric
// jitter is bounded by ten percent or five minutes. The final delay respects
// Retry-After and a one-minute floor; Retry-After or positive jitter can place
// it above the earlier twenty-four-hour cap.
func NextOllamaCloudUsageDelay(intervalMinutes, failureCount int, retryAfterDuration time.Duration, jitter func(int64) int64) time.Duration {
	minimumDelay := retryAfterDuration
	// Convert configured minutes and apply the fifteen-minute base floor
	// before
	// consecutive failures multiply that interval into their retry delay.
	base := max(time.Duration(intervalMinutes)*time.Minute, OllamaCloudUsageMinIntervalMinutes*time.Minute)
	if failureCount > 0 {
		// Cap exponential growth at sixty-four interval multiples.
		shift := min(failureCount-1, 6)
		base *= time.Duration(1 << shift)
	}
	// Bound exponential growth before adding upstream retry evidence.
	if base > OllamaCloudUsageMaxDelay {
		base = OllamaCloudUsageMaxDelay
	}
	// An upstream retry deadline can exceed the local growth cap; keep that
	// longer server-requested wait before calculating jitter.
	if retryAfterDuration > base {
		base = retryAfterDuration
	}
	// Spread retry times within ten percent or five minutes.
	jitterRange := min(base/10, 5*time.Minute)
	if jitterRange > 0 {
		// The callback receives a nanosecond range for a symmetric
		// offset;
		// subtracting its midpoint allows either a shorter or longer
		// delay.
		base += time.Duration(jitter(int64(jitterRange)*2+1)) - jitterRange
	}
	// Negative jitter cannot shorten the upstream Retry-After floor, so use
	// that server-requested minimum even after the randomized offset.
	if base < minimumDelay {
		return minimumDelay
	}
	// Keep retry scheduling at least one minute apart.
	if base < time.Minute {
		return time.Minute
	}
	return base
}

// ollamaEarlierTime returns the earlier deadline, selecting b when both
// instants are equal.
func ollamaEarlierTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// DecodeOllamaCloudUsageSettings parses JSON over disabled defaults and
// normalizes the result. Blank input returns defaults, zero timing fields
// restore defaults before clamping, and malformed JSON returns an error;
// cross-field debounce ordering is not rejected here.
func DecodeOllamaCloudUsageSettings(raw string) (*OllamaCloudUsageSettings, error) {
	defaults := DefaultOllamaCloudUsageSettings()
	if strings.TrimSpace(raw) == "" {
		return defaults, nil
	}
	settings := *defaults
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return nil, fmt.Errorf("parse Ollama Cloud usage settings: %w", err)
	}
	if settings.IntervalMinutes == 0 {
		settings.IntervalMinutes = defaults.IntervalMinutes
	}
	if settings.DebounceMinutes == 0 {
		settings.DebounceMinutes = defaults.DebounceMinutes
	}
	NormalizeOllamaCloudUsageSettings(&settings)
	return &settings, nil
}

// EncodeOllamaCloudUsageSettings validates administrator timing and returns
// JSON, mutating the supplied settings during defaulting and normalization. Nil
// is rejected, and zero debounce is defaulted even if later validation fails.
//
// Interval and debounce must fall within their own ranges before normalization,
// and debounce must be shorter than interval. For activity after the reference
// fetch, a debounce at least as long as maximum wait can never supply the
// earlier deadline, so that ineffective setting is rejected. Successful
// validation normalizes the same object before marshaling.
func EncodeOllamaCloudUsageSettings(settings *OllamaCloudUsageSettings) (string, error) {
	if settings == nil {
		return "", infraerrors.BadRequest("INVALID_OLLAMA_CLOUD_USAGE_SETTINGS", "settings cannot be nil")
	}
	if settings.DebounceMinutes == 0 {
		// Zero debounce uses the default for omitted request values.
		settings.DebounceMinutes = OllamaCloudUsageDefaultDebounceMinutes
	}
	if settings.IntervalMinutes < OllamaCloudUsageMinIntervalMinutes || settings.IntervalMinutes > OllamaCloudUsageMaxIntervalMinutes {
		return "", infraerrors.BadRequest(
			"INVALID_OLLAMA_CLOUD_USAGE_INTERVAL",
			fmt.Sprintf("interval_minutes must be between %d and %d", OllamaCloudUsageMinIntervalMinutes, OllamaCloudUsageMaxIntervalMinutes),
		)
	}
	if settings.DebounceMinutes < OllamaCloudUsageMinDebounceMinutes || settings.DebounceMinutes > OllamaCloudUsageMaxDebounceMinutes {
		return "", infraerrors.BadRequest(
			"INVALID_OLLAMA_CLOUD_USAGE_DEBOUNCE",
			fmt.Sprintf("debounce_minutes must be between %d and %d", OllamaCloudUsageMinDebounceMinutes, OllamaCloudUsageMaxDebounceMinutes),
		)
	}
	// New activity follows the reference attempt, so a debounce as long as
	// maximum wait cannot select an earlier activity deadline; reject that
	// ineffective administrator setting.
	if settings.DebounceMinutes >= settings.IntervalMinutes {
		return "", infraerrors.BadRequest(
			"INVALID_OLLAMA_CLOUD_USAGE_DEBOUNCE",
			fmt.Sprintf("debounce_minutes (%d) must be less than interval_minutes (%d)", settings.DebounceMinutes, settings.IntervalMinutes),
		)
	}
	NormalizeOllamaCloudUsageSettings(settings)
	data, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("marshal Ollama Cloud usage settings: %w", err)
	}
	return string(data), nil
}
