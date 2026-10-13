package grok

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestParseQuotaHeaders combines request/token windows, retry delay, tier,
// and entitlement with an authorization header. It asserts parsed values,
// reset spelling, header-observation metadata, and allowlisted copying while
// credentials are excluded from the raw map.
func TestParseQuotaHeaders(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Set("x-ratelimit-limit-requests", "100")
	headers.Set("x-ratelimit-remaining-requests", "25")
	headers.Set("x-ratelimit-reset-requests", "1893456000")
	headers.Set("x-ratelimit-limit-tokens", "1000000")
	headers.Set("x-ratelimit-remaining-tokens", "750000")
	headers.Set("retry-after", "60")
	headers.Set("xai-subscription-tier", "supergrok")
	headers.Set("xai-entitlement-status", "active")
	headers.Set("authorization", "should-not-be-copied")

	snapshot := ParseQuotaHeaders(headers, http.StatusTooManyRequests)
	require.NotNil(t, snapshot)
	require.Equal(t, http.StatusTooManyRequests, snapshot.StatusCode)
	require.True(t, snapshot.HeadersObserved)
	require.NotEmpty(t, snapshot.LastHeadersSeenAt)
	require.Equal(t, int64(100), *snapshot.Requests.Limit)
	require.Equal(t, int64(25), *snapshot.Requests.Remaining)
	require.Equal(t, int64(1893456000), *snapshot.Requests.ResetUnix)
	require.Equal(t, "2030-01-01T00:00:00Z", snapshot.Requests.ResetAt)
	require.Equal(t, int64(1000000), *snapshot.Tokens.Limit)
	require.Equal(t, int64(750000), *snapshot.Tokens.Remaining)
	require.Equal(t, 60, *snapshot.RetryAfterSeconds)
	require.Equal(t, "supergrok", snapshot.SubscriptionTier)
	require.Equal(t, "active", snapshot.EntitlementStatus)
	require.Contains(t, snapshot.Headers, "x-ratelimit-limit-requests")
	require.NotContains(t, snapshot.Headers, "authorization")
}

// TestParseQuotaHeadersAcceptsXAITierAliases supplies alternate user-tier and
// entitlement header names. It asserts both values populate normalized
// snapshot fields and remain available under their raw allowlisted header
// keys.
func TestParseQuotaHeadersAcceptsXAITierAliases(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Set("x-xai-user-tier", "supergrok-heavy")
	headers.Set("x-xai-user-entitlement-status", "enabled")

	snapshot := ParseQuotaHeaders(headers, http.StatusOK)
	require.NotNil(t, snapshot)
	require.True(t, snapshot.HeadersObserved)
	require.Equal(t, "supergrok-heavy", snapshot.SubscriptionTier)
	require.Equal(t, "enabled", snapshot.EntitlementStatus)
	require.Equal(t, "supergrok-heavy", snapshot.Headers["x-xai-user-tier"])
	require.Equal(t, "enabled", snapshot.Headers["x-xai-user-entitlement-status"])
}

// TestParseQuotaHeadersAcceptsRateLimitAliases uses the x-rate-limit token
// header spelling. It asserts limit, remaining, and absolute reset values
// populate the token window and the raw alias header is retained.
func TestParseQuotaHeadersAcceptsRateLimitAliases(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Set("x-rate-limit-limit-tokens", "500000")
	headers.Set("x-rate-limit-remaining-tokens", "100")
	headers.Set("x-rate-limit-reset-tokens", "1893456000")

	snapshot := ParseQuotaHeaders(headers, http.StatusOK)
	require.NotNil(t, snapshot)
	require.NotNil(t, snapshot.Tokens)
	require.Equal(t, int64(500000), *snapshot.Tokens.Limit)
	require.Equal(t, int64(100), *snapshot.Tokens.Remaining)
	require.Equal(t, int64(1893456000), *snapshot.Tokens.ResetUnix)
	require.Contains(t, snapshot.Headers, "x-rate-limit-limit-tokens")
}

// TestParseResetHeaderRelativeSecondsNotMisreadAsEpoch supplies a numeric
// sixty-second reset with exhausted requests. It asserts the resulting epoch
// falls near one minute after observation rather than representing a time in
// 1970.
func TestParseResetHeaderRelativeSecondsNotMisreadAsEpoch(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	// A small numeric reset belongs to a relative window. Sampling both sides
	// of parsing gives the assertion tolerance for elapsed wall-clock time during the test.
	headers.Set("x-ratelimit-reset-requests", "60")
	headers.Set("x-ratelimit-remaining-requests", "0")

	before := time.Now().Unix()
	snapshot := ParseQuotaHeaders(headers, http.StatusTooManyRequests)
	require.NotNil(t, snapshot)
	require.NotNil(t, snapshot.Requests)
	require.NotNil(t, snapshot.Requests.ResetUnix)
	got := *snapshot.Requests.ResetUnix
	require.GreaterOrEqual(t, got, before+59)
	require.LessOrEqual(t, got, time.Now().Unix()+61)
}

