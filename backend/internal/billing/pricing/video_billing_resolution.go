package pricing

import (
	"strings"
)

const (
	VideoBillingResolution480P  = "480p"
	VideoBillingResolution720P  = "720p"
	VideoBillingResolution1080P = "1080p"

	// Video billing uses a 1-15 second duration range and an 8-second default.
	// Submission and billing must use the same duration to charge user-selected lengths consistently.
	VideoBillingMinDurationSeconds     = 1
	VideoBillingMaxDurationSeconds     = 15
	VideoBillingDefaultDurationSeconds = 8
)

// NormalizeVideoBillingDurationSecondsOrDefault returns the duration used for billing.
// Nonpositive durations default to 8 seconds; positive durations are clamped to the supported range.
func NormalizeVideoBillingDurationSecondsOrDefault(durationSeconds int) int {
	if durationSeconds <= 0 {
		return VideoBillingDefaultDurationSeconds
	}
	if durationSeconds < VideoBillingMinDurationSeconds {
		return VideoBillingMinDurationSeconds
	}
	if durationSeconds > VideoBillingMaxDurationSeconds {
		return VideoBillingMaxDurationSeconds
	}
	return durationSeconds
}

// LookupVideoBillingResolution recognizes resolution aliases and reports unknown values for callers to handle.
func LookupVideoBillingResolution(resolution string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(resolution)) {
	case "480", "480p", "sd":
		return VideoBillingResolution480P, true
	case "720", "720p", "hd":
		return VideoBillingResolution720P, true
	case "1080", "1080p", "full_hd", "full-hd", "fhd":
		return VideoBillingResolution1080P, true
	default:
		return "", false
	}
}

// NormalizeVideoBillingResolutionOrDefault selects a billing resolution,
// using the lowest tier when the upstream resolution is absent or unrecognized.
func NormalizeVideoBillingResolutionOrDefault(resolution string) string {
	if normalized, ok := LookupVideoBillingResolution(resolution); ok {
		return normalized
	}
	return VideoBillingResolution480P
}
