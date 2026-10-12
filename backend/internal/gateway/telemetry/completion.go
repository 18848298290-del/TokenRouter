package telemetry

import (
	"log/slog"

	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

func ObserveCompletion(component, message string) {
	logging.LegacyPrintf(component, "%s", message)
}

// CompletionBillingEvent logs missing prices, free searches, and tier downgrades with event-specific fields and levels.
func CompletionBillingEvent(e completion.BillingEvent) {
	switch e.Kind {
	case "pricing_missing":
		logging.L().With(zap.String("component", e.Component), zap.Strings("billing_models", e.Models), zap.String("requested_model", e.RequestedModel), zap.String("mapped_model", e.MappedModel), zap.String("upstream_model", e.UpstreamModel), zap.Int64("api_key_id", e.KeyID), zap.Int64("provider_id", e.ProviderID)).Warn("openai_usage.pricing_missing_record_zero_cost", zap.Error(e.Err))
	case "standard_pricing_missing":
		logging.L().With(zap.String("component", e.Component), zap.String("request_id", e.RequestID)).Warn("openai_usage.standard_pricing_missing_free_fast_zero_cost", zap.Error(e.Err))
	case "search_free":
		logging.L().Info("openai_usage.search_price_per_1k_explicit_free", zap.Int("search_count", e.SearchCount), zap.String("model", e.Model), zap.Int64("api_key_id", e.KeyID), zap.Any("group_id", e.GroupID))
	case "tier_downgrade":
		slog.Info("billing.service_tier_downgraded", "component", e.Component, "request_id", e.RequestID, "requested_tier", e.RequestedTier, "response_tier", e.ObservedTier, "billed_tier", e.BilledTier, "platform", e.Platform, "provider_id", e.ProviderID)
	}
}
