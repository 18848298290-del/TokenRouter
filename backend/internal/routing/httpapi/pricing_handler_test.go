package httpapi

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// float64Ptr allocates an optional amount for pricing mapper assertions.
func float64Ptr(v float64) *float64 { return &v }

// pricingConfigIntPtr allocates an optional upper endpoint for interval
// assertions.
func pricingConfigIntPtr(v int) *int { return &v }

// TestPricingConfigToResponse_NilInput maps an absent stored configuration
// through the response helper. It checks that the mapper returns nil so
// absent records remain distinguishable from an empty configuration.
func TestPricingConfigToResponse_NilInput(t *testing.T) {
	require.Nil(t, pricingConfigToResponse(nil))
}

// TestPricingConfigToResponse_FullPricingConfig maps stored identity, group
// associations, UTC timestamps, card amounts, tier factors, and a daily
// schedule. It checks the selected response fields and confirms current Fast
// input wins over the legacy factor.
func TestPricingConfigToResponse_FullPricingConfig(t *testing.T) {
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	ch := &routing.PricingConfig{
		ID:                 42,
		Name:               "test-price-config",
		Description:        "desc",
		Status:             "active",
		BillingModelSource: "upstream",

		CreatedAt: now,
		UpdatedAt: now.Add(time.Hour),
		GroupIDs:  []int64{1, 2, 3},
		ModelPricing: []routing.ModelPricingEntry{
			{
				ID: 10,

				Models:             []string{"gpt-4"},
				BillingMode:        routing.BillingModeToken,
				PriceMultiplier:    float64Ptr(1.5),
				FastModeMultiplier: float64Ptr(2),
				FastMultiplier:     float64Ptr(2.5),
				FlexMultiplier:     float64Ptr(0.5),
				InputPrice:         float64Ptr(0.01),
				OutputPrice:        float64Ptr(0.03),
				CacheWritePrice:    float64Ptr(0.005),
				CacheReadPrice:     float64Ptr(0.002),
				PerRequestPrice:    float64Ptr(0.5),
				TimePricing: &routing.TimePricingConfig{
					Timezone:     "Asia/Shanghai",
					WeekdaysOnly: true,
					Periods: []routing.TimePricingPeriod{{
						StartTime: "09:00", EndTime: "12:00", Multiplier: 2,
					}},
				},
			},
		},
	}

	resp := pricingConfigToResponse(ch)
	require.NotNil(t, resp)
	require.Equal(t, int64(42), resp.ID)
	require.Equal(t, "test-price-config", resp.Name)
	require.Equal(t, "desc", resp.Description)
	require.Equal(t, "active", resp.Status)
	require.Equal(t, "upstream", resp.BillingModelSource)
	require.Equal(t, []int64{1, 2, 3}, resp.GroupIDs)
	require.Equal(t, "2025-06-01T12:00:00Z", resp.CreatedAt)
	require.Equal(t, "2025-06-01T13:00:00Z", resp.UpdatedAt)

	require.Len(t, resp.ModelPricing, 1)
	p := resp.ModelPricing[0]
	require.Equal(t, int64(10), p.ID)
	require.Equal(t, []string{"gpt-4"}, p.Models)
	require.Equal(t, "token", p.BillingMode)
	require.Equal(t, float64Ptr(1.5), p.PriceMultiplier)
	require.Equal(t, float64Ptr(2), p.FastModeMultiplier)
	require.Equal(t, float64Ptr(2.5), p.FastMultiplier)
	require.Equal(t, float64Ptr(0.5), p.FlexMultiplier)
	require.Equal(t, float64Ptr(0.01), p.InputPrice)
	require.Equal(t, float64Ptr(0.03), p.OutputPrice)
	require.Equal(t, float64Ptr(0.005), p.CacheWritePrice)
	require.Equal(t, float64Ptr(0.002), p.CacheReadPrice)
	require.Equal(t, float64Ptr(0.5), p.PerRequestPrice)
	require.Empty(t, p.Intervals)
	require.NotNil(t, p.TimePricing)
	require.Equal(t, "Asia/Shanghai", p.TimePricing.Timezone)
	require.True(t, p.TimePricing.WeekdaysOnly)
	require.Equal(t, 2.0, p.TimePricing.Periods[0].Multiplier)
}

