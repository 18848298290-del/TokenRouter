package testkit

import (
	"context"
	"slices"
	"sort"
	"time"

	billingadapter "github.com/TokenFlux/TokenRouter/internal/billing/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// ModelKey identifies a test model by group, platform, and name to distinguish same-named platform inputs.
type ModelKey struct {
	GroupID  int64
	Platform string // Platform identifier.
	Model    string // lowercase
}

// GroupPlatform keys wildcard test inputs by group and platform.
type GroupPlatform struct {
	GroupID  int64
	Platform string
}

// PricePattern is a wildcard pricing fixture.
type PricePattern struct {
	Prefix  string
	Pricing *routing.ModelPricingEntry
}

// ModelPattern is a wildcard mapping fixture.
type ModelPattern struct {
	Prefix string
	Target string
}

// ModelConfigData contains group-policy and pricing inputs for tests.
type ModelConfigData struct {
	GroupPolicies  map[int64]routing.GroupRoutingPolicy
	Configurations []Configuration
	UseRows        bool
	// Prices and model mappings supplied by the test.
	Prices        map[ModelKey]*routing.ModelPricingEntry // Exact prices by group, platform, and model.
	PricePatterns map[GroupPlatform][]*PricePattern       // Wildcard prices in first-match configuration order.
	Models        map[ModelKey]string                     // Exact mapping targets by group, platform, and model.
	ModelPatterns map[GroupPlatform][]*ModelPattern       // Wildcard mapping inputs by group and platform.
	ByGroup       map[int64]*Configuration                // Combined fixture configuration by group.
	Platforms     map[int64]string                        // Platform by group.

	// Catalogue metadata supplied by the test.
	ByID     map[int64]*Configuration
	LoadedAt time.Time
}

// NewModelConfigData initializes every test input map.
func NewModelConfigData() *ModelConfigData {
	return &ModelConfigData{
		Prices:        make(map[ModelKey]*routing.ModelPricingEntry),
		PricePatterns: make(map[GroupPlatform][]*PricePattern),
		Models:        make(map[ModelKey]string),
		ModelPatterns: make(map[GroupPlatform][]*ModelPattern),
		ByGroup:       make(map[int64]*Configuration),
		Platforms:     make(map[int64]string),
		ByID:          make(map[int64]*Configuration),
	}
}

// ModelConfigDataFromRows converts fixtures for compilation by the production routing service.
func ModelConfigDataFromRows(pricingConfigs []Configuration, platforms map[int64]string) *ModelConfigData {
	fixture := NewModelConfigData()
	fixture.Configurations = pricingConfigs
	fixture.UseRows = true
	fixture.Platforms = platforms
	return fixture
}

// ModelConfigFromData builds the production model configuration service from fixtures.
func ModelConfigFromData(fixture *ModelConfigData) *routing.PricingConfigService {
	if fixture.GroupPolicies == nil {
		fixture.GroupPolicies = make(map[int64]routing.GroupRoutingPolicy)
	}
	var pricingConfigs []Configuration
	if fixture.UseRows {
		pricingConfigs = fixture.Configurations
	} else {
		ids := make([]int64, 0, len(fixture.ByGroup))
		for gid := range fixture.ByGroup {
			ids = append(ids, gid)
		}
		slices.Sort(ids)
		for _, gid := range ids {
			ch := fixture.ByGroup[gid].Clone()
			ch.GroupIDs = []int64{gid}
			// Reconstruct group price cards from flattened test prices.
			keys := make([]ModelKey, 0, len(fixture.Prices))
			for key := range fixture.Prices {
				if key.GroupID == gid {
					keys = append(keys, key)
				}
			}
			sort.Slice(keys, func(i, j int) bool {
				if keys[i].Platform != keys[j].Platform {
					return keys[i].Platform < keys[j].Platform
				}
				return keys[i].Model < keys[j].Model
			})
			for _, key := range keys {
				price := fixture.Prices[key].Clone()
				price.Models = []string{key.Model}
				ch.ModelPricing = append(ch.ModelPricing, price)
			}
			for key, entries := range fixture.PricePatterns {
				if key.GroupID == gid {
					for _, entry := range entries {
						price := entry.Pricing.Clone()
						price.Models = []string{entry.Prefix + "*"}
						ch.ModelPricing = append(ch.ModelPricing, price)
					}
				}
			}
			policy, exists := fixture.GroupPolicies[gid]
			if !exists {
				policy = ch.Policy()
			}
			if len(fixture.Models) > 0 || len(fixture.ModelPatterns) > 0 {
				policy.Enabled = true
			}
			if policy.ModelMapping == nil {
				policy.ModelMapping = make(map[string]string)
			}
			for key, target := range fixture.Models {
				if key.GroupID == gid {
					policy.ModelMapping[key.Model] = target
				}
			}
			for key, entries := range fixture.ModelPatterns {
				if key.GroupID == gid {
					for _, entry := range entries {
						policy.ModelMapping[entry.Prefix+"*"] = entry.Target
					}
				}
			}
			fixture.GroupPolicies[gid] = policy
			pricingConfigs = append(pricingConfigs, *ch)
		}
	}
	return NewPricingConfigService(ConfigRows{Values: pricingConfigs, Platforms: fixture.Platforms}, nil, routing.PricingConfigOptions{Now: time.Now, LoadLocation: billingadapter.LoadPricingLocation, ReadGroup: func(_ context.Context, id int64) (*routing.Group, error) {
		policy, exists := fixture.GroupPolicies[id]
		if !exists {
			for _, row := range pricingConfigs {
				for _, groupID := range row.GroupIDs {
					if groupID == id {
						policy = row.Policy()
					}
				}
			}
		}
		return &routing.Group{ID: id, AllowedProtocols: capability.DefaultGroupClientProtocols(""), RoutingPolicy: policy}, nil
	}})
}
