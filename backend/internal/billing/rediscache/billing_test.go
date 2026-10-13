package rediscache

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBillingBalanceKey formats positive, zero, negative and maximum int64
// user IDs. It checks each exact balance namespace/key spelling for the
// supplied identities.
func TestBillingBalanceKey(t *testing.T) {
	tests := []struct {
		name     string // test case display name
		userID   int64  // supplied user key identifier
		expected string // expected complete Redis key
	}{
		{
			name:     "normal_user_id",
			userID:   123,
			expected: "billing:balance:123",
		},
		{
			name:     "zero_user_id",
			userID:   0,
			expected: "billing:balance:0",
		},
		{
			name:     "negative_user_id",
			userID:   -1,
			expected: "billing:balance:-1",
		},
		{
			name:     "max_int64",
			userID:   math.MaxInt64,
			expected: "billing:balance:9223372036854775807",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := billingBalanceKey(tc.userID)
			require.Equal(t, tc.expected, got)
		})
	}
}

// TestJitteredTTL samples two hundred production lifetimes against a broad
// four-minute-thirty-second to five-minute-thirty-second interval. It checks
// those permissive bounds; the separate base-bound tests exercise the tighter
// upper limit.
func TestJitteredTTL(t *testing.T) {
	// These sample bounds tolerate lifetimes above the base; the later
	// expected-range and base-ceiling cases check the narrower subtractive
	// behavior.
	const (
		minTTL = 4*time.Minute + 30*time.Second // 270s = 5min - 30s
		maxTTL = 5*time.Minute + 30*time.Second // 330s = 5min + 30s
	)

	for range 200 {
		ttl := jitteredTTL()
		require.GreaterOrEqual(t, ttl, minTTL, "jitteredTTL() 返回值低于下限: %v", ttl)
		require.LessOrEqual(t, ttl, maxTTL, "jitteredTTL() 返回值超过上限: %v", ttl)
	}
}

// TestJitteredTTL_HasVariation samples fifty expiration lifetimes from the
// random helper. It checks more than one value appears in that sample to
// detect a constant-lifetime implementation.
func TestJitteredTTL_HasVariation(t *testing.T) {
	// Repeated calls exercise randomized expiration times.
	seen := make(map[time.Duration]struct{}, 50)
	for range 50 {
		seen[jitteredTTL()] = struct{}{}
	}
	// Require more than one TTL across 50 samples.
	require.Greater(t, len(seen), 1, "jitteredTTL() 应产生不同的 TTL 值")
}

// TestJitteredTTL_WithinExpectedRange samples two hundred lifetimes using
// base-minus-jitter and base bounds. It checks every sampled duration stays
// within that subtractive range.
func TestJitteredTTL_WithinExpectedRange(t *testing.T) {
	// jitteredTTL subtracts a duration in [0, billingCacheJitter).
	// The bounds below conservatively cover the resulting TTL range.
	lowerBound := billingCacheTTL - billingCacheJitter // 5min - 30s = 4min30s
	upperBound := billingCacheTTL                      // 5min

	for range 200 {
		ttl := jitteredTTL()
		assert.GreaterOrEqual(t, int64(ttl), int64(lowerBound),
			"TTL 不应低于 %v，实际得到 %v", lowerBound, ttl)
		assert.LessOrEqual(t, int64(ttl), int64(upperBound),
			"TTL 不应超过 %v（上界不变保证），实际得到 %v", upperBound, ttl)
	}
}

// TestJitteredTTL_NeverExceedsBase samples five hundred generated lifetimes.
// It checks every sampled value is at most the configured base expiry,
// protecting this observed subtractive upper bound.
func TestJitteredTTL_NeverExceedsBase(t *testing.T) {
	// Subtractive jitter cannot exceed billingCacheTTL.
	for range 500 {
		ttl := jitteredTTL()
		assert.LessOrEqual(t, int64(ttl), int64(billingCacheTTL),
			"jitteredTTL 不应超过基础 TTL（上界预期不被打破）")
	}
}

// TestJitteredTTL_HasVariance collects one hundred generated durations into a
// set. It checks that sample has multiple values, detecting a constant output
// for those calls.
func TestJitteredTTL_HasVariance(t *testing.T) {
	// Sample repeated calls to detect a constant TTL.
	results := make(map[time.Duration]bool)
	for range 100 {
		ttl := jitteredTTL()
		results[ttl] = true
	}

	require.Greater(t, len(results), 1,
		"jitteredTTL 应产生不同的值（抖动生效），但 100 次调用结果全部相同")
}

// TestJitteredTTL_AverageNearCenter averages one thousand sampled durations.
// It checks the sample mean is within five seconds of the subtractive range
// midpoint, allowing random sample variation.
func TestJitteredTTL_AverageNearCenter(t *testing.T) {
	// The sample mean should lie near the center of the jitter range.
	var sum time.Duration
	runs := 1000
	for range runs {
		sum += jitteredTTL()
	}

	// Integer duration division discards subnanosecond sample-mean remainder.
	avg := sum / time.Duration(runs)
	expectedCenter := billingCacheTTL - billingCacheJitter/2 // 4min45s

	// The five-second tolerance accommodates random sample means around the
	// midpoint; this check does not require a particular sequence of generated
	// durations.
	tolerance := 5 * time.Second
	assert.InDelta(t, float64(expectedCenter), float64(avg), float64(tolerance),
		"平均 TTL 应接近抖动范围中心 %v", expectedCenter)
}

// TestBillingKeyGeneration generates a balance key for one ordinary user ID.
// It checks the exact billing namespace and decimal identity in that example.
func TestBillingKeyGeneration(t *testing.T) {
	t.Run("balance_key", func(t *testing.T) {
		key := billingBalanceKey(12345)
		assert.Equal(t, "billing:balance:12345", key)
	})
}

// BenchmarkJitteredTTL repeatedly samples the production random lifetime
// helper. It measures generation work through the benchmark loop, with
// resulting lifetimes discarded after each call.
func BenchmarkJitteredTTL(b *testing.B) {
	for range b.N {
		_ = jitteredTTL()
	}
}
