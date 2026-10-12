package app

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

// gatewayBillingRates holds separate completion rate caches shared with application cleanup tasks.
type gatewayBillingRates struct {
	Forward *billing.GroupRateResolver
	OpenAI  *billing.GroupRateResolver
}

func provideGatewayBillingRates(repo billing.UserGroupRateRepository, cfg *config.Config) *gatewayBillingRates {
	ttl := gatewayGroupRateCacheTTL(cfg)
	return &gatewayBillingRates{
		Forward: billing.NewGroupRateResolver(repo, nil, ttl, nil, "service.gateway", logging.LegacyPrintf),
		OpenAI:  billing.NewGroupRateResolver(repo, nil, ttl, nil, "service.openai_gateway", logging.LegacyPrintf),
	}
}

// gatewayGroupRateCacheTTL returns a positive configured TTL, or the billing default otherwise.
func gatewayGroupRateCacheTTL(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.Gateway.UserGroupRateCacheTTLSeconds <= 0 {
		return billing.DefaultGroupRateCacheTTL
	}
	return time.Duration(cfg.Gateway.UserGroupRateCacheTTLSeconds) * time.Second
}

// Expire removes expired entries from both completion caches when called by the application timing wheel.
func (r *gatewayBillingRates) Expire() {
	if r != nil {
		r.Forward.DeleteExpired()
		r.OpenAI.DeleteExpired()
	}
}
