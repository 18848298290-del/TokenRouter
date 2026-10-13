package anthropic

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

// TestCalculateAnthropic429ResetTime_Only5hExceeded supplies an exhausted
// session window and a nonexhausted weekly window. The selected reset is
// five-hour timing, which is also retained separately for session accounting.
func TestCalculateAnthropic429ResetTime_Only5hExceeded(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "1.02")
	headers.Set("anthropic-ratelimit-unified-5h-reset", "1770998400")
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.32")
	headers.Set("anthropic-ratelimit-unified-7d-reset", "1771549200")

	result := CalculateRateLimitReset(headers, nil)
	assertAnthropicResult(t, result, 1770998400)

	if result.FiveHourReset == nil || !result.FiveHourReset.Equal(time.Unix(1770998400, 0)) {
		t.Errorf("expected fiveHourReset=1770998400, got %v", result.FiveHourReset)
	}
}

// TestCalculateAnthropic429ResetTime_Only7dExceeded supplies weekly exhaustion
// alongside a parsed five-hour reset. Weekly timing controls the selected
// cooldown, while the shorter reset remains available in FiveHourReset.
func TestCalculateAnthropic429ResetTime_Only7dExceeded(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.50")
	headers.Set("anthropic-ratelimit-unified-5h-reset", "1770998400")
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "1.05")
	headers.Set("anthropic-ratelimit-unified-7d-reset", "1771549200")

	result := CalculateRateLimitReset(headers, nil)
	assertAnthropicResult(t, result, 1771549200)

	// fiveHourReset should still be populated for session window calculation
	if result.FiveHourReset == nil || !result.FiveHourReset.Equal(time.Unix(1770998400, 0)) {
		t.Errorf("expected fiveHourReset=1770998400, got %v", result.FiveHourReset)
	}
}

// TestCalculateAnthropic429ResetTime_BothExceeded supplies exhausted five-hour
// and seven-day windows with both resets. The selected cooldown follows the
// weekly reset, recording its precedence when both windows report exhaustion.
func TestCalculateAnthropic429ResetTime_BothExceeded(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "1.10")
	headers.Set("anthropic-ratelimit-unified-5h-reset", "1770998400")
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "1.02")
	headers.Set("anthropic-ratelimit-unified-7d-reset", "1771549200")

	result := CalculateRateLimitReset(headers, nil)
	assertAnthropicResult(t, result, 1771549200)
}

// TestCalculateAnthropic429ResetTime_NoPerWindowHeaders supplies an aggregate
// reset without either per-window reset Header. This parser returns nil,
// leaving aggregate handling to its caller rather than selecting that value
// here.
func TestCalculateAnthropic429ResetTime_NoPerWindowHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-reset", "1771549200")

	result := CalculateRateLimitReset(headers, nil)
	if result != nil {
		t.Errorf("expected nil result when no per-window headers, got resetAt=%v", result.ResetAt)
	}
}

// TestCalculateAnthropic429ResetTime_NoHeaders passes an empty response Header
// map to the 429 reset parser. It returns nil because neither per-window reset
// can contribute an observation.
func TestCalculateAnthropic429ResetTime_NoHeaders(t *testing.T) {
	result := CalculateRateLimitReset(http.Header{}, nil)
	if result != nil {
		t.Errorf("expected nil result for empty headers, got resetAt=%v", result.ResetAt)
	}
}

// TestCalculateAnthropic429ResetTime_SurpassedThreshold supplies true five-hour
// and false seven-day threshold flags with reset values. The five-hour flag
// selects its matching reset despite the other available endpoint.
func TestCalculateAnthropic429ResetTime_SurpassedThreshold(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-surpassed-threshold", "true")
	headers.Set("anthropic-ratelimit-unified-5h-reset", "1770998400")
	headers.Set("anthropic-ratelimit-unified-7d-surpassed-threshold", "false")
	headers.Set("anthropic-ratelimit-unified-7d-reset", "1771549200")

	result := CalculateRateLimitReset(headers, nil)
	assertAnthropicResult(t, result, 1770998400)
}

