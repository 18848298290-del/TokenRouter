package scheduler

import "context"

// rpm_cache.go defines provider request-count reads and increments for
// scheduler RPM admission.
// Provider RPM admission reads a soft count for the current minute and records
// requests through a separate increment. The Redis adapter uses its server
// clock so all instances address the same minute bucket for each observation.

// RPMCache reads and increments minute counters used by provider RPM
// admission.
type RPMCache interface {
	// IncrementRPM records one provider request and returns the updated
	// minute count. Redis resolves the minute using server time and
	// refreshes counter expiry when incrementing; callers handle storage
	// failures.
	IncrementRPM(ctx context.Context, providerID int64) (count int, err error)

	// GetRPM reads the provider's current minute count without
	// incrementing it. An absent Redis key returns zero; storage failures
	// remain errors so admission callers can choose their failure policy.
	GetRPM(ctx context.Context, providerID int64) (count int, err error)

	// GetRPMBatch reads counts for the supplied provider IDs using one
	// minute observation. Empty input returns an empty map; the Redis
	// adapter reports a batch failure and treats unavailable individual
	// values as zero.
	GetRPMBatch(ctx context.Context, providerIDs []int64) (map[int64]int, error)
}
