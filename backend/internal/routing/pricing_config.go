package routing

import (
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

const (
	BillingModeToken      = pricing.BillingModeToken
	BillingModePerRequest = pricing.BillingModePerRequest
	BillingModeImage      = pricing.BillingModeImage
	BillingModeVideo      = pricing.BillingModeVideo

	BillingModelSourceRequested   = "requested"
	BillingModelSourceUpstream    = "upstream"
	BillingModelSourceGroupMapped = "group_mapped"
)

// BillingMode is the shared price card's billing mode.
type BillingMode = pricing.BillingMode

// PricingConfig contains shared prices and billing settings for associated groups.
type PricingConfig struct {
	pricing.BillingSettings
	ID                 int64
	Name               string
	Description        string
	Status             string
	BillingModelSource string // "requested", "upstream", or "group_mapped"

	CreatedAt time.Time
	UpdatedAt time.Time

	// Associated groups share this configuration's prices.
	GroupIDs []int64

	ModelPricing []ModelPricingEntry

	// Provider-cost rules are ordered by SortOrder; the first matching rule applies.
	ProviderStatsPricingRules []ProviderStatsPricingRule
}

// ProviderStatsPricingRule defines prices used to calculate provider costs.
type ProviderStatsPricingRule = pricing.ProviderStatsPricingRule

// ModelPricingEntry is a model price card in a shared configuration.
type ModelPricingEntry = pricing.ModelPricingEntry

// TimePricingConfig defines daily time-based multipliers.
type TimePricingConfig = pricing.TimePricingConfig

// TimePricingPeriod defines one daily pricing period.
type TimePricingPeriod = pricing.TimePricingPeriod

// PricingInterval defines prices for a context range or media specification.
type PricingInterval = pricing.PricingInterval

// PricingUsageFields carries routing and pricing metadata into platform usage records.
type PricingUsageFields struct {
	PricingConfigID    int64  // Zero means no associated pricing configuration.
	OriginalModel      string // Request model after key redirection and before group mapping.
	GroupMappedModel   string // Group-mapped model; equals OriginalModel when no mapping applies.
	BillingModelSource string // Price source: requested, upstream, or group_mapped.
	ModelMappingChain  string // Display chain, such as "a→b→c".
}

// IsActive reports whether the configuration is enabled.
func (c *PricingConfig) IsActive() bool {
	return c.Status == StatusActive
}

// GetModelPricing returns a cloned exact match, or nil when none exists.
// Matching is case-insensitive; wildcard rules are not evaluated here.
func (c *PricingConfig) GetModelPricing(model string) *ModelPricingEntry {
	modelLower := strings.ToLower(model)

	for i := range c.ModelPricing {
		for _, m := range c.ModelPricing[i].Models {
			if strings.EqualFold(m, modelLower) {
				cp := c.ModelPricing[i].Clone()
				return &cp
			}
		}
	}

	return nil
}

// Clone returns a deep copy, or nil for a nil receiver.
func (c *PricingConfig) Clone() *PricingConfig {
	if c == nil {
		return nil
	}
	cp := *c
	cp.BillingSettings = c.BillingSettings.Clone()
	if c.GroupIDs != nil {
		cp.GroupIDs = make([]int64, len(c.GroupIDs))
		copy(cp.GroupIDs, c.GroupIDs)
	}
	if c.ModelPricing != nil {
		cp.ModelPricing = make([]ModelPricingEntry, len(c.ModelPricing))
		for i := range c.ModelPricing {
			cp.ModelPricing[i] = c.ModelPricing[i].Clone()
		}
	}

	if c.ProviderStatsPricingRules != nil {
		cp.ProviderStatsPricingRules = make([]ProviderStatsPricingRule, len(c.ProviderStatsPricingRules))
		for i, rule := range c.ProviderStatsPricingRules {
			cp.ProviderStatsPricingRules[i] = rule
			if rule.GroupIDs != nil {
				cp.ProviderStatsPricingRules[i].GroupIDs = make([]int64, len(rule.GroupIDs))
				copy(cp.ProviderStatsPricingRules[i].GroupIDs, rule.GroupIDs)
			}
			if rule.ProviderIDs != nil {
				cp.ProviderStatsPricingRules[i].ProviderIDs = make([]int64, len(rule.ProviderIDs))
				copy(cp.ProviderStatsPricingRules[i].ProviderIDs, rule.ProviderIDs)
			}
			if rule.Pricing != nil {
				cp.ProviderStatsPricingRules[i].Pricing = make([]ModelPricingEntry, len(rule.Pricing))
				for j := range rule.Pricing {
					cp.ProviderStatsPricingRules[i].Pricing[j] = rule.Pricing[j].Clone()
				}
			}
		}
	}
	return &cp
}

// ValidateIntervals delegates interval validation to the shared pricing package.
func ValidateIntervals(intervals []PricingInterval, mode BillingMode) error {
	return pricing.ValidateIntervals(intervals, mode)
}
