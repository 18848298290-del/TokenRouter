package pricing

import (
	"fmt"
	"math"
)

// ConfiguredImageUnitPrice returns a fixed per-image price and whether it exists; explicit zero is priced.
func ConfiguredImageUnitPrice(resolved *ResolvedPricing, size string) (float64, bool) {
	if resolved == nil || resolved.Mode != BillingModeImage && resolved.Mode != BillingModePerRequest {
		return 0, false
	}
	return ResolveRequestUnitPrice(resolved, size, nil)
}

// ValidateImageUnitPrice requires a finite, nonnegative fixed per-image price.
func ValidateImageUnitPrice(price float64) (float64, error) {
	if math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
		return 0, fmt.Errorf("invalid image unit price: %w", ErrModelPricingUnavailable)
	}
	return price, nil
}

// ResolveImageUnitPrice prefers the card's fixed price, then catalog media prices unless the card uses per-request billing.
func ResolveImageUnitPrice(resolved *ResolvedPricing, catalog *CatalogModelPricing, size string) (float64, error) {
	price, found := ConfiguredImageUnitPrice(resolved, size)
	if !found && (resolved == nil || resolved.Mode != BillingModePerRequest) {
		price, found = DefaultImagePrice(catalog, size)
	}
	if !found {
		return 0, ErrModelPricingUnavailable
	}
	return ValidateImageUnitPrice(price)
}
