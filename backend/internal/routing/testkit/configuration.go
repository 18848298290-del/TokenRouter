package testkit

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Combined test fixtures carry prices and group policy in one value, while
// production reads those inputs separately. Adapters expose fixture prices to
// cache building and fixture policy to group lookup, retaining production model
// matching and feature evaluation in tests.

// Configuration carries combined pricing and group-policy inputs for
// production-service test fixtures.
type Configuration struct {
	// BillingSettings supplies fixture amounts and billing rules. Price clones
	// supplied settings or uses production defaults for nil; Configuration.Clone
	// retains this pointer.
	BillingSettings *pricing.BillingSettings
	ID              int64  // fixture configuration identity
	Name            string // fixture configuration name
	Description     string // fixture configuration description
	// Status is copied into pricing and enables the generated group policy only
	// when exactly active. Disabled or empty values leave that policy disabled.
	Status string
	// BillingModelSource selects the pricing model stage and also becomes the
	// allowlist stage when Policy adapts this combined fixture into group rules.
	BillingModelSource string
	RestrictModels     bool   // generated model-allowlist activation flag
	Features           string // fixture descriptive feature text
	// FeaturesConfig holds platform feature values shared by Policy. Clone
	// delegates their nested copying to GroupRoutingPolicy.Clone, so tests follow
	// that copier's supported value rules.
	FeaturesConfig map[string]any
	CreatedAt      time.Time // fixture configuration creation timestamp
	UpdatedAt      time.Time // fixture configuration update timestamp
	// GroupIDs, ModelPricing, and ProviderStatsPricingRules pass directly through
	// Price; Clone copies their nested pricing values. Policy copies ModelMapping
	// and derives allowed names from ModelPricing.
	GroupIDs     []int64                     // associated fixture group identities
	ModelPricing []routing.ModelPricingEntry // fixture customer model prices
	ModelMapping map[string]string           // fixture group mapping rules
	// fixture provider-cost pricing rules
	ProviderStatsPricingRules []routing.ProviderStatsPricingRule
}

// configurationRows adapts combined fixtures into production pricing and
// group-policy reads.
type configurationRows struct {
	// PricingConfigRepository supplies promoted methods absent from this read
	// adapter. Its zero value supports only the implemented fixture reads, so
	// tests avoid other promoted calls.
	routing.PricingConfigRepository
	// source is retained for list, platform, and group reads. Callbacks may share
	// mutable fixture data, so tests keep it stable during service use.
	source interface {
		// ListAll returns combined fixtures for price conversion and group lookup,
		// including any source error. Callers follow the source's ownership rules for
		// its returned slice and nested values.
		ListAll(context.Context) ([]Configuration, error)
		// GetGroupPlatforms supplies platform metadata for the requested groups.
		// Fixture sources may return a shared complete map, so callers retain its
		// values without modifying source-owned entries.
		GetGroupPlatforms(context.Context, []int64) (map[int64]string, error)
	}
}

// Price creates production pricing data, cloning supplied billing settings or
// using production defaults. Group IDs, model-price entries, and provider-cost
// rules remain shared with the fixture, so mutation requires a later pricing
// clone.
func (c Configuration) Price() routing.PricingConfig {
	settings := pricing.DefaultBillingSettings()
	if c.BillingSettings != nil {
		settings = c.BillingSettings.Clone()
	}
	return routing.PricingConfig{BillingSettings: settings, ID: c.ID, Name: c.Name, Description: c.Description, Status: c.Status, BillingModelSource: c.BillingModelSource, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, GroupIDs: c.GroupIDs, ModelPricing: c.ModelPricing, ProviderStatsPricingRules: c.ProviderStatsPricingRules}
}

// Policy creates group rules using active status, fixture mappings, and model
// names from price entries. The mapping and allowed-name slice are copied,
// while FeaturesConfig remains shared with the fixture.
func (c Configuration) Policy() routing.GroupRoutingPolicy {
	var allowed []string
	for _, price := range c.ModelPricing {
		allowed = append(allowed, price.Models...)
	}
	mapping := make(map[string]string)
	maps.Copy(mapping, c.ModelMapping)
	return routing.GroupRoutingPolicy{Enabled: c.Status == routing.StatusActive, ModelMapping: mapping, RestrictModels: c.RestrictModels, RestrictionModelSource: c.BillingModelSource, AllowedModels: allowed, Features: c.Features, FeaturesConfig: c.FeaturesConfig}
}

// Clone copies fixture pricing collections and supported nested policy values,
// returning nil for a nil receiver. BillingSettings remains the input pointer;
// callers needing independent settings clone that pointed-to value separately.
func (c *Configuration) Clone() *Configuration {
	if c == nil {
		return nil
	}
	out := *c
	price := c.Price()
	clone := price.Clone()
	out.GroupIDs, out.ModelPricing, out.ProviderStatsPricingRules = clone.GroupIDs, clone.ModelPricing, clone.ProviderStatsPricingRules
	policy := c.Policy().Clone()
	out.FeaturesConfig = policy.FeaturesConfig
	out.ModelMapping = policy.ModelMapping
	return &out
}

