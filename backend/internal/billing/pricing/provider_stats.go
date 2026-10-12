package pricing

import (
	"slices"
	"strings"
)

// ProviderStatsInput supplies loaded rules and usage; an absent cost differs from an explicit zero cost.
type ProviderStatsInput struct {
	Rules               []ProviderStatsPricingRule
	ProviderID, GroupID int64
	Models              []string
	Tokens              UsageTokens
	RequestCount        int
}

func UniqueNonEmptyProviderStatsModels(models []string) []string {
	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	return out
}

// TryCustomRules returns the first applicable cost in rule order.
func TryCustomRules(
	rules []ProviderStatsPricingRule, providerID, groupID int64,
	model string, tokens UsageTokens, requestCount int,
) *float64 {
	modelLower := strings.ToLower(model)
	for _, rule := range rules {
		if !MatchProviderStatsRule(&rule, providerID, groupID) {
			continue
		}
		pricing := FindEffectivePricingForModel(rule.Pricing, modelLower)
		if pricing == nil {
			continue // The rule matches the provider or group, but lacks this model's price.
		}
		// Custom statistics prices define an independent cost base and do not inherit user model or effort multipliers.
		if cost := CalculateStatsCost(pricing, tokens, requestCount); cost != nil {
			return cost
		}
	}
	return nil
}

// MatchProviderStatsRule reports whether either the provider or group matches.
// A match requires providerID in ProviderIDs or groupID in GroupIDs.
// A rule with both lists empty does not match.
func MatchProviderStatsRule(rule *ProviderStatsPricingRule, providerID, groupID int64) bool {
	if len(rule.ProviderIDs) == 0 && len(rule.GroupIDs) == 0 {
		return false
	}
	if slices.Contains(rule.ProviderIDs, providerID) {
		return true
	}
	return slices.Contains(rule.GroupIDs, groupID)
}

// FindEffectivePricingForModel selects a priced card for provider cost statistics.
// Empty rows are placeholders; an explicit zero price is a valid cost override.
func FindEffectivePricingForModel(pricingList []ModelPricingEntry, modelLower string) *ModelPricingEntry {
	return FindPricingForModelByPredicate(pricingList, modelLower, func(p *ModelPricingEntry) bool {
		return p != nil && p.HasEffectivePricing()
	})
}

// FindPricingForModelByPredicate prefers exact matches among included rows, then wildcards in configuration order.
func FindPricingForModelByPredicate(pricingList []ModelPricingEntry, modelLower string, include func(*ModelPricingEntry) bool) *ModelPricingEntry {
	if include == nil {
		include = func(*ModelPricingEntry) bool { return true }
	}
	// Exact matches take precedence over every wildcard.
	for i := range pricingList {
		p := &pricingList[i]
		if !include(p) {
			continue
		}
		for _, m := range p.Models {
			if strings.EqualFold(m, modelLower) {
				return p
			}
		}
	}
	// The first matching wildcard in configuration order wins.
	for i := range pricingList {
		p := &pricingList[i]
		if !include(p) {
			continue
		}
		for _, m := range p.Models {
			ml := strings.ToLower(m)
			if !strings.HasSuffix(ml, "*") {
				continue
			}
			prefix := strings.TrimSuffix(ml, "*")
			if strings.HasPrefix(modelLower, prefix) {
				return p
			}
		}
	}
	return nil
}

// CalculateStatsCost computes the card's cost and then applies its optional price multiplier.
func CalculateStatsCost(pricing *ModelPricingEntry, tokens UsageTokens, requestCount int) *float64 {
	if pricing == nil {
		return nil
	}
	var cost *float64
	switch pricing.BillingMode {
	case BillingModePerRequest, BillingModeImage:
		cost = CalculatePerRequestStatsCost(pricing, requestCount)
	default:
		cost = CalculateTokenStatsCost(pricing, tokens)
	}
	if cost == nil {
		return nil
	}
	// Use the same configured price-multiplier handling as request billing.
	if multiplier, configured := NormalizedPriceMultiplier(pricing); configured {
		scaled := *cost * multiplier
		cost = &scaled
	}
	return cost
}

