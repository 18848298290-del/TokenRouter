package batchimage

import (
	"context"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// ImagePrices resolves per-image prices from pricing rules and the model.
type ImagePrices interface {
	ResolveImageUnitPrice(context.Context, billing.PricingInput, string) (float64, error)
}

// Pricing resolves batch image prices through the billing price resolver.
type Pricing struct {
	Resolver  ImagePrices
	GroupRepo GroupReader
}

func (r *Pricing) BatchImageUnitPrice(ctx context.Context, input BatchImagePriceInput) (float64, error) {
	if r == nil || r.Resolver == nil {
		return 0, ErrBatchImageSettlementPricingMissing
	}
	price, err := r.Resolver.ResolveImageUnitPrice(ctx, billing.PricingInput{Model: input.Model, GroupID: input.GroupID}, input.ImageSize)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrBatchImageSettlementPricingMissing, err)
	}
	return price, nil
}
