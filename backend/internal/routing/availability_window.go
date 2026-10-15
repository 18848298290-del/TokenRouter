package routing

// availability_window.go bounds marketplace history windows and adjusts bucket
// widths to cap response size.
// Availability history combines a requested day window with its bucket
// interval. Independently valid settings can still produce too many public time
// buckets, so normalization considers the combined response size after bounding
// each input.
//
// Nonpositive inputs use the configured defaults, then days and minutes are
// clamped to their accepted ranges. When the ceiling division exceeds the
// public bucket limit, the interval is raised by another ceiling division. This
// retains the normalized day window while reducing the sequence length, and the
// final interval can exceed its independently normalized input value.

// Availability defaults and bounds govern public history size; the bucket cap
// also constrains the combined normalized window and interval.
const (
	DefaultMarketplaceAvailabilityWindowDays    = 7    // default history days
	DefaultMarketplaceAvailabilityBucketMinutes = 120  // default bucket minutes
	MinMarketplaceAvailabilityWindowDays        = 1    // minimum history days
	MaxMarketplaceAvailabilityWindowDays        = 90   // maximum history days
	MinMarketplaceAvailabilityBucketMinutes     = 5    // initial minimum bucket minutes
	MaxMarketplaceAvailabilityBucketMinutes     = 1440 // initial maximum bucket minutes
	MaxMarketplaceAvailabilityBuckets           = 720  // public response bucket cap
)

// NormalizeMarketplaceAvailabilityWindow returns normalized history days and
// bucket minutes for public availability queries. Nonpositive arguments select
// defaults before each value is clamped to its accepted range.
//
// The day window and interval are then evaluated together because a small
// interval can produce an oversized response even after those clamps. Raising
// the interval to the rounded-up minutes per permitted bucket limits the
// sequence while retaining the day window; the returned interval can exceed its
// independently normalized input value.
func NormalizeMarketplaceAvailabilityWindow(windowDays int, bucketMinutes int) (int, int) {
	if windowDays <= 0 {
		windowDays = DefaultMarketplaceAvailabilityWindowDays
	}
	if bucketMinutes <= 0 {
		bucketMinutes = DefaultMarketplaceAvailabilityBucketMinutes
	}
	windowDays = ClampInt(windowDays, MinMarketplaceAvailabilityWindowDays, MaxMarketplaceAvailabilityWindowDays)
	bucketMinutes = ClampInt(bucketMinutes, MinMarketplaceAvailabilityBucketMinutes, MaxMarketplaceAvailabilityBucketMinutes)

	// Convert history days to minutes before comparing bucket counts.
	totalMinutes := windowDays * 24 * 60
	// Ceiling division includes a partial final bucket. Increase spacing when its
	// count would exceed the public response limit, retaining the normalized day
	// window.
	if bucketCount := (totalMinutes + bucketMinutes - 1) / bucketMinutes; bucketCount > MaxMarketplaceAvailabilityBuckets {
		bucketMinutes = (totalMinutes + MaxMarketplaceAvailabilityBuckets - 1) / MaxMarketplaceAvailabilityBuckets
	}
	return windowDays, bucketMinutes
}

// ClampInt returns value bounded by the caller's inclusive minimum and maximum.
// Callers provide an ordered range; values below the minimum or above the
// maximum select the corresponding endpoint.
func ClampInt(value int, minValue int, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
