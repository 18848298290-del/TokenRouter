package app

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/audit"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/creative"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/promptpolicy"
	"github.com/TokenFlux/TokenRouter/internal/gateway/ws"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/notification"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/server/runtimeconfig"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/settings/composite"
	settingshttp "github.com/TokenFlux/TokenRouter/internal/settings/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/site"
	"github.com/TokenFlux/TokenRouter/internal/team"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// provideCompositeSettingsHTTP binds domain services and settings coordination to the combined settings handler.
func provideCompositeSettingsHTTP(source *composite.Runtime, registry *settings.Registry, monitor *ops.OpsService, pay *payment.ConfigService, turnstile *identity.TurnstileService, aliyun *identity.AliyunCaptchaService, attributes *identity.UserAttributeService, totp *identity.TotpService, users *identity.UserService, creative *creative.Public) *settingshttp.Handler {
	return settingshttp.NewHandler(settingshttp.HandlerOptions{Settings: source, Participants: registry, Monitoring: monitor, Payment: pay, Turnstile: turnstile, Aliyun: aliyun, Attributes: attributes, Totp: totp, User: users, Creative: creative})
}

// provideCompositeReadOptions shares settings interpretation between background initialization and admin queries.
func provideCompositeReadOptions(cfg *config.Config, oauth *identity.OAuthSettings, gatewayRules *gateway.AdminSettingsRules, defaults *scheduler.AdminDefaults, sockets *ws.Runtime) *composite.ReadOptions {
	return &composite.ReadOptions{ResponsesWS: sockets, OAuth: oauth, Gateway: *gatewayRules, Scheduler: *defaults, DefaultBalance: func() float64 { return cfg.Default.UserBalance }, DefaultConcurrency: func() int { return cfg.Default.UserConcurrency }, Forwarded: func() runtimeconfig.ForwardedInput {
		value := cfg.ForwardedClientIPSettings()
		return runtimeconfig.ForwardedInput{APIKeyACLTrustForwardedIP: value.TrustForwardedIP, ForwardedClientIPHeaders: value.Headers}
	}, PublishModel: func(model string) {
		grok.SetRuntimeDefaultTextModel(model)
	}}
}

