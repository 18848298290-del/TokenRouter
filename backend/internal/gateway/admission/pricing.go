package admission

import (
	"context"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// A supported routing model can still lack a billable price. Text admission
// checks the selected billing model before contacting the provider and shares
// the settlement resolver, accepting explicit zero prices while rejecting
// multiplier-only configuration with no base price.

// ModelPricingUnavailableMessage directs clients to an administrator who can
// configure a price or map the request to a priced model.
const ModelPricingUnavailableMessage = "Model pricing is unavailable. Ask the administrator to configure a price or map the request to a priced model."

// ErrModelPricingRejected identifies a local preflight rejection and wraps the
// missing-price error so execution can stop before provider feedback or settlement.
var ErrModelPricingRejected = fmt.Errorf("model pricing admission rejected: %w", pricing.ErrModelPricingUnavailable)

// ModelPricing checks whether a text billing model has catalog pricing or a manual price card.
// @project-doc docs/domains/routing_and_billing.md#missing_model_pricing
type ModelPricing struct {
	// Resolver supplies the same catalog and configured price lookup used for
	// settlement. Application assembly provides a usable resolver before Check is called.
	Resolver *billing.PriceResolver
}

// Check accepts a model when the shared resolver finds usable pricing for its
// group. Manual zero prices and valid tiered prices pass; missing base prices
// return an error wrapping ErrModelPricingRejected and identifying the billing model.
func (p *ModelPricing) Check(ctx context.Context, groupID *int64, model string) error {
	resolved := p.Resolver.Resolve(ctx, billing.PricingInput{Model: model, GroupID: groupID})
	if resolved == nil || resolved.IsUnpriced() {
		return fmt.Errorf("%w: %s", ErrModelPricingRejected, model)
	}
	return nil
}