// TestPricingRequestToServiceTimePricing converts a card with a timezone,
// weekday restriction, and second-resolution period. It checks the service
// schedule retains the timezone, restriction, and supplied period multiplier.
func TestPricingRequestToServiceTimePricing(t *testing.T) {
	pricing := pricingRequestToService([]modelPricingRequest{{
		Models: []string{"gpt-5"},
		TimePricing: &timePricingRequest{
			Timezone:     "Asia/Tokyo",
			WeekdaysOnly: true,
			Periods: []timePricingPeriodRequest{{
				StartTime: "10:00:00", EndTime: "11:00:00", Multiplier: 1.25,
			}},
		},
	}})
	require.Len(t, pricing, 1)
	require.Equal(t, "Asia/Tokyo", pricing[0].TimePricing.Timezone)
	require.True(t, pricing[0].TimePricing.WeekdaysOnly)
	require.Equal(t, 1.25, pricing[0].TimePricing.Periods[0].Multiplier)
}

// TestPricingConfigToResponse_EmptyDefaults maps a configuration with unset
// model source, absent group associations, and empty card mode. It checks
// group_mapped/token defaults and a present empty group list.
func TestPricingConfigToResponse_EmptyDefaults(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ch := &routing.PricingConfig{
		ID:                 1,
		Name:               "ch",
		BillingModelSource: "",
		CreatedAt:          now,
		UpdatedAt:          now,
		GroupIDs:           nil,

		ModelPricing: []routing.ModelPricingEntry{
			{
				BillingMode: "",
				Models:      []string{"m1"},
			},
		},
	}

	resp := pricingConfigToResponse(ch)
	require.Equal(t, "group_mapped", resp.BillingModelSource)
	require.NotNil(t, resp.GroupIDs)
	require.Empty(t, resp.GroupIDs)

	require.Len(t, resp.ModelPricing, 1)
	require.Equal(t, "token", resp.ModelPricing[0].BillingMode)
}

// TestPricingConfigToResponse_NilModels maps a stored card with no model-name
// slice. It checks the response has one card and a nonnil empty model list
// for JSON array encoding.
func TestPricingConfigToResponse_NilModels(t *testing.T) {
	now := time.Now()
	ch := &routing.PricingConfig{
		ID:        1,
		Name:      "ch",
		CreatedAt: now,
		UpdatedAt: now,
		ModelPricing: []routing.ModelPricingEntry{
			{
				Models: nil,
			},
		},
	}

	resp := pricingConfigToResponse(ch)
	require.Len(t, resp.ModelPricing, 1)
	require.NotNil(t, resp.ModelPricing[0].Models)
	require.Empty(t, resp.ModelPricing[0].Models)
}

