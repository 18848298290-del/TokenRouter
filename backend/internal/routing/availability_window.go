package routing

const (
	DefaultMarketplaceAvailabilityWindowDays    = 7
	DefaultMarketplaceAvailabilityBucketMinutes = 120
	MinMarketplaceAvailabilityWindowDays        = 1
	MaxMarketplaceAvailabilityWindowDays        = 90
	MinMarketplaceAvailabilityBucketMinutes     = 5
	MaxMarketplaceAvailabilityBucketMinutes     = 1440
	MaxMarketplaceAvailabilityBuckets           = 720
)

func NormalizeMarketplaceAvailabilityWindow(windowDays int, bucketMinutes int) (int, int) {
	if windowDays <= 0 {
		windowDays = DefaultMarketplaceAvailabilityWindowDays
	}
	if bucketMinutes <= 0 {
		bucketMinutes = DefaultMarketplaceAvailabilityBucketMinutes
	}
	windowDays = ClampInt(windowDays, MinMarketplaceAvailabilityWindowDays, MaxMarketplaceAvailabilityWindowDays)
	bucketMinutes = ClampInt(bucketMinutes, MinMarketplaceAvailabilityBucketMinutes, MaxMarketplaceAvailabilityBucketMinutes)

	totalMinutes := windowDays * 24 * 60
	// Cap public response size and rendering cost when the configured bucket interval is too small.
	if bucketCount := (totalMinutes + bucketMinutes - 1) / bucketMinutes; bucketCount > MaxMarketplaceAvailabilityBuckets {
		bucketMinutes = (totalMinutes + MaxMarketplaceAvailabilityBuckets - 1) / MaxMarketplaceAvailabilityBuckets
	}
	return windowDays, bucketMinutes
}

func ClampInt(value int, minValue int, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
