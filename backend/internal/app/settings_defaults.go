package app

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identityprovider "github.com/TokenFlux/TokenRouter/internal/identity/provider"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
	"github.com/TokenFlux/TokenRouter/internal/server/runtimeconfig"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// provideOAuthSettings supplies startup authentication defaults to the OAuth settings reader.
func provideOAuthSettings(store *settings.Store, cfg *config.Config) *identity.OAuthSettings {
	var defaults *identity.OAuthSettingsDefaults
	if cfg != nil {
		defaults = &identity.OAuthSettingsDefaults{LinuxDo: cfg.LinuxDo, DingTalk: cfg.DingTalk, OIDC: cfg.OIDC, WeChat: cfg.WeChat, GitHubOAuth: cfg.GitHubOAuth, GoogleOAuth: cfg.GoogleOAuth}
	}
	return identity.NewOAuthSettings(store, defaults, identityprovider.ResolveSettingsOIDCMetadata)
}

// provideGrantSettings binds billing plan lookup for signup grants.
func provideGrantSettings(store *settings.Store, cfg *config.Config, plans *billing.Plans) *identity.GrantSettings {
	options := identity.GrantSettingsOptions{ValidatePlans: func(ctx context.Context, items []identity.DefaultSubscriptionSetting) error {
		return identity.ValidateDefaultSubscriptionPlans(ctx, items, plans.GetPlan)
	}}
	if cfg != nil {
		options.DefaultBalance = cfg.Default.UserBalance
		options.DefaultConcurrency = cfg.Default.UserConcurrency
	}
	return identity.NewGrantSettings(store, options)
}

// provideForwardedSettings binds startup proxy trust and shared client-IP runtime settings.
func provideForwardedSettings(store *settings.Store, cfg *config.Config) *runtimeconfig.ForwardedSettings {
	return runtimeconfig.NewForwardedSettings(store, runtimeconfig.ForwardedSettingsOptions{InitialTrust: cfg.Security.TrustForwardedIPForAPIKeyACL, TrustedProxiesConfigured: cfg.Server.TrustedProxiesConfigured, Headers: func() []string { return cfg.ForwardedClientIPSettings().Headers }, Publish: cfg.SetForwardedClientIPSettings})
}

// provideSchedulerAdminDefaults extracts process defaults and normalizes sticky-escape settings.
func provideSchedulerAdminDefaults(cfg *config.Config) *scheduler.AdminDefaults {
	value := scheduler.DefaultAdminSettingsDefaults()
	if cfg != nil {
		source := cfg.Gateway.AdvancedScheduler
		value.TopK = source.LBTopK
		value.Weights = source.ScoreWeights
		value.Process.EwmaErrorRateAlpha = source.EWMAErrorRateAlpha
		value.Process.EwmaTTFTAlpha = source.EWMATTFTAlpha
		value.Process.StickyEscape = policy.NormalizeStickyEscape(policy.StickyEscapeConfig{Enabled: source.StickyEscapeEnabled, TtftMs: float64(source.StickyEscapeTTFTMs), ErrorRate: source.StickyEscapeErrorRate})
	}
	return &value
}

// provideGatewayAdminRules returns platform settings rules.
func provideGatewayAdminRules() *gateway.AdminSettingsRules {
	return &gateway.AdminSettingsRules{GrokDefaultTextModel: grok.DefaultTextModel, NormalizeUserAgentVersion: antigravity.NormalizeUserAgentVersion, ValidateClaudePromptBlocks: anthropic.ValidateClaudeOAuthSystemPromptBlocksConfig}
}

// provideGatewaySettings creates gateway runtime settings with platform defaults.
func provideGatewaySettings(store *settings.Store) *gateway.RuntimeSettings {
	return gateway.NewRuntimeSettings(store, settings.ErrSettingNotFound, func() *gateway.BetaPolicySettings {
		return gatewayprovider.GatewayBetaPolicy(anthropic.DefaultBetaPolicySettings())
	}, gateway.ClientSettingsOptions{NormalizeUserAgentVersion: antigravity.NormalizeUserAgentVersion, DefaultUserAgentVersion: antigravity.GetDefaultUserAgentVersion})
}

// provideQuotaSettings uses the Ops parser to read provider quotas from shared settings JSON.
func provideQuotaSettings(store *settings.Store) *provider.QuotaSettingsCache {
	return provider.NewQuotaSettingsCache(store, settings.ErrSettingNotFound, ops.ParseRuntimeQuotaAutoPauseSettings)
}
