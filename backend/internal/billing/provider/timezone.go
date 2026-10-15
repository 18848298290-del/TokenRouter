package provider

import (
	"fmt"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// timezone.go loads and caches named locations for pricing schedules.
// Pricing schedules require named timezones; blank names and the
// process-dependent Local name are rejected before loading. Accepted
// locations are cached for later calculations, and callers receive
// the shared loaded location rather than repeating timezone loading
// for each price calculation.

// pricingTimeLocations retains named location pointers across pricing
// loads, using sync.Map to publish and reuse the winning entry
// when callers load concurrently.
var pricingTimeLocations sync.Map

// LoadPricingLocation validates and loads the supplied timezone name,
// returning a shared cached location when available. Names are used
// as supplied after validation, so callers provide the actual loadable
// name and treat returned locations as immutable shared values.
// Invalid cached values are removed and loaded again; loading errors
// and an invalid value encountered during publication are returned.
func LoadPricingLocation(name string) (*time.Location, error) {
	if err := pricing.ValidateTimezoneName(name); err != nil {
		return nil, err
	}
	if cached, ok := pricingTimeLocations.Load(name); ok {
		location, valid := cached.(*time.Location)
		if valid && location != nil {
			return location, nil
		}
		// A cached value of the wrong type or nil cannot supply a pricing
		// timezone. Remove it so normal loading can replace the unusable
		// entry for subsequent calculations.
		pricingTimeLocations.Delete(name)
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, err
	}
	// Concurrent loaders can finish together. LoadOrStore selects one
	// shared entry, and the type check below validates that selected
	// value before it is returned to the pricing caller.
	actual, _ := pricingTimeLocations.LoadOrStore(name, location)
	actualLocation, ok := actual.(*time.Location)
	if !ok || actualLocation == nil {
		return nil, fmt.Errorf("invalid cached timezone %q", name)
	}
	return actualLocation, nil
}
