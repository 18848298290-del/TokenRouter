package pricing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMatchProviderStatsRule_BothEmpty_NoMatch supplies a rule with no group or
// provider selectors. The rule must return false for the supplied identifiers,
// leaving later rules eligible.
func TestMatchProviderStatsRule_BothEmpty_NoMatch(t *testing.T) {
	rule := &ProviderStatsPricingRule{}
	require.False(t, MatchProviderStatsRule(rule, 1, 10))
}

// TestMatchProviderStatsRule_ProviderIDMatch supplies a rule selecting several
// providers and a matching provider ID. Matching that provider must succeed
// despite the unrelated group identifier.
func TestMatchProviderStatsRule_ProviderIDMatch(t *testing.T) {
	rule := &ProviderStatsPricingRule{ProviderIDs: []int64{1, 2, 3}}
	require.True(t, MatchProviderStatsRule(rule, 2, 999))
}

// TestMatchProviderStatsRule_GroupIDMatch supplies a group selector containing
// the requested group ID. The rule must match even though its provider selector
// has no entries.
func TestMatchProviderStatsRule_GroupIDMatch(t *testing.T) {
	rule := &ProviderStatsPricingRule{GroupIDs: []int64{10, 20}}
	require.True(t, MatchProviderStatsRule(rule, 999, 20))
}

// TestMatchProviderStatsRule_BothConfigured_ProviderMatch configures both
// selector lists with only the provider matching. The result must be true,
// protecting provider-or-group selection for this rule.
func TestMatchProviderStatsRule_BothConfigured_ProviderMatch(t *testing.T) {
	rule := &ProviderStatsPricingRule{
		ProviderIDs: []int64{1, 2},
		GroupIDs:    []int64{10, 20},
	}
	require.True(t, MatchProviderStatsRule(rule, 2, 999))
}

// TestMatchProviderStatsRule_BothConfigured_GroupMatch configures both selector
// lists with only the group matching. The result must be true, allowing that
// group to select the rule.
func TestMatchProviderStatsRule_BothConfigured_GroupMatch(t *testing.T) {
	rule := &ProviderStatsPricingRule{
		ProviderIDs: []int64{1, 2},
		GroupIDs:    []int64{10, 20},
	}
	require.True(t, MatchProviderStatsRule(rule, 999, 10))
}

// TestMatchProviderStatsRule_BothConfigured_NeitherMatch supplies identifiers
// absent from both configured selector lists. The rule must return false so a
// later applicable price rule can be considered.
func TestMatchProviderStatsRule_BothConfigured_NeitherMatch(t *testing.T) {
	rule := &ProviderStatsPricingRule{
		ProviderIDs: []int64{1, 2},
		GroupIDs:    []int64{10, 20},
	}
	require.False(t, MatchProviderStatsRule(rule, 999, 999))
}

