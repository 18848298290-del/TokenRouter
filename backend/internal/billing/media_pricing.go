package billing

import (
	"context"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// ResolveImageUnitPrice resolves a fixed per-image price for studio and batch tasks. Token rates cannot price a
// whole image.
func (r *PriceResolver) ResolveImageUnitPrice(ctx context.Context, input PricingInput, size string) (float64, error) {
	if r == nil || r.calculator == nil || strings.TrimSpace(input.Model) == "" {
		return 0, pricing.ErrModelPricingUnavailable
	}
	resolved := r.Resolve(ctx, input)
	return r.calculator.resolvedImageUnitPrice(input.Model, size, resolved)
}

// resolvedImageUnitPrice shares size-based price selection across display, reservation, and settlement.
func (s *Calculator) resolvedImageUnitPrice(model, size string, resolved *ResolvedPricing) (float64, error) {
	return pricing.ResolveImageUnitPrice(resolved, s.RawModelPricing(model), size)
}
