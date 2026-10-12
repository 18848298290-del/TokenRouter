package billing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// TestMediaExplicitFreeCardWins supplies an explicit zero image price with
// no catalog entry. It protects both zero unit-price acceptance and a
// priced public quote that remains free after the display multiplier.
func TestMediaExplicitFreeCardWins(t *testing.T) {
	zero := 0.0
	groupID := int64(1)
	resolver := NewPriceResolver(mediaPriceCards{card: &ModelPricingEntry{
		BillingMode: pricing.BillingModeImage, PerRequestPrice: &zero,
	}}, NewCalculator(nil, CalculatorOptions{}), nil, nil)
	input := PricingInput{Model: "custom-image", GroupID: &groupID}
	unit, err := resolver.ResolveImageUnitPrice(context.Background(), input, "1K")
	require.NoError(t, err)
	require.Zero(t, unit)
	quote := resolver.PublicQuote(context.Background(), PublicQuoteInput{PricingInput: input, RateMultiplier: 2})
	require.Equal(t, "priced", quote.PriceStatus)
	require.Zero(t, quote.ImagePrice1K)
}
