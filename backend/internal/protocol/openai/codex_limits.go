package openai

// codex_limits.go assigns primary and secondary quota observations to short and
// long duration categories.
// Primary and secondary quota fields do not determine which observation receives
// the five-hour or seven-day label. Duration evidence maps each utilization
// together with its corresponding reset and window fields, keeping measurements
// from different upstream windows associated during normalization.
//
// With both durations present, the shorter receives the five-hour category and
// ties put primary in seven days. One duration uses a six-hour threshold; no
// durations use primary as seven days and secondary as five hours. Those are
// classification rules rather than validation of an exact window length.
// Normalized fields retain the snapshot's pointers, so consumers share the
// pointed-to measurements and preserve their immutability while readers use them.

// OpenAICodexUsageSnapshot holds optional quota observations sampled from response headers.
type OpenAICodexUsageSnapshot struct {
	// PrimaryUsedPercent, PrimaryResetAfterSeconds, and PrimaryWindowMinutes
	// describe the same upstream primary window. Nil distinguishes an absent
	// observation from zero, and Normalize moves these pointers as one group.
	PrimaryUsedPercent       *float64 `json:"primary_used_percent,omitempty"`        // percent
	PrimaryResetAfterSeconds *int     `json:"primary_reset_after_seconds,omitempty"` // seconds
	PrimaryWindowMinutes     *int     `json:"primary_window_minutes,omitempty"`      // minutes
	// SecondaryUsedPercent, SecondaryResetAfterSeconds, and SecondaryWindowMinutes
	// describe the same secondary window. Nil distinguishes absence from zero,
	// and Normalize retains their association when selecting the canonical category.
	SecondaryUsedPercent        *float64 `json:"secondary_used_percent,omitempty"`         // percent
	SecondaryResetAfterSeconds  *int     `json:"secondary_reset_after_seconds,omitempty"`  // seconds
	SecondaryWindowMinutes      *int     `json:"secondary_window_minutes,omitempty"`       // minutes
	PrimaryOverSecondaryPercent *float64 `json:"primary_over_secondary_percent,omitempty"` // reported primary/secondary percentage
	UpdatedAt                   string   `json:"updated_at,omitempty"`                     // snapshot update timestamp
}

// NormalizedCodexLimits associates sampled quota fields with five-hour and
// seven-day categories.
type NormalizedCodexLimits struct {
	// Used5hPercent, Reset5hSeconds, and Window5hMinutes share one selected
	// source window; Used7dPercent, Reset7dSeconds, and Window7dMinutes share
	// the other. These pointers alias snapshot observations, and nil remains
	// an absent measurement rather than an observed zero value.
	Used5hPercent   *float64 // percent
	Reset5hSeconds  *int     // seconds
	Window5hMinutes *int     // minutes
	Used7dPercent   *float64 // percent
	Reset7dSeconds  *int     // seconds
	Window7dMinutes *int     // minutes
}

// Normalize classifies primary and secondary observations into five-hour and
// seven-day categories while retaining each window's utilization, reset, and
// duration pointers together. Nil receivers return nil; empty snapshots produce
// a nonnil result with absent measurements.
//
// Both known durations are ordered comparatively, with ties assigning primary
// to seven days. A single known duration uses 360 minutes as the short-category
// threshold; absent durations use the legacy primary-seven-day mapping. Values
// are classified as supplied, including nonpositive durations. The output shares
// pointed-to values with the snapshot, and metadata outside the window groups
// is not included in the normalized result.
func (s *OpenAICodexUsageSnapshot) Normalize() *NormalizedCodexLimits {
	if s == nil {
		return nil
	}

	result := &NormalizedCodexLimits{}

	primaryMins := 0
	secondaryMins := 0
	hasPrimaryWindow := false
	hasSecondaryWindow := false

	if s.PrimaryWindowMinutes != nil {
		primaryMins = *s.PrimaryWindowMinutes
		hasPrimaryWindow = true
	}
	if s.SecondaryWindowMinutes != nil {
		secondaryMins = *s.SecondaryWindowMinutes
		hasSecondaryWindow = true
	}

	use5hFromPrimary := false
	use7dFromPrimary := false

	if hasPrimaryWindow && hasSecondaryWindow {
		// Relative duration evidence keeps quota measurements with their own
		// reset window. Equal lengths follow the primary-seven-day convention
		// used for ambiguous ordering.
		if primaryMins < secondaryMins {
			use5hFromPrimary = true
		} else {
			use7dFromPrimary = true
		}
	} else if hasPrimaryWindow {
		// A missing secondary duration prevents relative ordering. Classify
		// the known primary against six hours and place its peer in the other category.
		if primaryMins <= 360 {
			use5hFromPrimary = true
		} else {
			use7dFromPrimary = true
		}
	} else if hasSecondaryWindow {
		// A missing primary duration leaves the secondary as the known input.
		// Classify it against six hours while keeping primary in the opposite category.
		if secondaryMins <= 360 {
			use7dFromPrimary = true
		} else {
			use5hFromPrimary = true
		}
	} else {
		// Without either duration there is no relative ordering evidence.
		// Use the legacy primary-seven-day association so partial measurements
		// still reach the expected display category.
		use7dFromPrimary = true
	}

	// Move utilization, reset, and duration pointers together for each selected
	// source window so normalization cannot attach one window's reset to the
	// other's utilization.
	if use5hFromPrimary {
		result.Used5hPercent = s.PrimaryUsedPercent
		result.Reset5hSeconds = s.PrimaryResetAfterSeconds
		result.Window5hMinutes = s.PrimaryWindowMinutes
		result.Used7dPercent = s.SecondaryUsedPercent
		result.Reset7dSeconds = s.SecondaryResetAfterSeconds
		result.Window7dMinutes = s.SecondaryWindowMinutes
	} else if use7dFromPrimary {
		result.Used7dPercent = s.PrimaryUsedPercent
		result.Reset7dSeconds = s.PrimaryResetAfterSeconds
		result.Window7dMinutes = s.PrimaryWindowMinutes
		result.Used5hPercent = s.SecondaryUsedPercent
		result.Reset5hSeconds = s.SecondaryResetAfterSeconds
		result.Window5hMinutes = s.SecondaryWindowMinutes
	}

	return result
}