// TestFindPricingForModel uses an unrestricted predicate with exact and
// wildcard model cards. It checks case-insensitive exact precedence,
// first-wildcard ordering, vendor-independent card membership, and nil for
// unresolved names or lists.
func TestFindPricingForModel(t *testing.T) {
	exactPricing := ModelPricingEntry{
		ID:     1,
		Models: []string{"claude-opus-4"},
	}
	wildcardPricing := ModelPricingEntry{
		ID:     2,
		Models: []string{"claude-*"},
	}
	additionalPricing := ModelPricingEntry{
		ID: 3,

		Models: []string{"gpt-4o"},
	}
	geminiPricing := ModelPricingEntry{
		ID:     4,
		Models: []string{"gemini-2.5-pro"},
	}

	tests := []struct {
		name  string              // subtest display case label
		list  []ModelPricingEntry // ordered candidate price cards
		model string              // queried complete model name
		// wantNil selects the absence assertion; wantID is checked only for a
		// returned card. The table therefore distinguishes no match from a selected
		// stored identifier.
		wantID  int64 // expected selected card identifier
		wantNil bool  // expected absent matching card
	}{
		{
			name:   "exact match",
			list:   []ModelPricingEntry{exactPricing},
			model:  "claude-opus-4",
			wantID: 1,
		},
		{
			name:   "exact match case insensitive",
			list:   []ModelPricingEntry{{ID: 5, Models: []string{"Claude-Opus-4"}}},
			model:  "claude-opus-4",
			wantID: 5,
		},
		{
			name:   "wildcard match",
			list:   []ModelPricingEntry{wildcardPricing},
			model:  "claude-opus-4",
			wantID: 2,
		},
		{
			name:   "exact match takes priority over wildcard",
			list:   []ModelPricingEntry{wildcardPricing, exactPricing},
			model:  "claude-opus-4",
			wantID: 1,
		},
		{
			name:   "same card includes different model vendors",
			list:   []ModelPricingEntry{exactPricing, additionalPricing, geminiPricing},
			model:  "gpt-4o",
			wantID: 3,
		},
		{
			name:   "Gemini model shares the same card",
			list:   []ModelPricingEntry{exactPricing, additionalPricing, geminiPricing},
			model:  "gemini-2.5-pro",
			wantID: 4,
		},
		{
			name:    "no match at all",
			list:    []ModelPricingEntry{exactPricing, wildcardPricing},
			model:   "gpt-4o",
			wantNil: true,
		},
		{
			name:    "empty list returns nil",
			list:    nil,
			model:   "claude-opus-4",
			wantNil: true,
		},
		{
			name: "wildcard matches by config order (first match wins)",
			list: []ModelPricingEntry{
				{ID: 10, Models: []string{"claude-*"}},
				{ID: 11, Models: []string{"claude-opus-*"}},
			},
			model:  "claude-opus-4",
			wantID: 10, // claude-* is the first matching wildcard.
		},
		{
			name: "shorter wildcard used when longer does not match",
			list: []ModelPricingEntry{
				{ID: 10, Models: []string{"claude-*"}},
				{ID: 11, Models: []string{"claude-opus-*"}},
			},
			model:  "claude-sonnet-4",
			wantID: 10, // Matches claude-*.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FindPricingForModelByPredicate(tt.list, tt.model, nil)
			if tt.wantNil {
				require.Nil(t, result)
				return
			}
			require.NotNil(t, result)
			require.Equal(t, tt.wantID, result.ID)
		})
	}
}

// TestCalculateStatsCost_NilPricing attempts a statistics calculation with an
// absent price card. The returned nil cost must leave the caller free to try
// another price source.
func TestCalculateStatsCost_NilPricing(t *testing.T) {
	result := CalculateStatsCost(nil, UsageTokens{}, 1)
	require.Nil(t, result)
}

// TestCalculateStatsCost_TokenBilling supplies separate input and output rates
// with positive usage. The statistics total must equal the sum of both
// independently priced token counts.
func TestCalculateStatsCost_TokenBilling(t *testing.T) {
	pricing := &ModelPricingEntry{
		BillingMode: BillingModeToken,
		InputPrice:  contractFloat(0.001),
		OutputPrice: contractFloat(0.002),
	}
	tokens := UsageTokens{
		InputTokens:  100,
		OutputTokens: 50,
	}
	result := CalculateStatsCost(pricing, tokens, 1)
	require.NotNil(t, result)
	// Input and output USD components combine decimal float64 prices; permit
	// binary rounding in their summed expected cost.
	require.InDelta(t, 0.2, *result, 1e-12)
}

