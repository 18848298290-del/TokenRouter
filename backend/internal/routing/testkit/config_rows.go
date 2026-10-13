package testkit

import (
	"context"
	"time"

	pricingprovider "github.com/TokenFlux/TokenRouter/internal/billing/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Pricing fixtures supply combined configuration rows while the production
// service builds its own cache. List reads expose the fixture collections
// directly, ID reads copy selected pricing and policy values, and a
// single-group helper prepares both inputs with the production clock and
// timezone loader.

// ConfigRows provides combined pricing fixtures and group-platform metadata for
// test services.
type ConfigRows struct {
	// PricingConfigRepository supplies methods this fixture does not implement.
	// Tests invoking those promoted methods configure the embedded repository; its
	// zero value has no target.
	routing.PricingConfigRepository
	// Values and Platforms are returned directly by list and platform reads. Tests
	// keep their slices, nested values, and maps stable while service reads or
	// callbacks use them.
	Values    []Configuration  // combined pricing fixture rows
	Platforms map[int64]string // platform labels by group
}

// ListAll returns the configured fixture slice directly with no error. The
// slice and its nested values remain shared with Values, so tests coordinate
// any replacement or mutation with service reads.
func (r ConfigRows) ListAll(context.Context) ([]Configuration, error) { return r.Values, nil }

// GetGroupPlatforms returns the complete fixture platform map, ignoring the
// requested subset and context. The returned map remains shared with Platforms,
// so callers copy it before modifying its entries.
func (r ConfigRows) GetGroupPlatforms(context.Context, []int64) (map[int64]string, error) {
	return r.Platforms, nil
}

// GetByID finds the first fixture row with id and returns Configuration.Clone.
// Missing rows return ErrPricingConfigNotFound; pricing and policy collections
// follow Clone's copying rules, including its shared BillingSettings pointer.
func (r ConfigRows) GetByID(_ context.Context, id int64) (*Configuration, error) {
	for i := range r.Values {
		if r.Values[i].ID == id {
			return r.Values[i].Clone(), nil
		}
	}
	return nil, routing.ErrPricingConfigNotFound
}

// PricingConfig prepares production price and policy reads from one cloned
// fixture bound to groupID, retaining platform as fixture metadata. It installs
// the current clock and pricing timezone loader; the cloned fixture retains its
// input BillingSettings pointer.
func PricingConfig(groupID int64, platform string, value Configuration) *routing.PricingConfigService {
	cloned := value.Clone()
	cloned.GroupIDs = []int64{groupID}
	return NewPricingConfigService(ConfigRows{Values: []Configuration{*cloned}, Platforms: map[int64]string{groupID: platform}}, nil, routing.PricingConfigOptions{Now: time.Now, LoadLocation: pricingprovider.LoadPricingLocation})
}
