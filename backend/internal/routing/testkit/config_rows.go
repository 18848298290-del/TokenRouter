package testkit

import (
	"context"
	"time"

	pricingprovider "github.com/TokenFlux/TokenRouter/internal/billing/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// ConfigRows supplies replaceable test data; the service owns query caching.
type ConfigRows struct {
	routing.PricingConfigRepository
	Values    []Configuration
	Platforms map[int64]string
}

func (r ConfigRows) ListAll(context.Context) ([]Configuration, error) { return r.Values, nil }
func (r ConfigRows) GetGroupPlatforms(context.Context, []int64) (map[int64]string, error) {
	return r.Platforms, nil
}

func (r ConfigRows) GetByID(_ context.Context, id int64) (*Configuration, error) {
	for i := range r.Values {
		if r.Values[i].ID == id {
			return r.Values[i].Clone(), nil
		}
	}
	return nil, routing.ErrPricingConfigNotFound
}

// PricingConfig clones a configuration, binds the supplied group, and injects the clock and timezone loader.
func PricingConfig(groupID int64, platform string, value Configuration) *routing.PricingConfigService {
	cloned := value.Clone()
	cloned.GroupIDs = []int64{groupID}
	return NewPricingConfigService(ConfigRows{Values: []Configuration{*cloned}, Platforms: map[int64]string{groupID: platform}}, nil, routing.PricingConfigOptions{Now: time.Now, LoadLocation: pricingprovider.LoadPricingLocation})
}
