package pricing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestImageModeKeepsExplicitDefaultPrice combines a flat image default with an
// unlabeled context price. With context unknown, the fixed 2K lookup must
// return the flat default.
func TestImageModeKeepsExplicitDefaultPrice(t *testing.T) {
	base, tier := 0.2, 0.1
	resolved := ResolvePriceCards(&ModelPricingEntry{BillingMode: BillingModeImage, PerRequestPrice: &base, Intervals: []PricingInterval{{MinTokens: 100, PerRequestPrice: &tier}}}, nil, PricingSourceUnpriced, true)
	price, found := ConfiguredImageUnitPrice(resolved, "2K")
	require.True(t, found)
	require.Equal(t, base, price)
}
