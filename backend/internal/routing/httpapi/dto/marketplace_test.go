package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// TestImagePriceJSONPreservesFreeAndMissingSizes serializes a quote with two
// declared sizes and one absent size. It checks that zero-valued 1K remains
// free in JSON, 2K keeps its amount, and 4K is omitted.
func TestImagePriceJSONPreservesFreeAndMissingSizes(t *testing.T) {
	row := modelMarketplacePricingFromRouting(pricing.ModelDisplayPricing{PricingMode: "image", PriceStatus: "priced", ImagePriceSizes: []string{"1K", "2K"}, ImagePrice1K: 0, ImagePrice2K: 0.1})
	body, err := json.Marshal(row)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(body, &fields))
	require.Contains(t, fields, "image_price_1k")
	require.Equal(t, 0.0, fields["image_price_1k"])
	require.Equal(t, 0.1, fields["image_price_2k"])
	require.NotContains(t, fields, "image_price_4k")
}

// TestMarketplacePricingJSONIncludesTokenModifiers serializes a token quote
// with a Max multiplier and a weekday time schedule. It checks the complete
// JSON object retains the factor, timezone, weekday restriction, and
// end-of-day clock spelling.
func TestMarketplacePricingJSONIncludesTokenModifiers(t *testing.T) {
	maxMultiplier := 1.5
	row := modelMarketplacePricingFromRouting(pricing.ModelDisplayPricing{
		PricingMode:                  "token",
		PriceStatus:                  "priced",
		InputPricePerToken:           1e-6,
		MaxReasoningEffortMultiplier: &maxMultiplier,
		TimePricing: &pricing.TimePricingConfig{
			Timezone:     "Asia/Shanghai",
			WeekdaysOnly: true,
			Periods:      []pricing.TimePricingPeriod{{StartTime: "20:00", EndTime: "00:00", Multiplier: 0.5}},
		},
	})
	body, err := json.Marshal(row)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"pricing_mode": "token",
		"price_status": "priced",
		"input_price_per_token": 0.000001,
		"max_reasoning_effort_multiplier": 1.5,
		"time_pricing": {
			"timezone": "Asia/Shanghai",
			"weekdays_only": true,
			"periods": [{"start_time": "20:00", "end_time": "00:00", "multiplier": 0.5}]
		}
	}`, string(body))
}

// TestMarketplacePricingJSONOmitsAbsentTokenModifiers serializes a token
// quote with neither optional modifier set. It checks that both Max effort
// and time schedule members are absent from the JSON object.
func TestMarketplacePricingJSONOmitsAbsentTokenModifiers(t *testing.T) {
	row := modelMarketplacePricingFromRouting(pricing.ModelDisplayPricing{PricingMode: "token", PriceStatus: "priced", InputPricePerToken: 1e-6})
	body, err := json.Marshal(row)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(body, &fields))
	require.NotContains(t, fields, "max_reasoning_effort_multiplier")
	require.NotContains(t, fields, "time_pricing")
}