// TestPricingConfigToResponse_WithIntervals maps a bounded priced interval
// followed by an unbounded interval. It checks retained endpoints, row
// identities, labels, amount and multiplier pointers, and supplied sort order
// in the response.
func TestPricingConfigToResponse_WithIntervals(t *testing.T) {
	now := time.Now()
	ch := &routing.PricingConfig{
		ID:        1,
		Name:      "ch",
		CreatedAt: now,
		UpdatedAt: now,
		ModelPricing: []routing.ModelPricingEntry{
			{
				Models:      []string{"m1"},
				BillingMode: routing.BillingModePerRequest,
				Intervals: []routing.PricingInterval{
					{
						ID:                   100,
						MinTokens:            0,
						MaxTokens:            pricingConfigIntPtr(1000),
						TierLabel:            "1K",
						InputPrice:           float64Ptr(0.01),
						OutputPrice:          float64Ptr(0.02),
						CacheWritePrice:      float64Ptr(0.003),
						CacheReadPrice:       float64Ptr(0.001),
						InputMultiplier:      float64Ptr(1.1),
						OutputMultiplier:     float64Ptr(1.2),
						CacheWriteMultiplier: float64Ptr(1.3),
						CacheReadMultiplier:  float64Ptr(1.4),
						PerRequestPrice:      float64Ptr(0.1),
						SortOrder:            1,
					},
					{
						ID:        101,
						MinTokens: 1000,
						MaxTokens: nil,
						TierLabel: "unlimited",
						SortOrder: 2,
					},
				},
			},
		},
	}

	resp := pricingConfigToResponse(ch)
	require.Len(t, resp.ModelPricing, 1)
	intervals := resp.ModelPricing[0].Intervals
	require.Len(t, intervals, 2)

	iv0 := intervals[0]
	require.Equal(t, int64(100), iv0.ID)
	require.Equal(t, 0, iv0.MinTokens)
	require.Equal(t, pricingConfigIntPtr(1000), iv0.MaxTokens)
	require.Equal(t, "1K", iv0.TierLabel)
	require.Equal(t, float64Ptr(0.01), iv0.InputPrice)
	require.Equal(t, float64Ptr(0.02), iv0.OutputPrice)
	require.Equal(t, float64Ptr(0.003), iv0.CacheWritePrice)
	require.Equal(t, float64Ptr(0.001), iv0.CacheReadPrice)
	require.Equal(t, float64Ptr(1.1), iv0.InputMultiplier)
	require.Equal(t, float64Ptr(1.2), iv0.OutputMultiplier)
	require.Equal(t, float64Ptr(1.3), iv0.CacheWriteMultiplier)
	require.Equal(t, float64Ptr(1.4), iv0.CacheReadMultiplier)
	require.Equal(t, float64Ptr(0.1), iv0.PerRequestPrice)
	require.Equal(t, 1, iv0.SortOrder)

	iv1 := intervals[1]
	require.Equal(t, int64(101), iv1.ID)
	require.Equal(t, 1000, iv1.MinTokens)
	require.Nil(t, iv1.MaxTokens)
	require.Equal(t, "unlimited", iv1.TierLabel)
	require.Equal(t, 2, iv1.SortOrder)
}

// TestPricingConfigToResponse_MultipleEntries maps token, per-request, and
// image cards together. It checks their model identities and modes remain in
// source order, and verifies the image card's output amount.
func TestPricingConfigToResponse_MultipleEntries(t *testing.T) {
	now := time.Now()
	ch := &routing.PricingConfig{
		ID:        1,
		Name:      "multi",
		CreatedAt: now,
		UpdatedAt: now,
		ModelPricing: []routing.ModelPricingEntry{
			{
				ID: 1,

				Models:      []string{"claude-sonnet-4"},
				BillingMode: routing.BillingModeToken,
				InputPrice:  float64Ptr(0.003),
				OutputPrice: float64Ptr(0.015),
			},
			{
				ID: 2,

				Models:          []string{"gpt-4", "gpt-4o"},
				BillingMode:     routing.BillingModePerRequest,
				PerRequestPrice: float64Ptr(1.0),
			},
			{
				ID: 3,

				Models:           []string{"gemini-2.5-pro"},
				BillingMode:      routing.BillingModeImage,
				ImageOutputPrice: float64Ptr(0.05),
				PerRequestPrice:  float64Ptr(0.2),
			},
		},
	}

	resp := pricingConfigToResponse(ch)
	require.Len(t, resp.ModelPricing, 3)

	require.Equal(t, int64(1), resp.ModelPricing[0].ID)
	require.Equal(t, []string{"claude-sonnet-4"}, resp.ModelPricing[0].Models)
	require.Equal(t, "token", resp.ModelPricing[0].BillingMode)

	require.Equal(t, int64(2), resp.ModelPricing[1].ID)
	require.Equal(t, []string{"gpt-4", "gpt-4o"}, resp.ModelPricing[1].Models)
	require.Equal(t, "per_request", resp.ModelPricing[1].BillingMode)

	require.Equal(t, int64(3), resp.ModelPricing[2].ID)
	require.Equal(t, []string{"gemini-2.5-pro"}, resp.ModelPricing[2].Models)
	require.Equal(t, "image", resp.ModelPricing[2].BillingMode)
	require.Equal(t, float64Ptr(0.05), resp.ModelPricing[2].ImageOutputPrice)
}

