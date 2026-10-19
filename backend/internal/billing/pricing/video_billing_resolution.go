package pricing

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// video_billing_resolution.go normalizes video price labels and billable seconds.
// Requests, saved task observations, price cards, and catalog tables share these
// labels so a custom output dimension reaches the same price during display and
// settlement. Existing SD, HD, and full-HD aliases select their canonical tiers;
// other nonempty labels remain independent dimensions after case normalization.
//
// Missing resolution selects 480p, while a missing duration selects eight seconds.
// Positive durations retain the observed number of seconds, including long videos.
// Validation limits labels to the persistence column's character capacity and
// rejects control characters before configuration or new tasks can be accepted.

const (
	// These canonical labels retain the existing resolution aliases used by saved
	// requests and configured video prices throughout the billing flow.
	VideoBillingResolution480P  = "480p"  // SD output price key
	VideoBillingResolution720P  = "720p"  // HD output price key
	VideoBillingResolution1080P = "1080p" // full-HD output price key

	// Video defaults fill missing observations; the label limit matches stored
	// pricing tiers and the usage record's resolution column.
	VideoBillingDefaultDurationSeconds = 8  // missing duration in seconds
	VideoBillingResolutionMaxLength    = 50 // maximum resolution label characters
)

// NormalizeVideoBillingDurationSecondsOrDefault returns positive seconds unchanged.
// Nonpositive observations use eight seconds. Provider submission limits belong
// to the corresponding protocol; billing retains the completed output duration.
func NormalizeVideoBillingDurationSecondsOrDefault(durationSeconds int) int {
	if durationSeconds <= 0 {
		return VideoBillingDefaultDurationSeconds
	}
	return durationSeconds
}

// LookupVideoBillingResolution returns a canonical label and whether it is valid.
// Empty text, control characters, invalid UTF-8, and labels longer than the stored
// column permits return false. Unknown valid labels retain their normalized text.
func LookupVideoBillingResolution(resolution string) (string, bool) {
	label := strings.ToLower(strings.TrimSpace(resolution))
	switch label {
	case "480", "480p", "sd":
		label = VideoBillingResolution480P
	case "720", "720p", "hd":
		label = VideoBillingResolution720P
	case "1080", "1080p", "full_hd", "full-hd", "fhd":
		label = VideoBillingResolution1080P
	}
	if label == "" || !utf8.ValidString(label) || utf8.RuneCountInString(label) > VideoBillingResolutionMaxLength || strings.ContainsFunc(label, unicode.IsControl) {
		return "", false
	}
	return label, true
}

// NormalizeVideoBillingResolutionOrDefault canonicalizes aliases and custom labels.
// Empty text selects 480p. Invalid nonempty text stays distinct from that default;
// request and configuration callers validate it before accepting new work.
func NormalizeVideoBillingResolutionOrDefault(resolution string) string {
	if normalized, ok := LookupVideoBillingResolution(resolution); ok {
		return normalized
	}
	label := strings.ToLower(strings.TrimSpace(resolution))
	if label == "" {
		return VideoBillingResolution480P
	}
	return label
}

// ValidateVideoBillingResolution checks a supplied or defaulted task resolution.
// Omission is valid and selects 480p during normalization. Invalid labels return
// a descriptive error before callers submit work or store its billing snapshot.
func ValidateVideoBillingResolution(resolution string) error {
	if _, ok := LookupVideoBillingResolution(NormalizeVideoBillingResolutionOrDefault(resolution)); !ok {
		return fmt.Errorf("video resolution must contain at most %d characters and no control characters", VideoBillingResolutionMaxLength)
	}
	return nil
}

// NormalizeVideoPrices returns an independently owned canonical price table.
// Every key must be a valid nonempty resolution and every amount finite and
// nonnegative. Alias collisions fail even when their supplied amounts agree.
// For example, HD and 720p describe one key and cannot coexist in a published
// table. The input map remains unchanged on both success and failure, so a
// rejected catalog candidate cannot alter an active snapshot. Nil input retains
// nil storage, and explicit zero prices remain present in the returned map.
func NormalizeVideoPrices(prices MediaPrices) (MediaPrices, error) {
	if prices == nil {
		return nil, nil
	}
	normalized := make(MediaPrices, len(prices))
	for key, price := range prices {
		label, ok := LookupVideoBillingResolution(key)
		if !ok || !validAmount(price) {
			return nil, fmt.Errorf("invalid video_prices.%s", key)
		}
		if _, exists := normalized[label]; exists {
			return nil, fmt.Errorf("duplicate video resolution %q", label)
		}
		normalized[label] = price
	}
	return normalized, nil
}
