package pricing

import (
	"fmt"
	"math"
)

// A fixed image reservation needs a price whose value does not depend on
// unknown token context. Configured image and per-request cards retain explicit
// zero prices, while catalog lookup supplies missing image dimensions only
// where the selected billing mode permits it.
//
// ConfiguredImageUnitPrice asks request-tier resolution for a known label or a
// safe default with no context count. ResolveImageUnitPrice then decides
// whether a missing configured price can use the same model's catalog media
// data. Per-request mode keeps missing prices unavailable, and a found invalid
// price produces an error instead of restarting source selection.

// ConfiguredImageUnitPrice returns a fixed card price and its presence for
// size. Image and per-request modes are eligible; nil or other modes return
// false. A matched label or safe explicit default may be zero.
func ConfiguredImageUnitPrice(resolved *ResolvedPricing, size string) (float64, bool) {
	if resolved == nil || resolved.Mode != BillingModeImage && resolved.Mode != BillingModePerRequest {
		return 0, false
	}
	return ResolveRequestUnitPrice(resolved, size, nil)
}

// ValidateImageUnitPrice returns price when it is finite and nonnegative.
// Negative values, NaN, and either infinity return an error wrapping
// ErrModelPricingUnavailable, so callers can classify invalid fixed prices with
// other missing-price failures.
func ValidateImageUnitPrice(price float64) (float64, error) {
	if math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
		return 0, fmt.Errorf("invalid image unit price: %w", ErrModelPricingUnavailable)
	}
	return price, nil
}

// ResolveImageUnitPrice selects and validates one fixed per-image price for
// size. A configured label or safe default wins, including an explicit zero.
// Missing image-card prices can use the supplied catalog entry; per-request
// cards require their own fixed price.
//
// Catalog selection stays within that entry's available dimensions or flat
// image price. A selected invalid value returns a pricing error and is not
// replaced by a different source, allowing reservation callers to reject that
// amount.
func ResolveImageUnitPrice(resolved *ResolvedPricing, catalog *CatalogModelPricing, size string) (float64, error) {
	price, found := ConfiguredImageUnitPrice(resolved, size)
	// A missing image dimension can use catalog media data; a per-request card
	// keeps its own fixed-price requirement.
	if !found && (resolved == nil || resolved.Mode != BillingModePerRequest) {
		price, found = DefaultImagePrice(catalog, size)
	}
	if !found {
		return 0, ErrModelPricingUnavailable
	}
	return ValidateImageUnitPrice(price)
}
