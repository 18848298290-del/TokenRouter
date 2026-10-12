package testkit

import (
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// groupSource adapts the routing repository for authentication fixtures.
type groupSource struct{ routing.GroupRepository }

// NewService constructs an authentication service for cross-module tests; callers start its lifecycle.
func NewService(keys apikey.APIKeyRepository, users identity.UserRepository, groups routing.GroupRepository, subs billing.UserSubscriptionRepository, rates billing.UserGroupRateRepository, cache apikey.APIKeyCache, cfg *config.Config) *apikey.APIKeyService {
	var groupReader apikey.GroupRepository
	if groups != nil {
		groupReader = groupSource{groups}
	}
	var options *apikey.Options
	if cfg != nil {
		options = &apikey.Options{APIKeyAuth: apikey.APIKeyAuthCacheConfig{
			L1Size: cfg.APIKeyAuth.L1Size, L1TTLSeconds: cfg.APIKeyAuth.L1TTLSeconds,
			L2TTLSeconds: cfg.APIKeyAuth.L2TTLSeconds, NegativeTTLSeconds: cfg.APIKeyAuth.NegativeTTLSeconds,
			JitterPercent: cfg.APIKeyAuth.JitterPercent, Singleflight: cfg.APIKeyAuth.Singleflight,
			LookupConcurrency: cfg.APIKeyAuth.LookupConcurrency,
			InvalidAbuse:      apikey.InvalidAuthAbuseConfig(cfg.APIKeyAuth.InvalidAbuse),
		}}
		options.Default.APIKeyPrefix = cfg.Default.APIKeyPrefix
		options.Team.Enabled = cfg.Team.Enabled
	}
	core := apikey.NewAPIKeyService(keys, users, groupReader, subs, rates, cache, options)
	core.SetGroupFastPolicy(func(raw string, force bool) string {
		return (&routing.Group{OpenAIFastPolicy: raw, ForceOpenAIFast: force}).EffectiveOpenAIFastPolicy()
	})
	return core
}