// TestCalculateStatsCost_TokenBilling_WithCache adds cache creation and read
// usage to text input and output. The resulting statistics amount must include
// each programmed token bucket.
func TestCalculateStatsCost_TokenBilling_WithCache(t *testing.T) {
	pricing := &ModelPricingEntry{
		BillingMode:     BillingModeToken,
		InputPrice:      contractFloat(0.001),
		OutputPrice:     contractFloat(0.002),
		CacheWritePrice: contractFloat(0.003),
		CacheReadPrice:  contractFloat(0.0005),
	}
	tokens := UsageTokens{
		InputTokens:         100,
		OutputTokens:        50,
		CacheCreationTokens: 200,
		CacheReadTokens:     300,
	}
	result := CalculateStatsCost(pricing, tokens, 1)
	require.NotNil(t, result)
	// The text and aggregate cache buckets sum four decimal amounts; allow float64
	// rounding residue in that total.
	require.InDelta(t, 0.95, *result, 1e-12)
}

// TestCalculateStatsCost_TokenBilling_WithCacheTTLPricing supplies separate
// short and hourly cache rates with matching usage details. The result must
// equal the independently priced 40-token and 60-token TTL amounts.
func TestCalculateStatsCost_TokenBilling_WithCacheTTLPricing(t *testing.T) {
	pricing := &ModelPricingEntry{
		BillingMode:       BillingModeToken,
		CacheWritePrice:   contractFloat(0.003),
		CacheWrite1hPrice: contractFloat(0.006),
	}
	tokens := UsageTokens{
		CacheCreationTokens:   100,
		CacheCreation5mTokens: 40,
		CacheCreation1hTokens: 60,
	}
	result := CalculateStatsCost(pricing, tokens, 1)
	require.NotNil(t, result)
	// Separately priced forty short-TTL and sixty hourly tokens produce decimal
	// USD components; allow binary rounding when summing them.
	require.InDelta(t, 0.48, *result, 1e-12)
}

// TestCalculateStatsCost_TokenBilling_WithImageOutput supplies text output and
// image-output counts with separate rates. Statistics must charge both supplied
// counts additively and produce the expected total.
func TestCalculateStatsCost_TokenBilling_WithImageOutput(t *testing.T) {
	pricing := &ModelPricingEntry{
		BillingMode:      BillingModeToken,
		InputPrice:       contractFloat(0.001),
		OutputPrice:      contractFloat(0.002),
		ImageOutputPrice: contractFloat(0.01),
	}
	tokens := UsageTokens{
		InputTokens:       100,
		OutputTokens:      50,
		ImageOutputTokens: 10,
	}
	result := CalculateStatsCost(pricing, tokens, 1)
	require.NotNil(t, result)
	// The supplied text and image-output counts contribute independent decimal USD
	// amounts; tolerate binary rounding in their additive total.
	require.InDelta(t, 0.3, *result, 1e-12)
}

// TestCalculateStatsCost_TokenBilling_PartialPricesNil supplies only an input
// rate while reporting output and cache usage. The resulting cost must charge
// input alone and keep omitted bucket prices at zero.
func TestCalculateStatsCost_TokenBilling_PartialPricesNil(t *testing.T) {
	pricing := &ModelPricingEntry{
		BillingMode: BillingModeToken,
		InputPrice:  contractFloat(0.001),
		// Omitted output and cache prices count as zero.
	}
	tokens := UsageTokens{
		InputTokens:         100,
		OutputTokens:        50,
		CacheCreationTokens: 200,
	}
	result := CalculateStatsCost(pricing, tokens, 1)
	require.NotNil(t, result)
	// The input-only amount multiplies a decimal unit rate by usage; permit
	// float64 representation residue in its comparison.
	require.InDelta(t, 0.1, *result, 1e-12)
}

// TestCalculateStatsCost_TokenBilling_AllTokensZero supplies a priced card with
// no reported usage. The result must be nil, allowing subsequent price-source
// selection instead of a handled zero override.
func TestCalculateStatsCost_TokenBilling_AllTokensZero(t *testing.T) {
	pricing := &ModelPricingEntry{
		BillingMode: BillingModeToken,
		InputPrice:  contractFloat(0.001),
		OutputPrice: contractFloat(0.002),
	}
	tokens := UsageTokens{} // All usage is zero.
	result := CalculateStatsCost(pricing, tokens, 1)
	// Zero usage yields nil, allowing the caller to continue price resolution.
	require.Nil(t, result)
}