// TestPricingRequestToService_Defaults converts a model card with an empty
// billing mode. It checks one service entry is produced and the selected
// default mode is token.
func TestPricingRequestToService_Defaults(t *testing.T) {
	tests := []struct {
		name      string              // default scenario display name
		req       modelPricingRequest // submitted model-card request value
		wantField string              // selected expected default field
		wantValue string              // expected default field value
	}{
		{
			name: "空计费模式默认使用 token",
			req: modelPricingRequest{
				Models:      []string{"m1"},
				BillingMode: "",
			},
			wantField: "BillingMode",
			wantValue: string(routing.BillingModeToken),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pricingRequestToService([]modelPricingRequest{tt.req})
			require.Len(t, result, 1)
			if tt.wantField == "BillingMode" {
				require.Equal(t, routing.BillingMode(tt.wantValue), result[0].BillingMode)
			}
		})
	}
}

// TestPricingRequestToService_WithAllFields converts a per-request card
// carrying model names and optional token, image, and unit amounts. It checks
// the supplied names, mode, factor, and selected amount pointers reach the
// service entry.
func TestPricingRequestToService_WithAllFields(t *testing.T) {
	reqs := []modelPricingRequest{
		{
			Models:           []string{"gpt-4", "gpt-4o"},
			BillingMode:      "per_request",
			PriceMultiplier:  float64Ptr(1.5),
			InputPrice:       float64Ptr(0.01),
			OutputPrice:      float64Ptr(0.03),
			CacheWritePrice:  float64Ptr(0.005),
			CacheReadPrice:   float64Ptr(0.002),
			ImageOutputPrice: float64Ptr(0.04),
			PerRequestPrice:  float64Ptr(0.5),
		},
	}

	result := pricingRequestToService(reqs)
	require.Len(t, result, 1)
	r := result[0]
	require.Equal(t, []string{"gpt-4", "gpt-4o"}, r.Models)
	require.Equal(t, routing.BillingModePerRequest, r.BillingMode)
	require.Equal(t, float64Ptr(1.5), r.PriceMultiplier)
	require.Equal(t, float64Ptr(0.01), r.InputPrice)
	require.Equal(t, float64Ptr(0.03), r.OutputPrice)
	require.Equal(t, float64Ptr(0.005), r.CacheWritePrice)
	require.Equal(t, float64Ptr(0.002), r.CacheReadPrice)
	require.Equal(t, float64Ptr(0.04), r.ImageOutputPrice)
	require.Equal(t, float64Ptr(0.5), r.PerRequestPrice)
}

// TestPricingRequestToService_WithFastModeMultiplier converts a token card
// containing its legacy Fast factor. It checks that the supplied legacy
// pointer remains present in the resulting service entry.
func TestPricingRequestToService_WithFastModeMultiplier(t *testing.T) {
	reqs := []modelPricingRequest{{
		Models:             []string{"gpt-5.4"},
		BillingMode:        string(routing.BillingModeToken),
		FastModeMultiplier: float64Ptr(2),
		InputPrice:         float64Ptr(0.01),
	}}

	result := pricingRequestToService(reqs)
	require.Len(t, result, 1)
	require.Equal(t, float64Ptr(2), result[0].FastModeMultiplier)
}

// TestPricingRequestToService_WithTierMultipliers converts Fast/Flex factors
// and an interval with per-bucket scaling. It checks the outer factors and
// the interval's input and cache-read multiplier pointers survive conversion.
func TestPricingRequestToService_WithTierMultipliers(t *testing.T) {
	result := pricingRequestToService([]modelPricingRequest{{
		Models:         []string{"claude-opus-4-8"},
		BillingMode:    string(routing.BillingModeToken),
		FastMultiplier: float64Ptr(2),
		FlexMultiplier: float64Ptr(0.5),
		Intervals: []pricingIntervalRequest{{
			InputMultiplier:      float64Ptr(1.1),
			OutputMultiplier:     float64Ptr(1.2),
			CacheWriteMultiplier: float64Ptr(1.3),
			CacheReadMultiplier:  float64Ptr(1.4),
		}},
	}})

	require.Len(t, result, 1)
	require.Equal(t, float64Ptr(2), result[0].FastMultiplier)
	require.Equal(t, float64Ptr(0.5), result[0].FlexMultiplier)
	require.Len(t, result[0].Intervals, 1)
	require.Equal(t, float64Ptr(1.1), result[0].Intervals[0].InputMultiplier)
	require.Equal(t, float64Ptr(1.4), result[0].Intervals[0].CacheReadMultiplier)
}

