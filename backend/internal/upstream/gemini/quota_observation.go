package gemini

import (
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

var retryInRegex = regexp.MustCompile(`Please retry in ([0-9.]+)s`)

// ParseGeminiRateLimitResetTime returns reset time in Unix seconds, or nil when no delay can be resolved.
func ParseGeminiRateLimitResetTime(body []byte, nextDaily func() *int64) *int64 {
	// Daily quota errors use the caller-supplied reset time when available.
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
			// Use ceil to avoid undercounting fractional seconds (e.g. 10.1s should not become 10s),
			// which can affect scheduling decisions around thresholds (like 10s).
			ts := time.Now().Unix() + int64(math.Ceil(dur.Seconds()))
			found = &ts
			return false
		}
		return true
	})
	if found != nil {
		return found
	}

	// Some errors expose the delay only as "Please retry in Xs" text.
	matches := retryInRegex.FindStringSubmatch(string(body))
	if len(matches) == 2 {
		if dur, err := time.ParseDuration(matches[1] + "s"); err == nil {
			ts := time.Now().Unix() + int64(math.Ceil(dur.Seconds()))
			return &ts
		}
	}

	return nil
}

func LooksLikeGeminiDailyQuota(message string) bool {
	m := strings.ToLower(message)
	if strings.Contains(m, "per day") || strings.Contains(m, "requests per day") || strings.Contains(m, "quota") && strings.Contains(m, "per day") {
		return true
	}
	return false
}
