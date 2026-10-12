package billing

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// PublicQuoteInput supplies model pricing and rates; FreeFastApplicable permits the free-Fast display policy.
type PublicQuoteInput struct {
	PricingInput
	RateMultiplier     float64
	FreeFastApplicable bool
}

// PublicQuote resolves shared prices and applies free Fast to a display copy when the model supports Fast pricing.
func (r *PriceResolver) PublicQuote(ctx context.Context, input PublicQuoteInput) ModelDisplayPricing {
	resolved := r.Resolve(ctx, input.PricingInput)
	if r.BillingSettings(ctx, input.GroupID).FreeOpenAIFast && input.FreeFastApplicable && pricing.ResolvedHasFastModeDisplayPricing(resolved) {
		cloned := *resolved
		standardMultiplier := 1.0
		pricing.ApplyPricingModifiers(&cloned, &ModelPricingEntry{FastMultiplier: &standardMultiplier})
		resolved = &cloned
	}
	return r.calculator.DisplayPricingWithResolvedMultipliers(input.Model, input.RateMultiplier, resolved)
}
