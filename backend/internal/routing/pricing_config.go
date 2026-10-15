package routing

import (
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// pricing_config.go defines shared pricing configurations and copies their
// editable price graphs.
// A shared pricing configuration associates several groups with the same model
// cards and billing settings. Model cards retain optional amount presence, and
// cloned configurations separate mutable price pointers and collections before
// cache consumers or management callers edit them.

// Billing-mode aliases select charging units, while model-source names identify
// the request stage used for group restrictions and shared price resolution.
const (
	BillingModeToken      = pricing.BillingModeToken      // charge by token usage
	BillingModePerRequest = pricing.BillingModePerRequest // charge by request count
	BillingModeImage      = pricing.BillingModeImage      // charge by image specification
	BillingModeVideo      = pricing.BillingModeVideo      // charge by video specification

	BillingModelSourceRequested   = "requested"    // model after Key redirection
	BillingModelSourceUpstream    = "upstream"     // final executed upstream model
	BillingModelSourceGroupMapped = "group_mapped" // model after group mapping
)

// BillingMode identifies the shared price card's unit of charging.
type BillingMode = pricing.BillingMode

// PricingConfig contains shared billing settings and ordered prices associated
// with groups.
type PricingConfig struct {
	pricing.BillingSettings        // shared configuration billing switches
	ID                      int64  // saved pricing configuration ID
	Name                    string // administrative configuration display name
	Description             string // administrative configuration description text
	Status                  string // saved configuration activity status
	// BillingModelSource selects the model stage used by pricing consumers. Empty
	// saved values resolve as group_mapped when ResolveGroupMapping attaches
	// billing metadata.
	BillingModelSource string // requested, upstream, or group_mapped model identity

	CreatedAt time.Time // configuration row creation time
	UpdatedAt time.Time // configuration row update time

	// GroupIDs identifies groups sharing this price configuration. Cache
	// publication clones the collection, while administrative creation initially
	// attaches supplied IDs to its working value.
	GroupIDs []int64 // associated saved group IDs

	// ModelPricing supplies exact and trailing-wildcard lookup entries.
	// Configuration-level settings remain effective for active groups even when
	// this collection has no cards.
	ModelPricing []ModelPricingEntry // ordered shared model cards

	// ProviderStatsPricingRules is loaded by SortOrder then ID in PostgreSQL.
	// Provider-cost selection follows slice order and uses the first matching rule
	// that can calculate the model's cost.
	ProviderStatsPricingRules []ProviderStatsPricingRule // ordered provider-cost pricing rules
}

// ProviderStatsPricingRule contains ordered selectors and model prices for
// provider-cost calculation.
type ProviderStatsPricingRule = pricing.ProviderStatsPricingRule

// ModelPricingEntry contains a model card's optional amounts and billing
// modifiers.
type ModelPricingEntry = pricing.ModelPricingEntry

// TimePricingConfig contains daily multiplier periods and their interpretation
// timezone.
type TimePricingConfig = pricing.TimePricingConfig

// TimePricingPeriod identifies one daily pricing interval and its multiplier.
type TimePricingPeriod = pricing.TimePricingPeriod

// PricingInterval contains context-range or media-label pricing overrides for a
// model card.
type PricingInterval = pricing.PricingInterval

// PricingUsageFields records group mapping identities and billing-source
// metadata for usage.
type PricingUsageFields struct {
	PricingConfigID int64 // associated configuration ID; zero means absent
	// OriginalModel and GroupMappedModel retain request identity before and after
	// group mapping. When mapping does not change the model, ToUsageFields uses
	// the request model for both fields.
	OriginalModel      string // model after Key redirection
	GroupMappedModel   string // model after group mapping
	BillingModelSource string // requested, upstream, or group_mapped price stage
	ModelMappingChain  string // ordered model rewrite display chain
}

// IsActive reports whether this pricing configuration has the active status.
func (c *PricingConfig) IsActive() bool {
	return c.Status == StatusActive
}

// GetModelPricing returns a cloned card for the first exact case-insensitive
// model name. Whitespace is retained in matching, wildcard entries are not
// expanded, and an unmatched name returns nil for the caller's next pricing
// source.
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

// Clone returns separate storage for billing amounts, group IDs, model cards,
// and provider-cost selectors and cards. A nil receiver returns nil;
// ModelPricingEntry.Clone handles each card's amounts, intervals, and
// time-pricing periods.
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

// ValidateIntervals delegates model-card interval checking to the shared
// pricing package. Empty intervals are accepted; billing mode determines
// whether context overlap is forbidden or ordered media-label selection can
// handle overlapping ranges.
func ValidateIntervals(intervals []PricingInterval, mode BillingMode) error {
	return pricing.ValidateIntervals(intervals, mode)
}
