package billing

import (
	"log/slog"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// applyCatalogTimePricing applies the catalog rate's time-of-day multiplier.
func (s *Calculator) applyCatalogTimePricing(price *ModelPricing, at time.Time) *ModelPricing {
	if price == nil || price.TimePricing == nil {
		return price
	}
	location, _ := s.options.LoadLocation(price.TimePricing.Timezone)
	return pricing.MultiplyModelPricing(price, price.TimePricing.MultiplierAt(at, location))
}

func (s *Calculator) operationPrices() pricing.OperationPrices {
	if source, ok := s.catalog.(interface {
		BillingDefaults() pricing.OperationPrices
	}); ok {
		return source.BillingDefaults()
	}
	return pricing.OperationPrices{}
}

// operationPrice prefers configured prices, including zero, and logs missing catalog prices before returning nil.
func operationPrice(operation string, configured, catalog *float64) *float64 {
	if configured != nil {
		return configured
	}
	if catalog == nil {
		slog.Warn("operation pricing unavailable", "operation", operation)
	}
	return catalog
}

// audioPrices fills an operation's unset administrator rate from catalog defaults.
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
