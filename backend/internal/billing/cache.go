package billing

import (
	"context"
)

// cache.go defines cached balance and API-key spending data used by billing
// eligibility checks.
// Eligibility reads cached balances and key spending windows before
// request admission. Persistent funding writes remain the financial source,
// while queued deductions, usage increments, and invalidation keep cache
// entries useful for subsequent checks; cache read errors can trigger
// repository reads.

// APIKeyRateLimitCacheData retains spending totals and encoded rolling-window starts.
type APIKeyRateLimitCacheData struct {
	// Usage5h, Usage1d, and Usage7d hold USD usage for their respective
	// rolling windows. Window5h, Window1d, and Window7d store matching starts
	// in Unix seconds; zero identifies a window that has not started.
	Usage5h  float64 `json:"usage_5h"`  // USD five-hour usage
	Usage1d  float64 `json:"usage_1d"`  // USD one-day usage
	Usage7d  float64 `json:"usage_7d"`  // USD seven-day usage
	Window5h int64   `json:"window_5h"` // five-hour start, Unix seconds
	Window1d int64   `json:"window_1d"` // one-day start, Unix seconds
	Window7d int64   `json:"window_7d"` // seven-day start, Unix seconds
}

// BillingCache stores balance and key-window data used by eligibility checks.
type BillingCache interface {
	// GetUserBalance reads the cached USD balance for a user. A cache miss
	// or decoding error is returned as an error; Eligibility.GetUserBalance
	// can then load the authoritative persisted value.
	GetUserBalance(ctx context.Context, userID int64) (float64, error)
	// SetUserBalance replaces the cached USD balance for a user. The Redis
	// adapter creates or refreshes the entry with its configured expiration.
	// Callers supply a persisted balance snapshot for later eligibility checks.
	SetUserBalance(ctx context.Context, userID int64, balance float64) error
	// DeductUserBalance subtracts a USD charge from an existing cached balance.
	// The Redis adapter treats an absent entry as a no-op, allowing a later
	// read to reload the persisted balance instead of creating a partial cache
	// value.
	DeductUserBalance(ctx context.Context, userID int64, amount float64) error
	// InvalidateUserBalance removes the cached balance after a persisted funds
	// change. Subsequent eligibility reads can reload persistence; callers
	// receive any adapter error so the owning operation can report failed
	// invalidation.
	InvalidateUserBalance(ctx context.Context, userID int64) error

	// GetAPIKeyRateLimit reads the cached usage totals and starts for a key.
	// An absent entry returns an error so Eligibility can read persistence.
	// The Redis adapter discards individual numeric parse errors and returns
	// the parsed values from a nonempty hash, including malformed fields.
	GetAPIKeyRateLimit(ctx context.Context, keyID int64) (*APIKeyRateLimitCacheData, error)
	// SetAPIKeyRateLimit stores a key spending-window snapshot for later
	// eligibility checks. The Redis adapter treats nil data as a no-op and
	// writes supplied totals and starts with the longest window's expiration.
	SetAPIKeyRateLimit(ctx context.Context, keyID int64, data *APIKeyRateLimitCacheData) error
	// UpdateAPIKeyRateLimitUsage adds a USD charge to each cached key window.
	// The Redis adapter restarts expired windows at the observation time
	// and skips an absent cache entry so later reads can populate a complete
	// repository snapshot.
	UpdateAPIKeyRateLimitUsage(ctx context.Context, keyID int64, cost float64) error
	// InvalidateAPIKeyRateLimit removes cached spending-window data for a key.
	// Later eligibility checks can load current persistence, and an adapter
	// failure is returned for the calling operation to log or propagate.
	InvalidateAPIKeyRateLimit(ctx context.Context, keyID int64) error
}
