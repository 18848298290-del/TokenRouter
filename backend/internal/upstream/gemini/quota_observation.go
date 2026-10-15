package gemini

import (
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// quota_observation.go derives Gemini rate-limit reset observations from daily
// quota messages and retry delay evidence.
// Google quota responses can carry a daily-limit message, a structured
// duration, or a human-readable retry hint. The caller supplies the
// calendar-specific daily reset, and this parser uses it when the message
// identifies a daily limit.
//
// If that reset is unavailable, the first parseable quotaResetDelay determines
// the observation before the textual retry hint is considered. Durations are
// rounded upward to whole seconds before adding the current Unix second, so
// fractional delay text is not shortened by integer truncation. The returned
// pointer is a scheduling observation; absent parseable evidence returns nil.

// retryInRegex recognizes the case-sensitive numeric retry hint used after
// structured quota delays fail to provide a reset observation.
var retryInRegex = regexp.MustCompile(`Please retry in ([0-9.]+)s`)

// ParseGeminiRateLimitResetTime returns the selected reset timestamp in Unix
// seconds, or nil when no source resolves it. A matching daily-quota message
// first uses a nonnil nextDaily result supplied by caller policy.
//
// Otherwise the first parseable structured quotaResetDelay wins over the
// textual retry hint. The duration is rounded upward and added to the current
// whole Unix second; negative structured durations are accepted as parsed.
// Parsing uses field extraction without whole-document validation, and the
// caller decides how this observation affects provider scheduling.
func ParseGeminiRateLimitResetTime(body []byte, nextDaily func() *int64) *int64 {
	// Let the caller calendar override transient delay hints for a daily-limit
	// message; a missing calendar observation continues through the ordinary
	// delay sources.
	errMsg := gjson.GetBytes(body, "error.message").String()
	if LooksLikeGeminiDailyQuota(errMsg) && nextDaily != nil {
		if ts := nextDaily(); ts != nil {
			return ts
		}
	}

	var found *int64
	gjson.GetBytes(body, "error.details").ForEach(func(_, detail gjson.Result) bool {
		v := detail.Get("metadata.quotaResetDelay").String()
		if v == "" {
			return true
		}
		if dur, err := time.ParseDuration(v); err == nil {
			// Round a fractional duration upward before converting to Unix seconds; a
			// 10.1-second delay contributes eleven seconds to this scheduling
			// observation.
			ts := time.Now().Unix() + int64(math.Ceil(dur.Seconds()))
			found = &ts
			return false
		}
		return true
	})
	if found != nil {
		return found
	}

	// When structured delay extraction fails, use the first textual retry hint so
	// compatible errors lacking metadata can still supply a scheduling timestamp.
	matches := retryInRegex.FindStringSubmatch(string(body))
	if len(matches) == 2 {
		if dur, err := time.ParseDuration(matches[1] + "s"); err == nil {
			ts := time.Now().Unix() + int64(math.Ceil(dur.Seconds()))
			return &ts
		}
	}

	return nil
}

// LooksLikeGeminiDailyQuota reports whether case-folded message text contains
// the daily-limit wording per day.
func LooksLikeGeminiDailyQuota(message string) bool {
	m := strings.ToLower(message)
	if strings.Contains(m, "per day") || strings.Contains(m, "requests per day") || strings.Contains(m, "quota") && strings.Contains(m, "per day") {
		return true
	}
	return false
}
