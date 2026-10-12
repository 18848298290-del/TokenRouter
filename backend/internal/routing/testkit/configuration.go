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

// Configuration combines pricing and group-policy inputs for test fixtures.
// Test assembly separates them into the production service's pricing and policy sources.
type Configuration struct {
	BillingSettings           *pricing.BillingSettings
	ID                        int64
	Name                      string
	Description               string
	Status                    string
	BillingModelSource        string
	RestrictModels            bool
	Features                  string
	FeaturesConfig            map[string]any
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
	GroupIDs                  []int64
	ModelPricing              []routing.ModelPricingEntry
	ModelMapping              map[string]string
	ProviderStatsPricingRules []routing.ProviderStatsPricingRule
}

// configurationRows separates combined fixtures into pricing and group-policy reads.
type configurationRows struct {
	routing.PricingConfigRepository
	source interface {
		ListAll(context.Context) ([]Configuration, error)
		GetGroupPlatforms(context.Context, []int64) (map[int64]string, error)
	}
}

func (c Configuration) Price() routing.PricingConfig {
	settings := pricing.DefaultBillingSettings()
	if c.BillingSettings != nil {
		settings = c.BillingSettings.Clone()
	}
	return routing.PricingConfig{BillingSettings: settings, ID: c.ID, Name: c.Name, Description: c.Description, Status: c.Status, BillingModelSource: c.BillingModelSource, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, GroupIDs: c.GroupIDs, ModelPricing: c.ModelPricing, ProviderStatsPricingRules: c.ProviderStatsPricingRules}
}

func (c Configuration) Policy() routing.GroupRoutingPolicy {
	var allowed []string
	for _, price := range c.ModelPricing {
		allowed = append(allowed, price.Models...)
	}
	mapping := make(map[string]string)
	maps.Copy(mapping, c.ModelMapping)
	return routing.GroupRoutingPolicy{Enabled: c.Status == routing.StatusActive, ModelMapping: mapping, RestrictModels: c.RestrictModels, RestrictionModelSource: c.BillingModelSource, AllowedModels: allowed, Features: c.Features, FeaturesConfig: c.FeaturesConfig}
}

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
func (c *Configuration) IsActive() bool { return c != nil && c.Status == routing.StatusActive }
func (c *Configuration) GetModelPricing(model string) *routing.ModelPricingEntry {
	price := c.Price()
	return price.GetModelPricing(model)
}

func (c *Configuration) IsWebSearchEmulationEnabled(platform string) bool {
	if c == nil {
		return false
	}
	p := routing.GroupPolicyView{GroupRoutingPolicy: c.Policy()}
	return p.IsWebSearchEmulationEnabled(platform)
}

func (c *Configuration) IsBedrockCCCompatEnabled(platform string) bool {
	if c == nil {
		return false
	}
	p := routing.GroupPolicyView{GroupRoutingPolicy: c.Policy()}
	return p.IsBedrockCCCompatEnabled(platform)
}

func (r configurationRows) ListAll(ctx context.Context) ([]routing.PricingConfig, error) {
	rows, err := r.source.ListAll(ctx)
	out := make([]routing.PricingConfig, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Price())
	}
	return out, err
}

func (r configurationRows) GetGroupPlatforms(ctx context.Context, ids []int64) (map[int64]string, error) {
	return r.source.GetGroupPlatforms(ctx, ids)
}

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

// NewPricingConfigService adapts combined test inputs to independent pricing and policy readers.
func NewPricingConfigService(repo any, invalidator routing.GroupAuthInvalidator, options ...routing.PricingConfigOptions) *routing.PricingConfigService {
	if repo == nil {
		return routing.NewPricingConfigService(nil, invalidator, options...)
	}
	opts := routing.PricingConfigOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	if source, ok := repo.(interface {
		ListAll(context.Context) ([]Configuration, error)
		GetGroupPlatforms(context.Context, []int64) (map[int64]string, error)
	}); ok {
		adapter := configurationRows{source: source}
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