// IsActive reports whether a nonnil fixture has exactly active status. Nil
// receivers return false, allowing test setup to check optional fixture
// configurations before invoking helpers that require a value.
func (c *Configuration) IsActive() bool { return c != nil && c.Status == routing.StatusActive }

// GetModelPricing returns a cloned case-insensitive exact price match, or nil
// when absent. It requires a nonnil fixture and delegates matching to
// production pricing; wildcard patterns are evaluated by the service instead.
func (c *Configuration) GetModelPricing(model string) *routing.ModelPricingEntry {
	price := c.Price()
	return price.GetModelPricing(model)
}

// IsWebSearchEmulationEnabled reads the fixture's platform-specific web-search
// flag through production group policy. Nil fixtures and absent or incorrectly
// typed flag objects return false, matching the policy reader's rules.
func (c *Configuration) IsWebSearchEmulationEnabled(platform string) bool {
	if c == nil {
		return false
	}
	p := routing.GroupPolicyView{GroupRoutingPolicy: c.Policy()}
	return p.IsWebSearchEmulationEnabled(platform)
}

// IsBedrockCCCompatEnabled reads the fixture's Bedrock compatibility override
// through production group policy. Nil fixtures return false; supplied global
// or platform boolean values follow the production override reader's rules.
func (c *Configuration) IsBedrockCCCompatEnabled(platform string) bool {
	if c == nil {
		return false
	}
	p := routing.GroupPolicyView{GroupRoutingPolicy: c.Policy()}
	return p.IsBedrockCCCompatEnabled(platform)
}

// ListAll converts source fixture rows into production price values and returns
// the source error alongside them. Billing settings are copied by Price, while
// pricing collections retain the fixture's ownership rules.
func (r configurationRows) ListAll(ctx context.Context) ([]routing.PricingConfig, error) {
	rows, err := r.source.ListAll(ctx)
	out := make([]routing.PricingConfig, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Price())
	}
	return out, err
}

// GetGroupPlatforms delegates the requested group IDs to the fixture source and
// returns its map and error. Ownership remains with that source, so callers
// respect any shared map values.
func (r configurationRows) GetGroupPlatforms(ctx context.Context, ids []int64) (map[int64]string, error) {
	return r.source.GetGroupPlatforms(ctx, ids)
}

// ReadGroup uses the first fixture associated with id to supply group policy
// and default text protocols. Source failures are returned; an unassociated ID
// returns a group containing that ID and zero-valued policy.
func (r configurationRows) ReadGroup(ctx context.Context, id int64) (*routing.Group, error) {
	rows, err := r.source.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if slices.Contains(row.GroupIDs, id) {
			return &routing.Group{ID: id, AllowedProtocols: capability.DefaultGroupClientProtocols(""), RoutingPolicy: row.Policy()}, nil
		}
	}
	return &routing.Group{ID: id}, nil
}

// NewPricingConfigService builds the production service from combined fixtures
// or a production repository. Combined sources take precedence and supply
// ReadGroup when the first options value lacks it.
// Their adapter implements cache and group reads; other repository calls reach
// its nil embedded repository, so administration uses a production repository.
// Unsupported types panic. A nil interface passes through to production
// construction; callers supply nonnil values for the combined-fixture branch.
func NewPricingConfigService(repo any, invalidator routing.GroupAuthInvalidator, options ...routing.PricingConfigOptions) *routing.PricingConfigService {
	if repo == nil {
		return routing.NewPricingConfigService(nil, invalidator, options...)
	}
	opts := routing.PricingConfigOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	if source, ok := repo.(interface {
		// ListAll supplies Configuration rows, selecting the combined-fixture branch
		// for price and policy adaptation. The adapter converts those rows for the
		// production price cache and reuses them when reading group policy.
		ListAll(context.Context) ([]Configuration, error)
		// GetGroupPlatforms is part of the combined fixture-source shape checked
		// during adapter selection. It remains available for direct metadata reads;
		// production pricing methods do not query platform labels.
		GetGroupPlatforms(context.Context, []int64) (map[int64]string, error)
	}); ok {
		adapter := configurationRows{source: source}
		// Use fixture group reads when no custom reader was supplied, letting tests
		// override policy lookup while still adapting combined rows for pricing.
		if opts.ReadGroup == nil {
			opts.ReadGroup = adapter.ReadGroup
		}
		return routing.NewPricingConfigService(adapter, invalidator, opts)
	}
	productionRepo, ok := repo.(routing.PricingConfigRepository)
	if !ok {
		panic("unsupported pricing fixture repository")
	}
	return routing.NewPricingConfigService(productionRepo, invalidator, opts)
}