// TestCalculateStatsCost_TokenBilling_ExplicitZeroPriceOverrides combines
// positive usage with a present zero input price. The calculation must return a
// present zero cost so it remains an applicable override.
func TestCalculateStatsCost_TokenBilling_ExplicitZeroPriceOverrides(t *testing.T) {
	pricing := &ModelPricingEntry{
		BillingMode: BillingModeToken,
		InputPrice:  contractFloat(0),
	}
	tokens := UsageTokens{InputTokens: 100, OutputTokens: 50}
	result := CalculateStatsCost(pricing, tokens, 1)
	require.NotNil(t, result)
	require.Zero(t, *result)
}

// TestCalculateStatsCost_TokenBilling_BlankPricingDoesNotOverride supplies
// positive usage and a card containing no token prices. The returned nil cost
// must leave another rule or catalog price eligible.
func TestCalculateStatsCost_TokenBilling_BlankPricingDoesNotOverride(t *testing.T) {
	pricing := &ModelPricingEntry{
		BillingMode: BillingModeToken,
	}
	tokens := UsageTokens{InputTokens: 100}
	result := CalculateStatsCost(pricing, tokens, 1)
	require.Nil(t, result)
}

// TestCalculateStatsCost_PerRequestBilling supplies token usage beside a flat
// price and three requests. The result must equal the flat amount times request
// count regardless of those token counts.
func TestCalculateStatsCost_PerRequestBilling(t *testing.T) {
	pricing := &ModelPricingEntry{
		BillingMode:     BillingModePerRequest,
		PerRequestPrice: contractFloat(0.05),
	}
	tokens := UsageTokens{InputTokens: 999, OutputTokens: 999}
	result := CalculateStatsCost(pricing, tokens, 3)
	require.NotNil(t, result)
	// Three flat request amounts multiply a decimal USD unit price; allow binary
	// rounding in the expected request total.
	require.InDelta(t, 0.15, *result, 1e-12)
}

// TestCalculateStatsCost_PerRequestBilling_PriceNil selects request mode with
// its flat amount omitted. The returned nil cost must leave the missing-price
// case distinguishable from free billing.
func TestCalculateStatsCost_PerRequestBilling_PriceNil(t *testing.T) {
	pricing := &ModelPricingEntry{
		BillingMode: BillingModePerRequest,
		// Per-request price intentionally omitted.
	}
	result := CalculateStatsCost(pricing, UsageTokens{}, 1)
	require.Nil(t, result)
}

// TestCalculateStatsCost_PerRequestBilling_ExplicitZeroPrice selects request
// mode with a present zero amount. The result must remain present and zero so
// callers recognize a free override.
func TestCalculateStatsCost_PerRequestBilling_ExplicitZeroPrice(t *testing.T) {
	pricing := &ModelPricingEntry{
		BillingMode:     BillingModePerRequest,
		PerRequestPrice: contractFloat(0),
	}
	result := CalculateStatsCost(pricing, UsageTokens{}, 1)
	require.NotNil(t, result)
	require.Zero(t, *result)
}

// TestCalculateStatsCost_ImageBilling supplies a flat image amount and a count
// of two. The returned statistics cost must equal two copies of that unit
// amount.
func TestCalculateStatsCost_ImageBilling(t *testing.T) {
	pricing := &ModelPricingEntry{
		BillingMode:     BillingModeImage,
		PerRequestPrice: contractFloat(0.10),
	}
	result := CalculateStatsCost(pricing, UsageTokens{}, 2)
	require.NotNil(t, result)
	// Two flat image amounts multiply a decimal USD price; permit float64
	// representation residue in the expected image total.
	require.InDelta(t, 0.20, *result, 1e-12)
}

