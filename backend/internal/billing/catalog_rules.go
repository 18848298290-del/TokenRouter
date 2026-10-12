package billing

import (
	"log/slog"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// Operation rates distinguish an absent amount from an explicitly configured
// zero. Catalog defaults supply an unset search or audio price, while a
// missing default remains unavailable and is logged by selection. Catalog time
// schedules separately scale model prices using the calculator's location
// loader.

// applyCatalogTimePricing scales available model amounts using the catalog's
// daily schedule at at. Nil prices or absent time rules retain the input
// pointer. The loader's returned location governs matching even alongside an
// error; a nil location selects the unit factor.
func (s *Calculator) applyCatalogTimePricing(price *ModelPricing, at time.Time) *ModelPricing {
	if price == nil || price.TimePricing == nil {
		return price
	}
	// Use the companion location even when the loader reports an error. A nil
	// location selects the unit factor, retaining unscheduled catalog amounts.
	location, _ := s.options.LoadLocation(price.TimePricing.Timezone)
	return pricing.MultiplyModelPricing(price, price.TimePricing.MultiplierAt(at, location))
}

// operationPrices returns catalog operation defaults when the catalog exposes
// BillingDefaults. Catalogs lacking that optional capability return empty
// prices, leaving amount pointers absent for callers to diagnose unavailable
// operation pricing.
func (s *Calculator) operationPrices() pricing.OperationPrices {
	if source, ok := s.catalog.(interface {
		// BillingDefaults returns optional operation amounts from this catalog
		// snapshot. Nil amount pointers represent unavailable defaults; a present
		// zero records a known free operation.
		BillingDefaults() pricing.OperationPrices
	}); ok {
		return source.BillingDefaults()
	}
	// An optional catalog without operation defaults leaves those prices absent,
	// so callers can distinguish missing operation rates from configured free
	// amounts.
	return pricing.OperationPrices{}
}

// operationPrice selects a supplied configured amount before the catalog
// amount, retaining a configured zero as free. An absent catalog amount is
// logged and returned as nil; the caller decides how to represent or reject
// that missing operation price.
func operationPrice(operation string, configured, catalog *float64) *float64 {
	if configured != nil {
		return configured
	}
	// No configured amount was supplied, so the catalog amount controls
	// availability; a nil default is reported before the caller handles that
	// missing price.
	if catalog == nil {
		slog.Warn("operation pricing unavailable", "operation", operation)
	}
	return catalog
}

// audioPrices copies the configured audio record and fills the selected mode's
// absent amount from catalog defaults. Mode matching ignores case but retains
// whitespace; other fields and supplied amount pointers remain shared, and
// unknown modes leave the copied record unchanged.
func (s *Calculator) audioPrices(mode string, configured *audioPriceConfig) *audioPriceConfig {
	var result audioPriceConfig
	if configured != nil {
		result = *configured
	}
	defaults := s.operationPrices()
	switch strings.ToLower(mode) {
	case "realtime":
		result.RealtimePerMin = operationPrice(mode, result.RealtimePerMin, defaults.AudioRealtimePricePerMin)
	case "tts":
		result.TTSPerMChars = operationPrice(mode, result.TTSPerMChars, defaults.AudioTTSPricePerMillionChars)
	case "stt":
		result.STTPerHour = operationPrice(mode, result.STTPerHour, defaults.AudioSTTPricePerHour)
	}
	return &result
}