// TestCalculateAnthropic429ResetTime_UtilizationExactlyOne reports utilization
// exactly one for the five-hour window and below one for weekly usage. The
// selected reset follows the fully utilized short window.
func TestCalculateAnthropic429ResetTime_UtilizationExactlyOne(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "1.0")
	headers.Set("anthropic-ratelimit-unified-5h-reset", "1770998400")
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.5")
	headers.Set("anthropic-ratelimit-unified-7d-reset", "1771549200")

	result := CalculateRateLimitReset(headers, nil)
	assertAnthropicResult(t, result, 1770998400)
}

// TestCalculateAnthropic429ResetTime_NeitherExceeded_UsesShorter supplies two
// nonexhausted windows with different reset endpoints. The selected result uses
// the earlier five-hour reset when no exhaustion signal decides the window.
func TestCalculateAnthropic429ResetTime_NeitherExceeded_UsesShorter(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.95")
	headers.Set("anthropic-ratelimit-unified-5h-reset", "1770998400") // sooner
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.80")
	headers.Set("anthropic-ratelimit-unified-7d-reset", "1771549200") // later

	result := CalculateRateLimitReset(headers, nil)
	assertAnthropicResult(t, result, 1770998400)
}

// TestCalculateAnthropic429ResetTime_Only5hResetHeader supplies five-hour
// exhaustion and only its reset Header. That endpoint becomes the selected
// result even though there is no weekly endpoint to compare.
func TestCalculateAnthropic429ResetTime_Only5hResetHeader(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "1.05")
	headers.Set("anthropic-ratelimit-unified-5h-reset", "1770998400")

	result := CalculateRateLimitReset(headers, nil)
	assertAnthropicResult(t, result, 1770998400)
}

// TestCalculateAnthropic429ResetTime_Only7dResetHeader supplies weekly
// exhaustion and only its reset Header. The result selects weekly timing and
// leaves FiveHourReset nil because no session reset was supplied.
func TestCalculateAnthropic429ResetTime_Only7dResetHeader(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "1.03")
	headers.Set("anthropic-ratelimit-unified-7d-reset", "1771549200")

	result := CalculateRateLimitReset(headers, nil)
	assertAnthropicResult(t, result, 1771549200)

	if result.FiveHourReset != nil {
		t.Errorf("expected fiveHourReset=nil when no 5h headers, got %v", result.FiveHourReset)
	}
}

