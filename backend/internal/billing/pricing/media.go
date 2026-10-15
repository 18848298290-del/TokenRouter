package pricing

// media.go calculates image and video charges and selects catalog media unit
// prices.
// Image costs use USD per generated image, while video costs use USD per output
// second multiplied by each video's normalized duration. Positive counts
// produce base totals and rate-scaled charges; nonpositive counts produce an
// empty breakdown.
//
// Catalog media lookup retains price presence independently of value, so an
// explicit zero remains available. A nonempty image-size map controls the
// allowed dimensions and prevents a missing size from borrowing the flat image
// price. Unknown image sizes select 2K, and unknown video resolutions select
// 480p before lookup; callers validate the resulting amount before using it for
// a charge.

// CalculateImageCost multiplies unitPrice by imageCount and the supplied rate.
// A nonpositive count returns an empty breakdown; a negative rate becomes zero.
// Callers supply a validated finite per-image price and rate.
func CalculateImageCost(unitPrice float64, imageCount int, rateMultiplier float64) *CostBreakdown {
	if imageCount <= 0 {
		return &CostBreakdown{}
	}
	totalCost := unitPrice * float64(imageCount)

	// Cached negative rates produce no user charge while retaining the calculated
	// base amount for the returned usage breakdown.
	if rateMultiplier < 0 {
		rateMultiplier = 0
	}
	// The effective user rate scales the base amount after negative-rate
	// normalization, retaining both totals in the breakdown.
	actualCost := totalCost * rateMultiplier

	return &CostBreakdown{
		TotalCost:   totalCost,
		ActualCost:  actualCost,
		BillingMode: string(BillingModeImage),
	}
}

// CalculateVideoCost charges videoCount copies of one normalized duration.
// Nonpositive counts return an empty breakdown; nonpositive durations use eight
// seconds and positive durations are capped at fifteen. Callers supply a finite
// per-second price and rate; negative rates become zero.
func CalculateVideoCost(perSecondPrice float64, videoCount, durationSeconds int, rateMultiplier float64) *CostBreakdown {
	if videoCount <= 0 {
		return &CostBreakdown{}
	}
	// Apply the submission duration rule before multiplying by count, so each
	// video uses one supported length.
	durationSeconds = NormalizeVideoBillingDurationSecondsOrDefault(durationSeconds)
	totalCost := perSecondPrice * float64(durationSeconds) * float64(videoCount)

	// A negative user rate would turn a positive video amount into a credit;
	// retain its base amount with a zero charge.
	if rateMultiplier < 0 {
		rateMultiplier = 0
	}
	actualCost := totalCost * rateMultiplier

	return &CostBreakdown{
		TotalCost:   totalCost,
		ActualCost:  actualCost,
		BillingMode: string(BillingModeVideo),
	}
}

// HasImageUnitPrice reports an available flat catalog price per image. A
// positive amount is accepted for legacy entries, while zero requires
// ImagePricePresent. A size map alone does not establish a flat price.
func HasImageUnitPrice(catalogPrice *CatalogModelPricing) bool {
	return catalogPrice != nil && (catalogPrice.OutputCostPerImage > 0 ||
		catalogPrice.ImagePricePresent && catalogPrice.OutputCostPerImage == 0)
}

// DefaultImagePrice returns the catalog amount for the normalized image size
// and its presence. Nil entries return false, and present zero values remain
// known free prices. Unrecognized or absent size text selects the shared 2K
// lookup key.
//
// A nonempty size map defines this entry's available dimensions, so a missing
// key returns false even when a flat image amount exists. With an empty map, a
// positive legacy flat amount or an explicitly present zero can supply the
// price. Values are returned as stored; callers validate the selected amount
// before charging.
func DefaultImagePrice(price *CatalogModelPricing, size string) (float64, bool) {
	if price == nil {
		return 0, false
	}
	if value, ok := price.ImagePrices[NormalizeImageBillingTierOrDefault(size)]; ok {
		return value, true
	}
	// A populated size table defines its own availability; a missing dimension
	// cannot inherit an unrelated flat amount.
	if len(price.ImagePrices) > 0 {
		return 0, false
	}
	if HasImageUnitPrice(price) {
		return price.OutputCostPerImage, true
	}
	return 0, false
}

// DefaultVideoPrice returns the supplied entry's per-second resolution price
// and presence. Aliases are normalized and an unknown resolution selects 480p.
// An absent map entry returns false, while a mapped zero remains a known price.
func DefaultVideoPrice(price *CatalogModelPricing, resolution string) (float64, bool) {
	if price == nil {
		return 0, false
	}
	value, ok := price.VideoPrices[NormalizeVideoBillingResolutionOrDefault(resolution)]
	return value, ok
}