// provideCompositeRuntime binds configuration, settings services, and the publication order after commit.
func provideCompositeRuntime(store *settings.Store, cfg *config.Config, read *composite.ReadOptions, grants *identity.GrantSettings, gatewayRuntime *gateway.RuntimeSettings, gatewayRules *gateway.AdminSettingsRules, defaults *scheduler.AdminDefaults, plans *billing.Plans, backendMode *admission.BackendMode, providerRuntime *provider.RuntimeSettings, quota *provider.QuotaSettingsCache, forwarded *runtimeconfig.ForwardedSettings, shared *schedulerSharedState, worker *creative.CreativeWorkerRuntime, monitor *ops.OpsService) *composite.Runtime {
	prepare := composite.PrepareOptions{ReadValues: store.GetAll, Gateway: *gatewayRules, Scheduler: *defaults, ValidatePlans: func(ctx context.Context, value []billing.DefaultSubscriptionSetting) error {
		return billing.ValidateDefaultSubscriptionPlans(ctx, value, plans.GetPlan)
	}}
	steps := []composite.Application{
		{Module: "responses-ws", Apply: func(_ context.Context, s *composite.Snapshot) error {
			if read.ResponsesWS != nil {
				return read.ResponsesWS.Publish(string(s.ResponsesWS))
			}
			return nil
		}},
		{Module: "gateway", Apply: func(_ context.Context, s *composite.Snapshot) error {
			backendMode.Publish(s.BackendModeEnabled)
			gatewayRuntime.PublishForwarding(s.MinClaudeCodeVersion, s.MaxClaudeCodeVersion, gateway.ForwardingSnapshot{OpenAITTFTMode: gateway.NormalizeOpenAITTFTMode(s.OpenAITTFTMode), FingerprintUnification: s.EnableFingerprintUnification, MetadataPassthrough: s.EnableMetadataPassthrough, CCHSigning: s.EnableCCHSigning, ClaudeOAuthSystemPromptInjection: s.EnableClaudeOAuthSystemPromptInjection, ClaudeOAuthSystemPrompt: s.ClaudeOAuthSystemPrompt, ClaudeOAuthSystemPromptBlocks: s.ClaudeOAuthSystemPromptBlocks, AnthropicCacheTTL1hInjection: s.EnableAnthropicCacheTTL1hInjection, RewriteMessageCacheControl: s.RewriteMessageCacheControl, ClientDatelineNormalization: s.EnableClientDatelineNormalization})
			gatewayRuntime.PublishClientUserAgents(s.AntigravityUserAgentVersion, s.OpenAICodexUserAgent)
			promptpolicy.SharedCache().Store((*promptpolicy.CompiledConfig)(nil))
			return nil
		}},
		{Module: "scheduler", Apply: func(_ context.Context, s *composite.Snapshot) error {
			shared.Settings.Store(scheduler.RuntimeSettingsFromAdmin(s.SchedulerAdminSettings(), *defaults))
			return nil
		}},
		{Module: "provider", Apply: func(_ context.Context, s *composite.Snapshot) error {
			quota.Apply(s.OpenAIQuotaAutoPauseSettings, s.OpenAIQuotaAutoPauseSettingsSet)
			providerRuntime.ApplySchedulingThresholds(s.ProviderSchedulingThresholds)
			return nil
		}},
		{Module: "server", Apply: func(_ context.Context, s *composite.Snapshot) error {
			forwarded.Apply(runtimeconfig.ForwardedInput{APIKeyACLTrustForwardedIP: s.APIKeyACLTrustForwardedIP, ForwardedClientIPHeaders: s.ForwardedClientIPHeaders})
			return nil
		}},
		{Module: "gateway-client", Apply: func(_ context.Context, s *composite.Snapshot) error {
			gatewayRuntime.PublishCodexPlugin(s.OpenAIAllowClaudeCodeCodexPlugin)
			return nil
		}},
		{Module: "site", Apply: func(_ context.Context, _ *composite.Snapshot) error { store.NotifyUpdated(); return nil }},
		{Module: "creative", Apply: func(_ context.Context, s *composite.Snapshot) error {
			worker.SetWorkerCount(s.CreativeWorkerCount)
			return nil
		}},
		{Module: "ops", Apply: func(_ context.Context, s *composite.Snapshot) error {
			monitor.SetMonitoringEnabled(s.OpsMonitoringEnabled)
			return nil
		}},
	}
	return composite.NewRuntime(store, *read, prepare, grants, gatewayRuntime, cfg.Totp.EncryptionKeyConfigured, steps)
}

// provideSettingsParticipants registers settings preparers; construction failure aborts application startup.
func provideSettingsParticipants(runtime *payment.Runtime, grants *identity.GrantSettings, schedulerDefaults *scheduler.AdminDefaults, gatewayRules *gateway.AdminSettingsRules, sockets *ws.Runtime) (*settings.Registry, error) {
	return settings.NewRegistry(staticSettingsParticipants(runtime, grants, schedulerDefaults, gatewayRules, sockets.Defaults())...)
}

// staticSettingsParticipants maps fields to owning modules and storage keys for assembly validation.
func staticSettingsParticipants(runtime *payment.Runtime, grants *identity.GrantSettings, schedulerDefaults *scheduler.AdminDefaults, gatewayRules *gateway.AdminSettingsRules, socketDefaults ...ws.Parameters) []settings.Participant {
	defaults := ws.DefaultParameters()
	if len(socketDefaults) > 0 {
		defaults = socketDefaults[0]
	}
	return []settings.Participant{ws.SettingsParticipant(defaults), settings.LocalizationParticipant(), identity.SettingsParticipant(grants), team.SettingsParticipant(), moderation.SettingsParticipant(), creative.SettingsParticipant(), runtimeconfig.SettingsParticipant(), billing.SettingsParticipant(), scheduler.SettingsParticipant(*schedulerDefaults), routing.SettingsParticipant(), site.SettingsParticipant(), provider.SettingsParticipant(), ops.SettingsParticipant(), payment.VisibleSettingsParticipant(), notification.SMTPSettingsParticipant(), promotion.SettingsParticipant(), usage.SettingsParticipant(), audit.SettingsParticipant(), gateway.FastSettingsParticipant(), gateway.AdminSettingsParticipant(*gatewayRules), payment.SettingsParticipant(runtime.RefreshProvidersChecked)}
}
