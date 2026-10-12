package pricing

import (
	"strings"
)

// Video billing recognizes three output-resolution tiers and uses a supported
// duration from one through fifteen seconds. Submission and settlement share
// duration normalization, with eight seconds for a missing duration and 480p
// for an unresolved billing resolution.

const (
	// These three normalized resolution keys select USD-per-second prices and
	// identify the supported output tiers for video billing.
	VideoBillingResolution480P  = "480p"  // 480p output-price lookup key
	VideoBillingResolution720P  = "720p"  // 720p output-price lookup key
	VideoBillingResolution1080P = "1080p" // 1080p output-price lookup key

	// Video durations share one supported range and default across submission and
	// charging, so a missing or excessive length receives the same normalized
	// value.
	VideoBillingMinDurationSeconds     = 1  // minimum submitted and billable seconds
	VideoBillingMaxDurationSeconds     = 15 // maximum submitted and billable seconds
	VideoBillingDefaultDurationSeconds = 8  // missing-duration seconds for submission and billing
)

// NormalizeVideoBillingDurationSecondsOrDefault returns the shared submission
// and billing duration. Nonpositive values use eight seconds; positive values
// are clamped to the supported one-to-fifteen-second range. Callers use this
// result consistently when submitting and charging videos.
func NormalizeVideoBillingDurationSecondsOrDefault(durationSeconds int) int {
	if durationSeconds <= 0 {
		return VideoBillingDefaultDurationSeconds
	}
	if durationSeconds < VideoBillingMinDurationSeconds {
		return VideoBillingMinDurationSeconds
	}
	// Cap duration at the same supported limit used by video submission.
	if durationSeconds > VideoBillingMaxDurationSeconds {
		return VideoBillingMaxDurationSeconds
	}
	return durationSeconds
}

// LookupVideoBillingResolution returns a supported resolution label and its
// presence. Case, surrounding whitespace, and listed aliases are normalized.
// Unrecognized text returns false with an empty label, allowing callers to
// distinguish lookup failure before applying a default.
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

// NormalizeVideoBillingResolutionOrDefault returns a recognized resolution or
// the 480p billing default. Absent and unsupported values use that lowest tier;
// callers retain raw response text separately when the original resolution
// matters.
func NormalizeVideoBillingResolutionOrDefault(resolution string) string {
	if normalized, ok := LookupVideoBillingResolution(resolution); ok {
		return normalized
	}
	// Unrecognized response text still needs a catalog lookup key, so use the
	// shared lowest supported billing resolution.
	return VideoBillingResolution480P
}
