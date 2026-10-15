package batchimage

import (
	"context"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// pricing.go resolves batch-image unit prices through the shared billing price
// resolver.
// Batch estimates need a per-image price for the selected billing model and
// image size. Pricing forwards that lookup to the shared billing resolver and
// wraps lookup failures with the missing-task-price category used by
// submission and settlement.

// ImagePrices resolves a per-image unit price using billing rules and output
// size.
type ImagePrices interface {
	// ResolveImageUnitPrice finds a per-image price using the billing model,
	// group and output size. A configured zero is a valid price; unavailable
	// image pricing returns an error for submission or catalogue filtering.
	ResolveImageUnitPrice(context.Context, billing.PricingInput, string) (float64, error)
}

// Pricing delegates batch unit-price lookup to the shared image pricing
// resolver.
type Pricing struct {
	Resolver  ImagePrices // shared image price resolver
	GroupRepo GroupReader // unused group reader dependency
}

// BatchImageUnitPrice resolves Model and ImageSize within GroupID through the
// shared resolver. The optional Group view is not forwarded. A missing resolver
// or lookup error returns the batch pricing category; a successful configured
// zero price is returned unchanged.
func (r *Pricing) BatchImageUnitPrice(ctx context.Context, input BatchImagePriceInput) (float64, error) {
	if r == nil || r.Resolver == nil {
		return 0, ErrBatchImageSettlementPricingMissing
	}
	price, err := r.Resolver.ResolveImageUnitPrice(ctx, billing.PricingInput{Model: input.Model, GroupID: input.GroupID}, input.ImageSize)
	if err != nil {
		// Admission and catalogue filtering inspect the batch missing-price category.
		// Keep the resolver error as diagnostic text while exposing that category.
		return 0, fmt.Errorf("%w: %v", ErrBatchImageSettlementPricingMissing, err)
	}
	return price, nil
}
