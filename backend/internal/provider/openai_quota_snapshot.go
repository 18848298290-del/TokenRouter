package provider

import (
	"time"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Codex usage observations carry primary and secondary window measurements whose
// labels can differ from five-hour and seven-day categories. Extra updates keep
// the raw window fields alongside normalized fields, including supplied zero
// values, so diagnostics and scheduling readers can use their respective forms.
//
// Absolute reset timestamps share one snapshot base time. A usable UpdatedAt
// wins over the caller's fallback time, and malformed or absent timestamps use
// that fallback as supplied. Relative negative reset counts remain in Extra for
// observation, while absolute timestamps clamp those counts to the base time.
// Formatting uses whole-second RFC3339 text with the base time's zone; interval
// arithmetic uses time.Duration and requires representable caller inputs.

// CodexSnapshotBaseTime returns the parsed snapshot timestamp or fallback as
// supplied. Nil snapshots and absent or malformed UpdatedAt values select
// fallback. The helper does not read a clock or replace a zero fallback time.
func CodexSnapshotBaseTime(snapshot *wire.OpenAICodexUsageSnapshot, fallback time.Time) time.Time {
	if snapshot == nil {
		return fallback
	}
	if snapshot.UpdatedAt == "" {
		return fallback
	}
	base, err := time.Parse(time.RFC3339, snapshot.UpdatedAt)
	if err != nil {
		// Malformed observation time cannot anchor a relative reset. The update
		// builder uses this supplied fallback for both its timestamp and derived reset dates.
		return fallback
	}
	return base
}

// CodexResetAtRFC3339 returns a new string containing base plus the reset interval.
// Nil intervals return nil, and negative counts select zero seconds. Supply
// intervals representable in time.Duration; multiplication is direct. RFC3339
// formatting drops subseconds and retains the resulting time's zone offset.
func CodexResetAtRFC3339(base time.Time, resetAfterSeconds *int) *string {
	if resetAfterSeconds == nil {
		return nil
	}
	// Negative reset observations yield the base timestamp, avoiding a past date
	// caused by that negative count while raw observations retain the supplied seconds.
	sec := max(*resetAfterSeconds, 0)
	// Convert seconds to duration and emit whole-second text.
	resetAt := base.Add(time.Duration(sec) * time.Second).Format(time.RFC3339)
	return &resetAt
}

// BuildCodexUsageExtraUpdates returns a new Extra patch for a quota observation.
// Nil snapshots return nil; other snapshots include the chosen update timestamp
// even when every measurement is absent. Keep snapshot pointer values stable
// during this read, and supply representable reset intervals.
//
// Raw primary and secondary measurements are copied by value alongside the
// categories supplied by Normalize, retaining used percentages as reported.
// Present zeros and negative relative counts remain observable. Absolute resets
// use one parsed UpdatedAt or fallbackNow and clamp negative counts through
// CodexResetAtRFC3339. Omitted fields are absent from the patch, so merging callers
// decide whether earlier stored measurements remain available.
func BuildCodexUsageExtraUpdates(snapshot *wire.OpenAICodexUsageSnapshot, fallbackNow time.Time) map[string]any {
	if snapshot == nil {
		return nil
	}

	baseTime := CodexSnapshotBaseTime(snapshot, fallbackNow)
	updates := make(map[string]any)

	// Keep raw primary and secondary measurements alongside canonical categories
	// because upstream window positions can swap and diagnostics need their supplied values.
	if snapshot.PrimaryUsedPercent != nil {
		updates["codex_primary_used_percent"] = *snapshot.PrimaryUsedPercent
	}
	if snapshot.PrimaryResetAfterSeconds != nil {
		updates["codex_primary_reset_after_seconds"] = *snapshot.PrimaryResetAfterSeconds
	}
	if snapshot.PrimaryWindowMinutes != nil {
		updates["codex_primary_window_minutes"] = *snapshot.PrimaryWindowMinutes
	}
	if snapshot.SecondaryUsedPercent != nil {
		updates["codex_secondary_used_percent"] = *snapshot.SecondaryUsedPercent
	}
	if snapshot.SecondaryResetAfterSeconds != nil {
		updates["codex_secondary_reset_after_seconds"] = *snapshot.SecondaryResetAfterSeconds
	}
	if snapshot.SecondaryWindowMinutes != nil {
		updates["codex_secondary_window_minutes"] = *snapshot.SecondaryWindowMinutes
	}
	if snapshot.PrimaryOverSecondaryPercent != nil {
		updates["codex_primary_over_secondary_percent"] = *snapshot.PrimaryOverSecondaryPercent
	}
	updates["codex_usage_updated_at"] = baseTime.Format(time.RFC3339)

	// Normalize keeps utilization, duration, and reset observations associated
	// while selecting categories. Copy their values here so Extra does not retain
	// snapshot pointers.
	if normalized := snapshot.Normalize(); normalized != nil {
		if normalized.Used5hPercent != nil {
			updates["codex_5h_used_percent"] = *normalized.Used5hPercent
		}
		if normalized.Reset5hSeconds != nil {
			updates["codex_5h_reset_after_seconds"] = *normalized.Reset5hSeconds
		}
		if normalized.Window5hMinutes != nil {
			updates["codex_5h_window_minutes"] = *normalized.Window5hMinutes
		}
		if normalized.Used7dPercent != nil {
			updates["codex_7d_used_percent"] = *normalized.Used7dPercent
		}
		if normalized.Reset7dSeconds != nil {
			updates["codex_7d_reset_after_seconds"] = *normalized.Reset7dSeconds
		}
		if normalized.Window7dMinutes != nil {
			updates["codex_7d_window_minutes"] = *normalized.Window7dMinutes
		}
		if reset5hAt := CodexResetAtRFC3339(baseTime, normalized.Reset5hSeconds); reset5hAt != nil {
			updates["codex_5h_reset_at"] = *reset5hAt
		}
		if reset7dAt := CodexResetAtRFC3339(baseTime, normalized.Reset7dSeconds); reset7dAt != nil {
			updates["codex_7d_reset_at"] = *reset7dAt
		}
	}

	return updates
}
