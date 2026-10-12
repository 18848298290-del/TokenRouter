package provider

import (
	"fmt"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

var pricingTimeLocations sync.Map

// LoadPricingLocation loads and caches a timezone for callers to pass into pricing calculations.
func LoadPricingLocation(name string) (*time.Location, error) {
	if err := pricing.ValidateTimezoneName(name); err != nil {
		return nil, err
	}
	if cached, ok := pricingTimeLocations.Load(name); ok {
		location, valid := cached.(*time.Location)
		if valid && location != nil {
			return location, nil
		}
		pricingTimeLocations.Delete(name)
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, err
	}
	actual, _ := pricingTimeLocations.LoadOrStore(name, location)
	actualLocation, ok := actual.(*time.Location)
	if !ok || actualLocation == nil {
		return nil, fmt.Errorf("invalid cached timezone %q", name)
	}
	return actualLocation, nil
}