// TestCalculateStatsCost_ImageBilling_PriceNil selects image mode with the flat
// amount omitted. The calculation must return nil, allowing later rules or the
// catalog to resolve the cost.
func TestCalculateStatsCost_ImageBilling_PriceNil(t *testing.T) {
	pricing := &ModelPricingEntry{
		BillingMode: BillingModeImage,
		// Per-request price intentionally omitted.
	}
	result := CalculateStatsCost(pricing, UsageTokens{}, 1)
	require.Nil(t, result)
}

// TestCalculateStatsCost_AppliesPriceMultiplier applies a paid token multiplier
// and a zero request multiplier. The token amount must scale by 1.5, while the
// request override remains present at zero.
func TestCalculateStatsCost_AppliesPriceMultiplier(t *testing.T) {
	t.Run("token 定价", func(t *testing.T) {
		pricing := &ModelPricingEntry{
			BillingMode:     BillingModeToken,
			PriceMultiplier: contractFloat(1.5),
			InputPrice:      contractFloat(0.001),
		}
		result := CalculateStatsCost(pricing, UsageTokens{InputTokens: 100}, 1)
		require.NotNil(t, result)
		// The paid token total combines its decimal rate and 1.5 factor; allow
		// multiplication rounding in that expected USD amount.
		require.InDelta(t, 0.15, *result, 1e-12)
	})

	t.Run("按次定价", func(t *testing.T) {
		pricing := &ModelPricingEntry{
			BillingMode:     BillingModePerRequest,
			PriceMultiplier: contractFloat(0),
			PerRequestPrice: contractFloat(0.25),
		}
		result := CalculateStatsCost(pricing, UsageTokens{}, 2)
		require.NotNil(t, result)
		require.Zero(t, *result)
	})
}

// TestCalculateStatsCost_DefaultBillingMode_FallsToToken leaves the mode empty
// while supplying input and output prices. The resulting cost must use both
// token counts and their configured rates.
func TestCalculateStatsCost_DefaultBillingMode_FallsToToken(t *testing.T) {
	// An empty BillingMode uses token billing.
	pricing := &ModelPricingEntry{
		InputPrice:  contractFloat(0.001),
		OutputPrice: contractFloat(0.002),
	}
	tokens := UsageTokens{
		InputTokens:  100,
		OutputTokens: 50,
	}
	result := CalculateStatsCost(pricing, tokens, 1)
	require.NotNil(t, result)
	// Default-mode input and output charges use decimal USD token rates; tolerate
	// float64 residue in their summed amount.
	require.InDelta(t, 0.2, *result, 1e-12)
}

// TestTryCustomRules_FirstMatchWins supplies two applicable group rules with
// different rates for the same model. The returned amount must use the first
// rule in the supplied list.
func TestTryCustomRules_FirstMatchWins(t *testing.T) {
	configPricing := &struct {
		// ordered test statistics-price rules
		ProviderStatsPricingRules []ProviderStatsPricingRule
	}{
		ProviderStatsPricingRules: []ProviderStatsPricingRule{
			{
				GroupIDs: []int64{1},
				Pricing: []ModelPricingEntry{
					{ID: 100, Models: []string{"claude-opus-4"}, InputPrice: contractFloat(0.01), OutputPrice: contractFloat(0.02)},
				},
			},
			{
				GroupIDs: []int64{1},
				Pricing: []ModelPricingEntry{
					{ID: 200, Models: []string{"claude-opus-4"}, InputPrice: contractFloat(0.99), OutputPrice: contractFloat(0.99)},
				},
			},
		},
	}
	tokens := UsageTokens{InputTokens: 100, OutputTokens: 50}
	result := TryCustomRules(configPricing.ProviderStatsPricingRules, 999, 1, "claude-opus-4", tokens, 1)
	require.NotNil(t, result)
	// The first card's input and output prices add two decimal USD components;
	// allow binary rounding in that selected cost.
	require.InDelta(t, 2.0, *result, 1e-12)
}

