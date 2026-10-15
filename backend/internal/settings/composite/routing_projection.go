package composite

import "github.com/TokenFlux/TokenRouter/internal/routing"

// routing_projection.go transfers routing fallback models and marketplace
// windows through the combined snapshot.
// Routing settings determine fallback model choices and the marketplace
// observation window shown in administrator settings. Their value struct copies
// these scalar fields between the combined Snapshot and the routing normalizer,
// so the saved batch and later readback use the owning module's selected values.

// RoutingAdminSettings returns fallback choices and marketplace window controls
// for routing preparation.
func (s *Snapshot) RoutingAdminSettings() routing.AdminSettings {
	return routing.AdminSettings{
		EnableModelFallback:                  s.EnableModelFallback,
		FallbackModelAnthropic:               s.FallbackModelAnthropic,
		FallbackModelAntigravity:             s.FallbackModelAntigravity,
		FallbackModelGemini:                  s.FallbackModelGemini,
		FallbackModelOpenAI:                  s.FallbackModelOpenAI,
		MarketplaceAvailabilityBucketMinutes: s.MarketplaceAvailabilityBucketMinutes,
		MarketplaceAvailabilityWindowDays:    s.MarketplaceAvailabilityWindowDays,
	}
}

// ApplyRoutingAdminSettings assigns normalized fallback models and marketplace
// intervals to the Snapshot.
func (s *Snapshot) ApplyRoutingAdminSettings(value routing.AdminSettings) {
	s.EnableModelFallback = value.EnableModelFallback
	s.FallbackModelAnthropic = value.FallbackModelAnthropic
	s.FallbackModelAntigravity = value.FallbackModelAntigravity
	s.FallbackModelGemini = value.FallbackModelGemini
	s.FallbackModelOpenAI = value.FallbackModelOpenAI
	s.MarketplaceAvailabilityBucketMinutes = value.MarketplaceAvailabilityBucketMinutes
	s.MarketplaceAvailabilityWindowDays = value.MarketplaceAvailabilityWindowDays
}

// ApplyRoutingAdminReadSettings assigns the routing reader's fallback models and
// marketplace observation settings.
func (s *Snapshot) ApplyRoutingAdminReadSettings(value *routing.AdminReadSettings) {
	s.EnableModelFallback = value.EnableModelFallback
	s.FallbackModelAnthropic = value.FallbackModelAnthropic
	s.FallbackModelAntigravity = value.FallbackModelAntigravity
	s.FallbackModelGemini = value.FallbackModelGemini
	s.FallbackModelOpenAI = value.FallbackModelOpenAI
	s.MarketplaceAvailabilityBucketMinutes = value.MarketplaceAvailabilityBucketMinutes
	s.MarketplaceAvailabilityWindowDays = value.MarketplaceAvailabilityWindowDays
}
