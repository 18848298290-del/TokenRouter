package pricing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestImageModeKeepsExplicitDefaultPrice checks that unlabeled context intervals do not replace an image card's default price.
func TestImageModeKeepsExplicitDefaultPrice(t *testing.T) {
	base, tier := 0.2, 0.1
	resolved := ResolvePriceCards(&ModelPricingEntry{BillingMode: BillingModeImage, PerRequestPrice: &base, Intervals: []PricingInterval{{MinTokens: 100, PerRequestPrice: &tier}}}, nil, PricingSourceUnpriced, true)
	price, found := ConfiguredImageUnitPrice(resolved, "2K")
	require.True(t, found)
	require.Equal(t, base, price)
}
