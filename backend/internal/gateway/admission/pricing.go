package admission

import (
	"context"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// ModelPricingUnavailableMessage directs callers to configure pricing or select a priced model.
const ModelPricingUnavailableMessage = "Model pricing is unavailable. Ask the administrator to configure a price or map the request to a priced model."

// ErrModelPricingRejected marks rejection for missing pricing before an upstream call.
var ErrModelPricingRejected = fmt.Errorf("model pricing admission rejected: %w", pricing.ErrModelPricingUnavailable)

// ModelPricing checks whether a text billing model has catalog pricing or a manual price card.
// @project-doc docs/domains/routing_and_billing.md#missing_model_pricing
type ModelPricing struct {
	Resolver *billing.PriceResolver
}

// Check accepts manual zero prices and tiered prices. Multipliers without a base price remain unpriced.
func (p *ModelPricing) Check(ctx context.Context, groupID *int64, model string) error {
	resolved := p.Resolver.Resolve(ctx, billing.PricingInput{Model: model, GroupID: groupID})
	if resolved == nil || resolved.IsUnpriced() {
		return fmt.Errorf("%w: %s", ErrModelPricingRejected, model)
	}
	return nil
}
