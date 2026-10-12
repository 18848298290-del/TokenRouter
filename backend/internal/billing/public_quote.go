package billing

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// Marketplace prices represent the user charge for a group and model,
// including the Standard rate for eligible free-Fast requests. A free-Fast
// quote requires the effective billing setting, the caller's applicability
// decision, and resolved Fast display prices to agree before Standard
// prices are shown for accelerated requests.
//
// The quote first resolves ordinary model prices, then applies an eligible
// Fast override to a local resolved value. ApplyPricingModifiers copies
// the base pricing and card it changes, keeping the supplied pricing
// metadata available for other calculations. The final display calculation
// still uses the requested group multiplier and retains the calculator's
// priced-versus-unpriced and media-price rules.

// PublicQuoteInput selects a model quote, user multiplier, and caller-confirmed Fast applicability.
type PublicQuoteInput struct {
	PricingInput           // model and group selection
	RateMultiplier float64 // user pricing multiplier
	// FreeFastApplicable supplies the caller's applicability decision for this
	// model quote. It permits the display override only when the effective
	// settings enable free Fast and resolved prices contain Fast display data.
	FreeFastApplicable bool
}

// PublicQuote resolves model prices and returns display amounts using
// the requested rate multiplier. Call with a resolver that has a calculator.
// FreeFastApplicable supplies the caller's eligibility decision; the free
// Fast setting and resolved Fast display data are also required.
//
// When all three conditions hold, the quote uses a Standard Fast multiplier
// on a copied resolved value. ApplyPricingModifiers copies the base pricing
// and card before changing their modifier metadata, so other pricing
// calculations can retain their resolved sources. The calculator then
// formats this choice using its ordinary model and media display rules.
func (r *PriceResolver) PublicQuote(ctx context.Context, input PublicQuoteInput) ModelDisplayPricing {
	resolved := r.Resolve(ctx, input.PricingInput)
	if r.BillingSettings(ctx, input.GroupID).FreeOpenAIFast && input.FreeFastApplicable && pricing.ResolvedHasFastModeDisplayPricing(resolved) {
		// Eligible free Fast needs a unit Fast multiplier for the displayed
		// Standard amount. Keep that rewrite on a local resolved value before
		// the modifier helper copies the base and card it updates.
		cloned := *resolved
		standardMultiplier := 1.0
		pricing.ApplyPricingModifiers(&cloned, &ModelPricingEntry{FastMultiplier: &standardMultiplier})
		resolved = &cloned
	}
	return r.calculator.DisplayPricingWithResolvedMultipliers(input.Model, input.RateMultiplier, resolved)
}
