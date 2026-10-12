package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	OllamaCloudUsageDefaultIntervalMinutes = 60
	OllamaCloudUsageMinIntervalMinutes     = 15
	OllamaCloudUsageMaxIntervalMinutes     = 24 * 60
	OllamaCloudUsageDefaultDebounceMinutes = 1
	OllamaCloudUsageMinDebounceMinutes     = 1
	OllamaCloudUsageMaxDebounceMinutes     = 60
	OllamaCloudUsageMaxDelay               = 24 * time.Hour
)

func DefaultOllamaCloudUsageSettings() *OllamaCloudUsageSettings {
	return &OllamaCloudUsageSettings{
		Enabled:         false,
		IntervalMinutes: OllamaCloudUsageDefaultIntervalMinutes,
		DebounceMinutes: OllamaCloudUsageDefaultDebounceMinutes,
	}
}

func NormalizeOllamaCloudUsageSettings(settings *OllamaCloudUsageSettings) {
	if settings.IntervalMinutes < OllamaCloudUsageMinIntervalMinutes {
		settings.IntervalMinutes = OllamaCloudUsageMinIntervalMinutes
	}
	if settings.IntervalMinutes > OllamaCloudUsageMaxIntervalMinutes {
		settings.IntervalMinutes = OllamaCloudUsageMaxIntervalMinutes
	}
	if settings.DebounceMinutes <= 0 {
		settings.DebounceMinutes = OllamaCloudUsageDefaultDebounceMinutes
	}
	if settings.DebounceMinutes < OllamaCloudUsageMinDebounceMinutes {
		settings.DebounceMinutes = OllamaCloudUsageMinDebounceMinutes
	}
	if settings.DebounceMinutes > OllamaCloudUsageMaxDebounceMinutes {
		settings.DebounceMinutes = OllamaCloudUsageMaxDebounceMinutes
	}
}

func OllamaCloudUsageDurations(settings *OllamaCloudUsageSettings) (debounce, maxWait time.Duration) {
	normalized := DefaultOllamaCloudUsageSettings()
	if settings != nil {
		*normalized = *settings
	}
	NormalizeOllamaCloudUsageSettings(normalized)
	return time.Duration(normalized.DebounceMinutes) * time.Minute,
		time.Duration(normalized.IntervalMinutes) * time.Minute
}

// OllamaCloudUsageIsAutoRefreshDue checks activity-based timing; the caller checks whether refresh is enabled.
// groupLastUsedAt is MAX(last_used_at) across providers sharing the API key.
// After success, new activity uses min(lastUsed+debounce, fetchedAt+maxWait), subject to the minimum fetch
// interval.
// After failure, activity since last_attempt_at uses that minimum, then waits until next_refresh_at if later.
// Missing or invalid snapshots are immediately due; failure timing respects Retry-After and backoff.
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
		// Successful fetches are separated by at least OllamaCloudUsageMinFetchInterval,
		// even when the configured debounce is shorter.
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
		if !snapshot.NextRefreshAt.IsZero() && snapshot.NextRefreshAt.UTC().After(activityDue) {
			return snapshot.NextRefreshAt.UTC(), true
		}
		return activityDue, true
	default:
		return time.Time{}, true
	}
}

// MaxOllamaCloudUsageGroupLastUsed returns the latest nonzero activity time among group members.
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

func NextOllamaCloudUsageDelay(intervalMinutes, failureCount int, retryAfterDuration time.Duration, jitter func(int64) int64) time.Duration {
	minimumDelay := retryAfterDuration
	base := max(time.Duration(intervalMinutes)*time.Minute, OllamaCloudUsageMinIntervalMinutes*time.Minute)
	if failureCount > 0 {
		shift := min(failureCount-1, 6)
		base *= time.Duration(1 << shift)
	}
	if base > OllamaCloudUsageMaxDelay {
		base = OllamaCloudUsageMaxDelay
	}
	if retryAfterDuration > base {
		base = retryAfterDuration
	}
	jitterRange := min(base/10, 5*time.Minute)
	if jitterRange > 0 {
		base += time.Duration(jitter(int64(jitterRange)*2+1)) - jitterRange
	}
	if base < minimumDelay {
		return minimumDelay
	}
	if base < time.Minute {
		return time.Minute
	}
	return base
}

func ollamaEarlierTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// DecodeOllamaCloudUsageSettings parses JSON, applies defaults, and clamps supported ranges.
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

// EncodeOllamaCloudUsageSettings applies defaults in place, validates timing, and returns JSON.
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
	// Due time is min(lastUsed+debounce, fetchedAt+maxWait). If debounce reaches maxWait,
	// the debounce term cannot win for new activity, so reject that ineffective setting.
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