// TestIsAnthropicWindowExceeded covers utilization around full capacity,
// threshold flags, case variation, and absent Headers. The parser reports the
// expected exhaustion decision for each listed window signal.
func TestIsAnthropicWindowExceeded(t *testing.T) {
	tests := []struct {
		name     string      // case display name
		headers  http.Header // input quota Headers
		window   string      // parsed window label
		expected bool        // expected comparison result
	}{
		{
			name:     "utilization above 1.0",
			headers:  makeHeader("anthropic-ratelimit-unified-5h-utilization", "1.02"),
			window:   "5h",
			expected: true,
		},
		{
			name:     "utilization exactly 1.0",
			headers:  makeHeader("anthropic-ratelimit-unified-5h-utilization", "1.0"),
			window:   "5h",
			expected: true,
		},
		{
			name:     "utilization below 1.0",
			headers:  makeHeader("anthropic-ratelimit-unified-5h-utilization", "0.99"),
			window:   "5h",
			expected: false,
		},
		{
			name:     "surpassed-threshold true",
			headers:  makeHeader("anthropic-ratelimit-unified-7d-surpassed-threshold", "true"),
			window:   "7d",
			expected: true,
		},
		{
			name:     "surpassed-threshold True (case insensitive)",
			headers:  makeHeader("anthropic-ratelimit-unified-7d-surpassed-threshold", "True"),
			window:   "7d",
			expected: true,
		},
		{
			name:     "surpassed-threshold false",
			headers:  makeHeader("anthropic-ratelimit-unified-7d-surpassed-threshold", "false"),
			window:   "7d",
			expected: false,
		},
		{
			name:     "no headers",
			headers:  http.Header{},
			window:   "5h",
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isWindowExceeded(tc.headers, tc.window)
			if got != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

// TestSelectAnthropicFableWindowLimit_RejectedStatus supplies rejected Fable
// weekly status with a future reset. The selected limit carries that exact
// endpoint and the Fable-specific persistence reason.
func TestSelectAnthropicFableWindowLimit_RejectedStatus(t *testing.T) {
	now := time.Now()
	reset := now.Add(80 * time.Hour).Truncate(time.Second)

	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-7d_oi-status", "rejected")
	headers.Set("anthropic-ratelimit-unified-7d_oi-utilization", "1.0")
	headers.Set("anthropic-ratelimit-unified-7d_oi-surpassed-threshold", "1.0")
	headers.Set("anthropic-ratelimit-unified-7d_oi-reset", strconv.FormatInt(reset.Unix(), 10))

	limit := SelectFableWindowLimit(headers, now)
	if limit == nil {
		t.Fatal("expected non-nil limit")
	}
	if !limit.ResetAt.Equal(reset) {
		t.Errorf("expected resetAt=%v, got %v", reset, limit.ResetAt)
	}
	if limit.Reason != FableWindowReason {
		t.Errorf("expected reason=%q, got %q", FableWindowReason, limit.Reason)
	}
}

// TestSelectAnthropicFableWindowLimit_UtilizationOnly supplies full Fable
// utilization with a usable reset and no status Header. The limit is still
// selected, demonstrating exhaustion from the numeric signal alone.
func TestSelectAnthropicFableWindowLimit_UtilizationOnly(t *testing.T) {
	// Full utilization indicates exhaustion even without a status header.
	now := time.Now()
	reset := now.Add(3 * 24 * time.Hour).Truncate(time.Second)

	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-7d_oi-utilization", "1.0")
	headers.Set("anthropic-ratelimit-unified-7d_oi-reset", strconv.FormatInt(reset.Unix(), 10))

	limit := SelectFableWindowLimit(headers, now)
	if limit == nil {
		t.Fatal("expected non-nil limit")
	}
	if !limit.ResetAt.Equal(reset) {
		t.Errorf("expected resetAt=%v, got %v", reset, limit.ResetAt)
	}
}

// TestSelectAnthropicFableWindowLimit_AllowedReturnsNil supplies allowed Fable
// status, partial utilization, and a future reset. Selection returns nil
// because the window has a deadline but lacks an exhaustion signal.
func TestSelectAnthropicFableWindowLimit_AllowedReturnsNil(t *testing.T) {
	now := time.Now()
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-7d_oi-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-7d_oi-utilization", "0.56")
	headers.Set("anthropic-ratelimit-unified-7d_oi-reset", strconv.FormatInt(now.Add(80*time.Hour).Unix(), 10))

	if limit := SelectFableWindowLimit(headers, now); limit != nil {
		t.Errorf("expected nil limit for allowed window, got %+v", limit)
	}
}

// TestSelectAnthropicFableWindowLimit_NoHeadersReturnsNil passes an empty
// Header map to Fable window selection. It returns nil, so absent window
// signals cannot synthesize a model-specific quota observation.
func TestSelectAnthropicFableWindowLimit_NoHeadersReturnsNil(t *testing.T) {
	if limit := SelectFableWindowLimit(http.Header{}, time.Now()); limit != nil {
		t.Errorf("expected nil limit for empty headers, got %+v", limit)
	}
}

// TestSelectAnthropicFableWindowLimit_FallsBackToAggregateReset supplies
// rejected Fable status and a future aggregate reset without its dedicated
// reset. Selection uses the aggregate endpoint and returns a model-specific
// limit.
func TestSelectAnthropicFableWindowLimit_FallsBackToAggregateReset(t *testing.T) {
	// A missing 7d_oi reset uses the aggregate reset header.
	now := time.Now()
	reset := now.Add(80 * time.Hour).Truncate(time.Second)

	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-7d_oi-status", "rejected")
	headers.Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(reset.Unix(), 10))

	limit := SelectFableWindowLimit(headers, now)
	if limit == nil {
		t.Fatal("expected non-nil limit via aggregate reset fallback")
	}
	if !limit.ResetAt.Equal(reset) {
		t.Errorf("expected resetAt=%v, got %v", reset, limit.ResetAt)
	}
}

// TestSelectAnthropicFableWindowLimit_RejectedWithoutAnyResetReturnsNil
// supplies rejected Fable status with no reset values. Selection returns nil
// because exhaustion alone cannot supply a usable endpoint for a quota limit.
func TestSelectAnthropicFableWindowLimit_RejectedWithoutAnyResetReturnsNil(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-7d_oi-status", "rejected")

	if limit := SelectFableWindowLimit(headers, time.Now()); limit != nil {
		t.Errorf("expected nil limit when no reset time available, got %+v", limit)
	}
}

// TestParseAnthropicAggregateReset covers future seconds and milliseconds
// alongside empty, malformed, past, and distant timestamps. Valid future inputs
// resolve to the expected second-precision time, while invalid horizons fail.
func TestParseAnthropicAggregateReset(t *testing.T) {
	now := time.Now()
	future := now.Add(80 * time.Hour).Truncate(time.Second)

	tests := []struct {
		name   string    // case display name
		value  string    // candidate reset timestamp
		want   time.Time // expected comparison value
		wantOK bool      // expected timestamp acceptance
	}{
		{"valid seconds", strconv.FormatInt(future.Unix(), 10), future, true},
		{"valid milliseconds", strconv.FormatInt(future.UnixMilli(), 10), future, true},
		{"empty", "", time.Time{}, false},
		{"garbage", "abc", time.Time{}, false},
		{"in the past", strconv.FormatInt(now.Add(-time.Hour).Unix(), 10), time.Time{}, false},
		{"too far in the future", strconv.FormatInt(now.Add(30*24*time.Hour).Unix(), 10), time.Time{}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{}
			if tc.value != "" {
				headers.Set("anthropic-ratelimit-unified-reset", tc.value)
			}
			got, ok := parseAggregateReset(headers, now)
			if ok != tc.wantOK {
				t.Fatalf("expected ok=%v, got %v", tc.wantOK, ok)
			}
			if ok && !got.Equal(tc.want) {
				t.Errorf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

// TestIsAnthropicWindowRejected supplies mixed-case rejected Fable status,
// allowed five-hour status, and no weekly status. Each window reports rejection
// according to its own status Header.
func TestIsAnthropicWindowRejected(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-7d_oi-status", "Rejected")
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")

	if !isWindowRejected(headers, "7d_oi") {
		t.Error("expected 7d_oi to be rejected (case insensitive)")
	}
	if isWindowRejected(headers, "5h") {
		t.Error("expected 5h not rejected")
	}
	if isWindowRejected(headers, "7d") {
		t.Error("expected missing 7d status not rejected")
	}
}

// assertAnthropicResult requires a nonnil rate-limit result and compares its
// selected reset with wantUnix seconds. It marks the call as a helper and stops
// the test before dereferencing an absent result.
func assertAnthropicResult(t *testing.T, result *RateLimitReset, wantUnix int64) {
	t.Helper()
	if result == nil {
		t.Fatal("expected non-nil result")
		return // unreachable, but satisfies staticcheck SA5011
	}
	want := time.Unix(wantUnix, 0)
	if !result.ResetAt.Equal(want) {
		t.Errorf("expected resetAt=%v, got %v", want, result.ResetAt)
	}
}

// makeHeader returns one canonical Header entry with the supplied value.
func makeHeader(key, value string) http.Header {
	h := http.Header{}
	h.Set(key, value)
	return h
}