// TestTryCustomRules_SkipsNonMatchingRules places a mismatched provider rule
// before a matching group rule. The returned cost must use the later group's
// input price.
func TestTryCustomRules_SkipsNonMatchingRules(t *testing.T) {
	configPricing := &struct {
		// ordered test statistics-price rules
		ProviderStatsPricingRules []ProviderStatsPricingRule
	}{
		ProviderStatsPricingRules: []ProviderStatsPricingRule{
			{
				ProviderIDs: []int64{888}, // Provider does not match.
				Pricing: []ModelPricingEntry{
					{ID: 100, Models: []string{"claude-opus-4"}, InputPrice: contractFloat(0.99)},
				},
			},
			{
				GroupIDs: []int64{1}, // Group matches.
				Pricing: []ModelPricingEntry{
					{ID: 200, Models: []string{"claude-opus-4"}, InputPrice: contractFloat(0.05)},
				},
			},
		},
	}
	tokens := UsageTokens{InputTokens: 100}
	result := TryCustomRules(configPricing.ProviderStatsPricingRules, 999, 1, "claude-opus-4", tokens, 1)
	require.NotNil(t, result)
	// The later group's input cost uses its decimal unit amount; allow
	// multiplication rounding in the expected USD total.
	require.InDelta(t, 5.0, *result, 1e-12)
}

// TestTryCustomRules_NoMatch_ReturnsNil supplies a rule whose provider and
// group selectors both miss. The resolver must return nil so its caller can
// attempt catalog pricing.
func TestTryCustomRules_NoMatch_ReturnsNil(t *testing.T) {
	configPricing := &struct {
		// ordered test statistics-price rules
		ProviderStatsPricingRules []ProviderStatsPricingRule
	}{
		ProviderStatsPricingRules: []ProviderStatsPricingRule{
			{
				ProviderIDs: []int64{888},
				Pricing: []ModelPricingEntry{
					{ID: 100, Models: []string{"claude-opus-4"}, InputPrice: contractFloat(0.01)},
				},
			},
		},
	}
	tokens := UsageTokens{InputTokens: 100}
	result := TryCustomRules(configPricing.ProviderStatsPricingRules, 999, 2, "claude-opus-4", tokens, 1)
	require.Nil(t, result) // Neither provider nor group matches.
}

// TestTryCustomRules_RuleMatchesButModelNot_ContinuesToNext supplies an
// applicable group rule for a different model. Resolution must continue to the
// later matching model card and return its input cost.
func TestTryCustomRules_RuleMatchesButModelNot_ContinuesToNext(t *testing.T) {
	configPricing := &struct {
		// ordered test statistics-price rules
		ProviderStatsPricingRules []ProviderStatsPricingRule
	}{
		ProviderStatsPricingRules: []ProviderStatsPricingRule{
			{
				GroupIDs: []int64{1},
				Pricing: []ModelPricingEntry{
					{ID: 100, Models: []string{"gpt-4o"}, InputPrice: contractFloat(0.01)}, // Model does not match.
				},
			},
			{
				GroupIDs: []int64{1},
				Pricing: []ModelPricingEntry{
					{ID: 200, Models: []string{"claude-opus-4"}, InputPrice: contractFloat(0.05)}, // Model matches.
				},
			},
		},
	}
	tokens := UsageTokens{InputTokens: 100}
	result := TryCustomRules(configPricing.ProviderStatsPricingRules, 999, 1, "claude-opus-4", tokens, 1)
	require.NotNil(t, result)
	// The model-matched second card supplies a decimal input rate; permit
	// multiplication residue in its expected USD total.
	require.InDelta(t, 5.0, *result, 1e-12) // Uses the second rule.
}

// contractFloat returns separately allocated storage for the supplied test-card
// amount.
func contractFloat(v float64) *float64 { return &v }
