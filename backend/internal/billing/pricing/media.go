package pricing

// CalculateImageCost charges an explicit per-image price and clamps negative rate multipliers to zero.
func CalculateImageCost(unitPrice float64, imageCount int, rateMultiplier float64) *CostBreakdown {
	if imageCount <= 0 {
		return &CostBreakdown{}
	}
	// Base cost is the unit price times the image count.
	totalCost := unitPrice * float64(imageCount)

	// Clamp invalid negative rates to zero.
	if rateMultiplier < 0 {
		rateMultiplier = 0
	}
	actualCost := totalCost * rateMultiplier

	return &CostBreakdown{
		TotalCost:   totalCost,
		ActualCost:  actualCost,
		BillingMode: string(BillingModeImage),
	}
}

// CalculateVideoCost charges each video's normalized duration at the supplied per-second price.
func CalculateVideoCost(perSecondPrice float64, videoCount, durationSeconds int, rateMultiplier float64) *CostBreakdown {
	if videoCount <= 0 {
		return &CostBreakdown{}
	}
	durationSeconds = NormalizeVideoBillingDurationSecondsOrDefault(durationSeconds)
	totalCost := perSecondPrice * float64(durationSeconds) * float64(videoCount)

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

// HasImageUnitPrice recognizes an explicit per-image catalog price, including zero.
func HasImageUnitPrice(catalogPrice *CatalogModelPricing) bool {
	return catalogPrice != nil && (catalogPrice.OutputCostPerImage > 0 ||
		catalogPrice.ImagePricePresent && catalogPrice.OutputCostPerImage == 0)
}

// DefaultImagePrice uses the requested size's price, or the flat image price when no size map exists.
func DefaultImagePrice(price *CatalogModelPricing, size string) (float64, bool) {
	if price == nil {
		return 0, false
	}
	if value, ok := price.ImagePrices[NormalizeImageBillingTierOrDefault(size)]; ok {
		return value, true
	}
	if len(price.ImagePrices) > 0 {
		return 0, false
	}
	if HasImageUnitPrice(price) {
		return price.OutputCostPerImage, true
	}
	return 0, false
}

// DefaultVideoPrice reads the supplied model's per-second price for the normalized resolution.
func DefaultVideoPrice(price *CatalogModelPricing, resolution string) (float64, bool) {
	if price == nil {
		return 0, false
	}
	value, ok := price.VideoPrices[NormalizeVideoBillingResolutionOrDefault(resolution)]
	return value, ok
}