// CalculatePerRequestStatsCost charges the flat request/image price, defaulting a nonpositive count to one.
func CalculatePerRequestStatsCost(pricing *ModelPricingEntry, requestCount int) *float64 {
	if pricing.PerRequestPrice == nil {
		return nil
	}
	if requestCount <= 0 {
		requestCount = 1
	}
	cost := *pricing.PerRequestPrice * float64(requestCount)
	if cost < 0 {
		return nil
	}
	return &cost
}

// CalculateTokenStatsCost computes provider costs from token usage.
// Interval selection counts input, output, and cache tokens together.
// A matching interval replaces flat token prices; omitted interval buckets cost zero.
func CalculateTokenStatsCost(pricing *ModelPricingEntry, tokens UsageTokens) *float64 {
	p := pricing
	if validIntervals := FilterValidTokenIntervals(pricing.Intervals); len(validIntervals) > 0 {
		totalTokens := tokens.InputTokens + tokens.OutputTokens + tokens.CacheCreationTokens + tokens.CacheReadTokens
		if iv := FindMatchingInterval(validIntervals, totalTokens); iv != nil {
			p = &ModelPricingEntry{
				InputPrice:        iv.InputPrice,
				OutputPrice:       iv.OutputPrice,
				CacheWritePrice:   iv.CacheWritePrice,
				CacheWrite1hPrice: iv.CacheWrite1hPrice,
				CacheReadPrice:    iv.CacheReadPrice,
				ImageOutputPrice:  pricing.ImageOutputPrice,
			}
		}
	}
	if !HasAnyTokenStatsPrice(p) || !HasAnyStatsTokenUsage(tokens) {
		return nil
	}
	deref := func(ptr *float64) float64 {
		if ptr == nil {
			return 0
		}
		return *ptr
	}
	cacheCreationCost := float64(tokens.CacheCreationTokens) * deref(p.CacheWritePrice)
	if p.CacheWrite1hPrice != nil {
		cache5m, cache1h := NormalizeCacheCreationBreakdown(tokens)
		if cache5m > 0 || cache1h > 0 {
			cacheCreationCost = float64(cache5m)*deref(p.CacheWritePrice) +
				float64(cache1h)*deref(p.CacheWrite1hPrice)
		}
	}
	cost := float64(tokens.InputTokens)*deref(p.InputPrice) +
		float64(tokens.OutputTokens)*deref(p.OutputPrice) +
		cacheCreationCost +
		float64(tokens.CacheReadTokens)*deref(p.CacheReadPrice) +
		float64(tokens.ImageOutputTokens)*deref(p.ImageOutputPrice)
	if cost < 0 {
		return nil
	}
	return &cost
}

func HasAnyTokenStatsPrice(pricing *ModelPricingEntry) bool {
	return pricing != nil && (pricing.InputPrice != nil ||
		pricing.OutputPrice != nil ||
		pricing.CacheWritePrice != nil ||
		pricing.CacheWrite1hPrice != nil ||
		pricing.CacheReadPrice != nil ||
		pricing.ImageOutputPrice != nil)
}

func HasAnyStatsTokenUsage(tokens UsageTokens) bool {
	return tokens.InputTokens > 0 ||
		tokens.OutputTokens > 0 ||
		tokens.CacheCreationTokens > 0 ||
		tokens.CacheReadTokens > 0 ||
		tokens.ImageOutputTokens > 0
}

// ResolveProviderStatsOverride returns handled=true for a custom cost, including zero; false allows catalog lookup.
func ResolveProviderStatsOverride(input ProviderStatsInput) (*float64, bool) {
	for _, model := range input.Models {
		if cost := TryCustomRules(input.Rules, input.ProviderID, input.GroupID, model, input.Tokens, input.RequestCount); cost != nil {
			return cost, true
		}
	}
	return nil, false
}
