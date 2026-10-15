package billing

import (
	"context"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// media_pricing.go resolves per-image reservation prices through configured
// and catalog pricing.
// Studio and batch submissions reserve a fixed price for each image
// before final usage is available. Configured image or fixed request
// prices can supply that unit amount, including zero, while catalog
// media pricing supplies the eligible fallback.

// ResolveImageUnitPrice returns a finite nonnegative fixed price for one
// image at size. Missing resolver, calculator, or model input returns
// ErrModelPricingUnavailable; price selection prefers a configured fixed
// amount and accepts explicit free pricing.
func (r *PriceResolver) ResolveImageUnitPrice(ctx context.Context, input PricingInput, size string) (float64, error) {
	if r == nil || r.calculator == nil || strings.TrimSpace(input.Model) == "" {
		return 0, pricing.ErrModelPricingUnavailable
	}
	resolved := r.Resolve(ctx, input)
	return r.calculator.resolvedImageUnitPrice(input.Model, size, resolved)
}

// resolvedImageUnitPrice selects a fixed image price from resolved settings
// and the model catalog. A per-request card lacking a fixed matching
// price cannot use catalog image fallback; missing or invalid unit
// prices return ErrModelPricingUnavailable.
func (s *Calculator) resolvedImageUnitPrice(model, size string, resolved *ResolvedPricing) (float64, error) {
	return pricing.ResolveImageUnitPrice(resolved, s.RawModelPricing(model), size)
}
