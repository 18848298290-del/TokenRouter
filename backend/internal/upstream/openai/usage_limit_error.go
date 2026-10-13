package openai

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Usage-limit responses can report an absolute Unix reset timestamp, a relative
// seconds offset, or an OpenCode Go readable duration. Recognized error types
// gate those interpretations, and absolute reset evidence takes precedence
// before relative parsing consults the supplied clock.
//
// Readable duration parsing applies only to GoUsageLimitError and can sum
// several positive unit components after resets in. It rejects overflowing
// duration arithmetic and retains a successfully parsed prefix when later text
// has no duration component. Structured numeric fields are converted as
// supplied, so callers decide whether a returned reset is future and applicable
// to their quota window. Plan metadata is read only from the supported OpenAI
// structured limit types.

// These patterns locate the OpenCode Go reset marker and contiguous positive
// duration components used when structured limit responses omit reset timestamp fields.
var (
	openCodeGoUsageLimitResetPattern        = regexp.MustCompile(`(?i)\bresets\s+in\s+`)
	openCodeGoUsageLimitDurationPartPattern = regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)?)\s*(s|sec|secs|second|seconds|m|min|mins|minute|minutes|h|hr|hrs|hour|hours|d|day|days|w|week|weeks)\b`)
)

// ParseUsageLimitResetTime returns a Unix-seconds reset for recognized
// structured limit errors. It accepts numeric or integer-string absolute resets
// before relative seconds and returns nil when no supported evidence can be
// read. The required now callback is used only by relative paths.
//
// GoUsageLimitError can fall back to positive readable duration components in
// its message. Other types do not use that text fallback. Structured numbers
// are decoded as float64 and converted to integers without range or future-time
// validation, while readable components reject duration overflow. This
// precedence keeps an explicit timestamp authoritative and gives callers the
// observed reset value to assess for their window.
func ParseUsageLimitResetTime(body []byte, now func() time.Time) *int64 {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}

	errObj, ok := parsed["error"].(map[string]any)
	if !ok {
		return nil
	}

	errType, _ := errObj["type"].(string)
	if errType != "usage_limit_reached" && errType != "rate_limit_exceeded" && errType != "GoUsageLimitError" {
		return nil
	}

	// An explicit Unix reset takes precedence over relative offsets, keeping
	// provider-supplied timestamps authoritative when both representations appear in one error.
	if resetsAt, ok := errObj["resets_at"].(float64); ok {
		ts := int64(resetsAt)
		return &ts
	}
	if resetsAt, ok := errObj["resets_at"].(string); ok {
		if ts, err := strconv.ParseInt(resetsAt, 10, 64); err == nil {
			return &ts
		}
	}

	// A relative offset is considered after absolute parsing fails; apply it
	// to the caller clock so the same response can be interpreted at observation time.
	if resetsInSeconds, ok := errObj["resets_in_seconds"].(float64); ok {
		ts := now().Unix() + int64(resetsInSeconds)
		return &ts
	}
	if resetsInSeconds, ok := errObj["resets_in_seconds"].(string); ok {
		if sec, err := strconv.ParseInt(resetsInSeconds, 10, 64); err == nil {
			ts := now().Unix() + sec
			return &ts
		}
	}

	// OpenCode Go can omit structured reset fields and retain readable duration
	// evidence. Limit this fallback to its typed error so unrelated messages cannot create a reset.
	if errType == "GoUsageLimitError" {
		message, _ := errObj["message"].(string)
		if resetAfter := parseOpenCodeGoUsageLimitResetDuration(message); resetAfter > 0 {
			ts := now().Add(resetAfter).Unix()
			return &ts
		}
	}

	return nil
}

// parseOpenCodeGoUsageLimitResetDuration sums positive duration components
// after the first resets-in marker. Missing markers, unusable positive
// components, overflow, or values rounding below duration precision return
// zero. Unit aliases are recognized case-insensitively.
//
// Parsing proceeds from the start of each remaining component and stops when
// later text no longer matches. A previously accumulated duration is retained
// at that point, so punctuation or unrelated suffix text can follow the usable
// prefix. Each part and the running total are checked before addition,
// preventing arithmetic overflow from producing an apparently valid reset
// delay.
func parseOpenCodeGoUsageLimitResetDuration(message string) time.Duration {
	resetPrefix := openCodeGoUsageLimitResetPattern.FindStringIndex(message)
	if resetPrefix == nil {
		return 0
	}

	remainder := message[resetPrefix[1]:]
	var total time.Duration
	for {
		remainder = strings.TrimSpace(remainder)
		matches := openCodeGoUsageLimitDurationPartPattern.FindStringSubmatchIndex(remainder)
		if matches == nil {
			break
		}

		value, err := strconv.ParseFloat(remainder[matches[2]:matches[3]], 64)
		if err != nil || value <= 0 {
			return 0
		}

		unit := openCodeGoUsageLimitDurationUnit(remainder[matches[4]:matches[5]])
		if unit <= 0 {
			return 0
		}

		// Reject overflowing converted parts and totals before duration addition.
		const maxDuration = time.Duration(1<<63 - 1)
		if value >= float64(maxDuration)/float64(unit) {
			return 0
		}
		part := time.Duration(value * float64(unit))
		if part <= 0 || total > maxDuration-part {
			return 0
		}
		total += part
		remainder = remainder[matches[1]:]
	}

	return total
}

// openCodeGoUsageLimitDurationUnit maps recognized unit spellings to fixed
// durations. Days use twenty-four hours and weeks use seven such days;
// unsupported values return zero and no calendar or timezone arithmetic is
// applied.
func openCodeGoUsageLimitDurationUnit(raw string) time.Duration {
	switch strings.ToLower(raw) {
	case "s", "sec", "secs", "second", "seconds":
		return time.Second
	case "m", "min", "mins", "minute", "minutes":
		return time.Minute
	case "h", "hr", "hrs", "hour", "hours":
		return time.Hour
	case "d", "day", "days":
		return 24 * time.Hour
	case "w", "week", "weeks":
		return 7 * 24 * time.Hour
	default:
		return 0
	}
}

// ParseUsageLimitPlanType reads the normalized plan_type from supported OpenAI
// limit-error objects. Malformed JSON, missing objects, unsupported types, and
// nonstring plan values return empty text; GoUsageLimitError does not supply
// this plan metadata.
func ParseUsageLimitPlanType(body []byte) string {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}

	errObj, ok := parsed["error"].(map[string]any)
	if !ok {
		return ""
	}

	errType, _ := errObj["type"].(string)
	if errType != "usage_limit_reached" && errType != "rate_limit_exceeded" {
		return ""
	}

	planType, _ := errObj["plan_type"].(string)
	return strings.ToLower(strings.TrimSpace(planType))
}
