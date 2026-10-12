package composite

import "github.com/TokenFlux/TokenRouter/internal/routing"

// RoutingAdminSettings extracts routing settings from the snapshot.
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

// ApplyRoutingAdminSettings copies routing settings into the snapshot.
func (s *Snapshot) ApplyRoutingAdminSettings(value routing.AdminSettings) {
	s.EnableModelFallback = value.EnableModelFallback
	s.FallbackModelAnthropic = value.FallbackModelAnthropic
	s.FallbackModelAntigravity = value.FallbackModelAntigravity
	s.FallbackModelGemini = value.FallbackModelGemini
	s.FallbackModelOpenAI = value.FallbackModelOpenAI
	s.MarketplaceAvailabilityBucketMinutes = value.MarketplaceAvailabilityBucketMinutes
	s.MarketplaceAvailabilityWindowDays = value.MarketplaceAvailabilityWindowDays
}

// ApplyRoutingAdminReadSettings copies model fallback and marketplace availability windows into the snapshot.
func (s *Snapshot) ApplyRoutingAdminReadSettings(value *routing.AdminReadSettings) {
	s.EnableModelFallback = value.EnableModelFallback
	s.FallbackModelAnthropic = value.FallbackModelAnthropic
	s.FallbackModelAntigravity = value.FallbackModelAntigravity
	s.FallbackModelGemini = value.FallbackModelGemini
	s.FallbackModelOpenAI = value.FallbackModelOpenAI
	s.MarketplaceAvailabilityBucketMinutes = value.MarketplaceAvailabilityBucketMinutes
	s.MarketplaceAvailabilityWindowDays = value.MarketplaceAvailabilityWindowDays
}
