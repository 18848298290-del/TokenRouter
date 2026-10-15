package billing

import (
	"time"
)

// key_rate_limits.go defines API-key spending windows and computes their
// effective usage.
// API-key spending windows use elapsed durations of five hours, one day,
// and seven days. Reading effective usage treats an absent or expired
// start as zero consumption, while reset inputs separately choose
// whether persistence should replace lifetime quota usage or rolling
// window state.

// Rolling-window durations govern effective usage and match the database
// and Redis spending counters, using elapsed hours rather than local
// calendar boundaries.
const (
	RateLimitWindow5h = 5 * time.Hour      // five-hour spending period
	RateLimitWindow1d = 24 * time.Hour     // twenty-four-hour spending period
	RateLimitWindow7d = 7 * 24 * time.Hour // seven-day spending period
)

// APIKeyQuotaUsageState retains lifetime quota and activation facts returned after an atomic update.
type APIKeyQuotaUsageState struct {
	QuotaUsed float64 // USD charged or reserved usage
	// Quota is the lifetime USD ceiling compared with QuotaUsed. A zero
	// ceiling disables lifetime quota enforcement while the key's other
	// admission checks remain applicable.
	Quota  float64
	Key    string // stored API key credential
	Status string // stored key activation state
}

// APIKeyRateLimitData retains USD usage and timestamp starts for each rolling spending period.
type APIKeyRateLimitData struct {
	// Usage5h, Usage1d, and Usage7d belong to Window5hStart, Window1dStart,
	// and Window7dStart respectively. Each effective-usage method applies
	// its matching duration; a nil or expired start makes that returned
	// usage zero without writing a reset.
	Usage5h       float64    // USD five-hour usage
	Usage1d       float64    // USD one-day usage
	Usage7d       float64    // USD seven-day usage
	Window5hStart *time.Time // five-hour window origin
	Window1dStart *time.Time // one-day window origin
	Window7dStart *time.Time // seven-day window origin
}

// KeyUsageReset supplies selected lifetime quota and rolling-window replacement values.
type KeyUsageReset struct {
	// ResetQuota enables writing QuotaUsed, and ResetWindows enables writing
	// Windows. ApplyKeyUsageReset ignores each disabled section; enabled
	// window replacement writes every counter and clears stored starts
	// whose supplied pointers are nil.
	ResetQuota, ResetWindows bool                // ResetQuota replaces quota; ResetWindows replaces windows.
	QuotaUsed                float64             // replacement USD lifetime usage
	Windows                  APIKeyRateLimitData // replacement spending-window data
}

// EffectiveUsage5h returns the five-hour amount while its window remains
// active, otherwise zero. Call with a nonnil data value; the read uses
// current elapsed time and leaves stored counters and starts unchanged.
func (d *APIKeyRateLimitData) EffectiveUsage5h() float64 {
	if IsWindowExpired(d.Window5hStart, RateLimitWindow5h) {
		return 0
	}
	return d.Usage5h
}

// EffectiveUsage1d returns the one-day amount while its window remains
// active, otherwise zero. Call with a nonnil data value; the read uses
// current elapsed time and leaves stored counters and starts unchanged.
func (d *APIKeyRateLimitData) EffectiveUsage1d() float64 {
	if IsWindowExpired(d.Window1dStart, RateLimitWindow1d) {
		return 0
	}
	return d.Usage1d
}

// EffectiveUsage7d returns the seven-day amount while its window remains
// active, otherwise zero. Call with a nonnil data value; the read uses
// current elapsed time and leaves stored counters and starts unchanged.
func (d *APIKeyRateLimitData) EffectiveUsage7d() float64 {
	if IsWindowExpired(d.Window7dStart, RateLimitWindow7d) {
		return 0
	}
	return d.Usage7d
}

// IsWindowExpired reports whether the start is absent or its elapsed
// time reaches duration. The comparison uses time.Since at the call;
// a present future start with a positive duration remains unexpired
// until that duration has elapsed.
func IsWindowExpired(windowStart *time.Time, duration time.Duration) bool {
	return windowStart == nil || time.Since(*windowStart) >= duration
}
