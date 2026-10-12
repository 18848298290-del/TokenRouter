package openai

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// openAIImageTryAgainPattern captures the numeric delay and time unit in retry hints.
var openAIImageTryAgainPattern = regexp.MustCompile(`(?i)try again in\s+([0-9]+(?:\.[0-9]+)?)\s*(ms|s|sec|secs|second|seconds|m|min|mins|minute|minutes)`)

// IsImageCapabilityLossError recognizes rejection of an image_generation tool choice.
// Use it for gateway-built image requests, which contain a matching tool declaration;
// in that context a missing-tool error indicates provider capability loss.
func IsImageCapabilityLossError(statusCode int, body []byte) bool {
	if statusCode != http.StatusBadRequest || len(body) == 0 {
		return false
	}
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "image_generation") &&
		strings.Contains(lower, "not found in 'tools' parameter")
}

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

func ParseRetryAfterResetTime(headers http.Header, now time.Time) *time.Time {
	if headers == nil {
		return nil
	}
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if raw == "" {
		return nil
	}
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
		resetAt := now.Add(time.Duration(seconds * float64(time.Second)))
		return &resetAt
	}
	if parsed, err := http.ParseTime(raw); err == nil {
		return &parsed
	}
	return nil
}

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
