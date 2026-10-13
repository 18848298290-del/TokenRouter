package openai

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Image-specific throttling is distinguished from ordinary text rate limits by
// a 429 response containing image markers. A separate 400 predicate recognizes
// loss of a declared image_generation tool, allowing gateway-built image
// requests to identify capability rejection.
//
// Cooldown readers accept Retry-After seconds or dates and image-message delays
// with millisecond, second, or minute units. Missing and unusable values return
// no reset or a zero duration. These parsers extract upstream evidence; callers
// decide whether it applies to the selected image model or provider.

// openAIImageTryAgainPattern captures decimal image retry hints with supported
// unit spellings, allowing cooldown consumers to interpret the vendor message duration.
var openAIImageTryAgainPattern = regexp.MustCompile(`(?i)try again in\s+([0-9]+(?:\.[0-9]+)?)\s*(ms|s|sec|secs|second|seconds|m|min|mins|minute|minutes)`)

// IsImageCapabilityLossError recognizes a 400 rejection of image_generation in
// tool choice. Use it for gateway-built image requests with a matching tool
// declaration, where missing-tool evidence can represent provider capability
// loss.
func IsImageCapabilityLossError(statusCode int, body []byte) bool {
	if statusCode != http.StatusBadRequest || len(body) == 0 {
		return false
	}
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "image_generation") &&
		strings.Contains(lower, "not found in 'tools' parameter")
}

// IsImageRateLimitError reports whether a nonempty 429 body contains recognized
// image-limit markers. Other status codes and text-only throttling evidence
// return false so image-specific cooldown handling remains limited to matching
// responses.
func IsImageRateLimitError(statusCode int, body []byte) bool {
	if statusCode != http.StatusTooManyRequests || len(body) == 0 {
		return false
	}
	lower := strings.ToLower(string(body))
	for _, marker := range []string{
		"for limit gpt-image",
		"input-images per min",
		"gpt-image-2-codex",
		"gpt-image",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// ParseRetryAfterResetTime reads a numeric seconds delay or HTTP-date from
// Retry-After. Missing or unparseable values return nil; parsed numeric values
// are applied as supplied, so callers handle nonfuture or unsuitable reset
// times.
func ParseRetryAfterResetTime(headers http.Header, now time.Time) *time.Time {
	if headers == nil {
		return nil
	}
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if raw == "" {
		return nil
	}
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
		// Retry-After numeric values are seconds relative to the supplied clock.
		resetAt := now.Add(time.Duration(seconds * float64(time.Second)))
		return &resetAt
	}
	if parsed, err := http.ParseTime(raw); err == nil {
		return &parsed
	}
	return nil
}

// ParseImageTryAgainCooldown extracts a positive decimal delay from image
// retry-message text. Recognized units become a duration, while missing,
// unparseable, nonpositive, or unsupported values return zero for the caller to
// handle.
func ParseImageTryAgainCooldown(body []byte) time.Duration {
	if len(body) == 0 {
		return 0
	}
	match := openAIImageTryAgainPattern.FindSubmatch(body)
	if len(match) != 3 {
		return 0
	}
	value, err := strconv.ParseFloat(string(match[1]), 64)
	if err != nil || value <= 0 {
		return 0
	}
	// Convert the vendor unit into nanosecond duration precision.
	switch strings.ToLower(string(match[2])) {
	case "ms":
		return time.Duration(value * float64(time.Millisecond))
	case "s", "sec", "secs", "second", "seconds":
		return time.Duration(value * float64(time.Second))
	case "m", "min", "mins", "minute", "minutes":
		return time.Duration(value * float64(time.Minute))
	default:
		return 0
	}
}