// TestParseResetHeaderDurationWindow supplies a six-minute duration string
// with exhausted requests. It asserts the parsed reset falls near six minutes
// after the test's observation time, covering duration-based relative resets.
func TestParseResetHeaderDurationWindow(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Set("x-ratelimit-reset-requests", "6m0s")
	headers.Set("x-ratelimit-remaining-requests", "0")

	before := time.Now().Unix()
	snapshot := ParseQuotaHeaders(headers, http.StatusTooManyRequests)
	require.NotNil(t, snapshot)
	require.NotNil(t, snapshot.Requests)
	require.NotNil(t, snapshot.Requests.ResetUnix)
	got := *snapshot.Requests.ResetUnix
	require.GreaterOrEqual(t, got, before+359)
	require.LessOrEqual(t, got, time.Now().Unix()+361)
}

// TestParseResetHeaderSubsecondDurationCeilsToFutureSecond supplies a
// 250-millisecond token reset. It asserts a parsed reset exists within the
// test's current-to-two-seconds window; this assertion does not require a
// strictly later epoch second.
func TestParseResetHeaderSubsecondDurationCeilsToFutureSecond(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Set("x-rate-limit-reset-tokens", "250ms")
	headers.Set("x-rate-limit-remaining-tokens", "0")

	before := time.Now().Unix()
	snapshot := ParseQuotaHeaders(headers, http.StatusTooManyRequests)
	require.NotNil(t, snapshot)
	require.NotNil(t, snapshot.Tokens)
	require.NotNil(t, snapshot.Tokens.ResetUnix)
	require.GreaterOrEqual(t, *snapshot.Tokens.ResetUnix, before)
	require.LessOrEqual(t, *snapshot.Tokens.ResetUnix, time.Now().Unix()+2)
}

// TestParseResetHeaderMillisecondsEpochNormalized supplies an absolute
// millisecond timestamp for an exhausted token window. It asserts
// normalization stores the expected Unix-second epoch rather than leaving
// milliseconds in ResetUnix.
func TestParseResetHeaderMillisecondsEpochNormalized(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Set("x-ratelimit-reset-tokens", "1893456000000") // ms epoch
	headers.Set("x-ratelimit-remaining-tokens", "0")

	snapshot := ParseQuotaHeaders(headers, http.StatusTooManyRequests)
	require.NotNil(t, snapshot)
	require.NotNil(t, snapshot.Tokens)
	require.Equal(t, int64(1893456000), *snapshot.Tokens.ResetUnix)
}

// TestParseQuotaHeadersReturnsNilForMissingHeaders supplies an empty
// successful response header map. It asserts passive parsing yields no
// snapshot, retaining the distinction between an unobserved quota and a
// parsed zero-valued quota window.
func TestParseQuotaHeadersReturnsNilForMissingHeaders(t *testing.T) {
	t.Parallel()

	require.Nil(t, ParseQuotaHeaders(http.Header{}, http.StatusOK))
}

// TestObserveQuotaHeadersRecordsNoHeaderProbe records a successful active
// probe with an empty header map. It asserts attempt status/time remain while
// header-observation markers, windows, and last-header time stay absent.
func TestObserveQuotaHeadersRecordsNoHeaderProbe(t *testing.T) {
	t.Parallel()

	snapshot := ObserveQuotaHeaders(http.Header{}, http.StatusOK, "active_probe")
	require.NotNil(t, snapshot)
	require.False(t, snapshot.HeadersObserved)
	require.Equal(t, http.StatusOK, snapshot.StatusCode)
	require.Equal(t, "active_probe", snapshot.ObservationSource)
	require.NotEmpty(t, snapshot.LastProbeAt)
	require.Empty(t, snapshot.LastHeadersSeenAt)
	require.Empty(t, snapshot.Headers)
	require.Nil(t, snapshot.Requests)
	require.Nil(t, snapshot.Tokens)
}

// TestIsGrokFreeRolling24hTokenLimit compares the nominal rolling-day limit
// and configured alternate amounts with an unmatched value. It asserts all
// listed accepted limits classify as Free and the unrelated amount does not.
func TestIsGrokFreeRolling24hTokenLimit(t *testing.T) {
	t.Parallel()

	require.True(t, IsGrokFreeRolling24hTokenLimit(GrokFreeRolling24hTokenLimit))
	require.True(t, IsGrokFreeRolling24hTokenLimit(500_000))
	require.True(t, IsGrokFreeRolling24hTokenLimit(1_000_000), "observed Free limit variants remain classifiable")
	require.True(t, IsGrokFreeRolling24hTokenLimit(2_000_000), "legacy snapshots remain classifiable")
	require.False(t, IsGrokFreeRolling24hTokenLimit(3_000_000))
}