// TestPricingRequestToService_WithIntervals converts two per-request tiers
// with bounded and unbounded context endpoints. It checks endpoints, labels,
// selected token/unit amount pointers, and ordering fields reach the service
// intervals.
func TestPricingRequestToService_WithIntervals(t *testing.T) {
	reqs := []modelPricingRequest{
		{
			Models:      []string{"m1"},
			BillingMode: "per_request",
			Intervals: []pricingIntervalRequest{
				{
					MinTokens:       0,
					MaxTokens:       pricingConfigIntPtr(2000),
					TierLabel:       "small",
					InputPrice:      float64Ptr(0.01),
					OutputPrice:     float64Ptr(0.02),
					CacheWritePrice: float64Ptr(0.003),
					CacheReadPrice:  float64Ptr(0.001),
					PerRequestPrice: float64Ptr(0.1),
					SortOrder:       1,
				},
				{
					MinTokens: 2000,
					MaxTokens: nil,
					TierLabel: "large",
					SortOrder: 2,
				},
			},
		},
	}

	result := pricingRequestToService(reqs)
	require.Len(t, result, 1)
	require.Len(t, result[0].Intervals, 2)

	iv0 := result[0].Intervals[0]
	require.Equal(t, 0, iv0.MinTokens)
	require.Equal(t, pricingConfigIntPtr(2000), iv0.MaxTokens)
	require.Equal(t, "small", iv0.TierLabel)
	require.Equal(t, float64Ptr(0.01), iv0.InputPrice)
	require.Equal(t, float64Ptr(0.02), iv0.OutputPrice)
	require.Equal(t, float64Ptr(0.003), iv0.CacheWritePrice)
	require.Equal(t, float64Ptr(0.001), iv0.CacheReadPrice)
	require.Equal(t, float64Ptr(0.1), iv0.PerRequestPrice)
	require.Equal(t, 1, iv0.SortOrder)

	iv1 := result[0].Intervals[1]
	require.Equal(t, 2000, iv1.MinTokens)
	require.Nil(t, iv1.MaxTokens)
	require.Equal(t, "large", iv1.TierLabel)
	require.Equal(t, 2, iv1.SortOrder)
}

// TestPricingRequestToService_EmptySlice converts an empty HTTP model-card
// list to service input. It checks the resulting list is empty and nonnil for
// callers that replace stored collections.
func TestPricingRequestToService_EmptySlice(t *testing.T) {
	result := pricingRequestToService([]modelPricingRequest{})
	require.NotNil(t, result)
	require.Empty(t, result)
}

// TestPricingRequestToService_NilPriceFields converts a token card whose
// optional prices and factors were omitted. It checks the selected amount and
// multiplier pointers remain nil, retaining missing-value information for
// pricing consumers.
func TestPricingRequestToService_NilPriceFields(t *testing.T) {
	reqs := []modelPricingRequest{
		{
			Models:      []string{"m1"},
			BillingMode: "token",
		},
	}

	result := pricingRequestToService(reqs)
	require.Len(t, result, 1)
	r := result[0]
	require.Nil(t, r.InputPrice)
	require.Nil(t, r.PriceMultiplier)
	require.Nil(t, r.FastModeMultiplier)
	require.Nil(t, r.OutputPrice)
	require.Nil(t, r.CacheWritePrice)
	require.Nil(t, r.CacheReadPrice)
	require.Nil(t, r.ImageOutputPrice)
	require.Nil(t, r.PerRequestPrice)
}
