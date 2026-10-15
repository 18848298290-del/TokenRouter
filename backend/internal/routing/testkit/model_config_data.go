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

// model_config_data.go converts flattened model fixtures into pricing
// configurations and group policies.
// Flattened model fixtures key prices and mappings by group, platform, and
// model, while production pricing and group policy use group-wide model rules.
// Building the service combines those fixture inputs into configuration rows,
// assigning the group ID and placing flattened exact and wildcard prices after
// each row's existing prices.
//
// Row mode uses supplied configurations directly. Flattened mode starts from
// ByGroup, sorts group IDs and exact-price keys, copies pricing entries, and
// writes derived policy back into GroupPolicies. Platform keys organize
// fixtures but are removed during this conversion; tests supply unambiguous
// group-wide rules when several platform keys contribute. The resulting group
// reader retains the fixture policy map, while production cache construction
// copies price configurations before indexing them.

// ModelKey identifies an exact fixture input by group, platform, and model
// name.
type ModelKey struct {
	GroupID  int64  // fixture owning group identity
	Platform string // fixture source platform identifier
	Model    string // fixture exact model name
}

// GroupPlatform identifies the group and platform supplying wildcard fixture
// inputs.
type GroupPlatform struct {
	GroupID  int64  // fixture owning group identity
	Platform string // fixture source platform identifier
}

// PricePattern pairs a wildcard model prefix with a fixture price entry.
type PricePattern struct {
	Prefix string // model prefix before wildcard
	// Pricing points to nonnil test-owned entry data. ModelConfigFromData clones that
	// entry before replacing its model names, so fixture values remain reusable.
	Pricing *routing.ModelPricingEntry
}

// ModelPattern pairs a wildcard request-model prefix with its fixture mapping
// target.
type ModelPattern struct {
	Prefix string // request prefix before wildcard
	Target string // configured mapped model name
}

// ModelConfigData holds row or flattened inputs for production pricing-service
// tests.
type ModelConfigData struct {
	// GroupPolicies receives generated policies and remains captured by the group
	// reader. Flattened construction also edits any existing nonnil ModelMapping
	// map directly; tests coordinate mutations of those shared maps with reads.
	GroupPolicies map[int64]routing.GroupRoutingPolicy
	// Configurations supplies row mode by reference. UseRows selects these rows
	// instead of rebuilding from ByGroup, while the production pricing cache later
	// copies each configuration.
	Configurations []Configuration // complete row-mode pricing fixtures
	UseRows        bool            // supplied configuration row selector
	// Prices and PricePatterns add cloned price entries after each ByGroup row's
	// existing prices. Models and ModelPatterns add group mappings, dropping their
	// fixture platform dimension during construction.
	// Prices holds nonnil entries for selected ByGroup IDs; construction clones
	// each entry before replacing its model names.
	Prices map[ModelKey]*routing.ModelPricingEntry // exact fixture price entries
	// PricePatterns retains order within each slice, whose patterns and Pricing
	// pointers are nonnil. Iteration across group-platform keys is unspecified,
	// so tests use unambiguous group-wide prices when several keys contribute.
	PricePatterns map[GroupPlatform][]*PricePattern // wildcard fixture price entries
	// Models drops platform keys when assigning group-wide mappings. Repeated
	// group/model keys from different platforms overwrite values in unspecified
	// map traversal order.
	Models map[ModelKey]string // exact fixture mapping targets
	// ModelPatterns contains nonnil entries contributing prefix-plus-wildcard
	// mapping keys. Entries sharing a group-wide key overwrite earlier values,
	// with map-key iteration order unspecified across platform inputs.
	ModelPatterns map[GroupPlatform][]*ModelPattern // wildcard fixture mapping entries
	// ByGroup selects the groups reconstructed in flattened mode. Every selected
	// value must be nonnil so its configuration can be cloned before adding prices
	// and policy.
	ByGroup map[int64]*Configuration
	// Platforms remains shared with ConfigRows for direct fixture metadata reads.
	// Production price matching uses group and model keys; tests coordinate map
	// mutations with any direct platform read.
	Platforms map[int64]string

	// ByID and LoadedAt retain test-supplied metadata for fixture setup.
	// ModelConfigFromData does not use them to populate the production service or
	// choose its cache age.
	ByID     map[int64]*Configuration // fixture metadata by configuration identity
	LoadedAt time.Time                // fixture metadata load timestamp
}

// NewModelConfigData allocates maps for flattened fixture prices, mappings,
// group configurations, platforms, and metadata. GroupPolicies starts nil and
// is allocated when ModelConfigFromData builds the service; row mode remains
// disabled.
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

// ModelConfigDataFromRows retains supplied configuration rows and platform
// metadata for row-mode service construction. Rows and platform metadata remain
// shared with the fixture and service adapter, so tests keep them stable during
// construction and subsequent reads.
func ModelConfigDataFromRows(pricingConfigs []Configuration, platforms map[int64]string) *ModelConfigData {
	fixture := NewModelConfigData()
	fixture.Configurations = pricingConfigs
	fixture.UseRows = true
	fixture.Platforms = platforms
	return fixture
}

// ModelConfigFromData builds the production pricing service from a nonnil
// fixture and captures its group policies for subsequent reads. Row mode uses
// Configurations; flattened mode clones each nonnil ByGroup row, appends
// flattened prices, and adds group-wide mappings. The function allocates or
// updates GroupPolicies in place.
//
// Flattened group IDs and exact-price keys are sorted for stable row
// construction. Wildcard entries retain each source slice's order, while
// traversal across platform keys is unspecified; tests avoid ambiguous
// overlapping sources after platform keys are dropped.
//
// Existing GroupPolicies entries supply the starting policy; incoming mappings
// overwrite matching keys. Any nonempty mapping collection enables every
// generated flattened policy, even groups with no added mappings. Callers
// supply nonnil price and pattern entries and keep captured fixture data stable
// while the service reads it.
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
			// Sort exact keys before dropping their platform dimension, making appended
			// price order stable while keeping existing row prices ahead of these
			// fixture additions.
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
			// An existing policy retains its ModelMapping map; additions below therefore
			// update the fixture's nested map as well as this copied policy value.
			policy, exists := fixture.GroupPolicies[gid]
			// Derive policy from the combined row when the fixture has no group
			// override, keeping supplied policies as the starting point for added
			// mappings.
			if !exists {
				policy = ch.Policy()
			}
			// Enable generated policies when mapping collections are present so service
			// tests exercise supplied mapping rules even when the base row was disabled.
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
		// When no captured policy exists, derive it from associated rows; the scan
		// keeps the last matching row's policy for this group ID.
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
