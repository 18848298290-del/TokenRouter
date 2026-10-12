package app

import (
	"context"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/notification"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/site"
)

// identityAuthSettings combines identity settings readers from the owning modules.
type identityAuthSettings struct {
	*identity.RuntimeSettings
	*identity.GrantSettings
	*site.DisplaySettings
	oauth     *identity.OAuthSettings
	promotion *promotion.RuntimeSettings
}

// identityAuthGraph holds authentication use cases and transaction adapters shared by authentication endpoints.
type identityAuthGraph struct {
	Core   *identity.AuthService
	State  *identitypostgres.AuthState
	Client *dbent.Client
}

func provideIdentityAuthSettings(runtime *identity.RuntimeSettings, grants *identity.GrantSettings, display *site.DisplaySettings, oauth *identity.OAuthSettings, promotion *promotion.RuntimeSettings) *identityAuthSettings {
	return &identityAuthSettings{RuntimeSettings: runtime, GrantSettings: grants, DisplaySettings: display, oauth: oauth, promotion: promotion}
}

func (s identityAuthSettings) IsPromoCodeEnabled(ctx context.Context) bool {
	return s.promotion.IsPromoCodeEnabled(ctx)
}

func (s identityAuthSettings) IsInvitationCodeEnabled(ctx context.Context) bool {
	return s.promotion.IsInvitationCodeEnabled(ctx)
}

func (s identityAuthSettings) IsAffiliateAdminRechargeEnabled(ctx context.Context) bool {
	return s.promotion.IsAffiliateAdminRechargeEnabled(ctx)
}

func (s identityAuthSettings) GetDingTalkConnectOAuthConfig(ctx context.Context) (identity.DingTalkRegistrationPolicy, error) {
	value, err := s.oauth.GetDingTalkConnectOAuthConfig(ctx)
	return identity.DingTalkRegistrationPolicy{Enabled: value.Enabled, BypassRegistration: value.BypassRegistration, CorpRestrictionPolicy: value.CorpRestrictionPolicy}, err
}

func provideIdentityAuthGraph(
	entClient *dbent.Client,
	users *identitypostgres.UserStore,
	redeemRepo billing.RedeemCodeRepository,
	refreshTokenCache identity.RefreshTokenCache,
	cfg *config.Config,
	settingService *identityAuthSettings,
	emailService *identity.EmailChallenges,
	turnstileService *identity.TurnstileService,
	tencentCaptchaService *identity.TencentCaptchaService,
	aliyunCaptchaService *identity.AliyunCaptchaService,
	emailQueueService *notification.EmailQueueService,
	promoService *promotion.PromoService,
	defaultSubAssigner identity.DefaultSubscriptionAssigner,
	affiliateService *promotion.AffiliateService,
	authCacheInvalidator apikey.APIKeyAuthCacheInvalidator,
	billingCache billing.BillingCache,
) *identityAuthGraph {
	options := &identity.AuthOptions{}
	if cfg != nil {
		options.Default.UserBalance = cfg.Default.UserBalance
		options.Default.UserConcurrency = cfg.Default.UserConcurrency
		options.JWT = identity.SessionOptions{Now: time.Now, Secret: cfg.JWT.Secret, ExpireHour: cfg.JWT.ExpireHour, AccessTokenExpireMinutes: cfg.JWT.AccessTokenExpireMinutes, RefreshTokenExpireDays: cfg.JWT.RefreshTokenExpireDays}
		options.Server.Mode = cfg.Server.Mode
		options.Turnstile.Required = cfg.Turnstile.Required
	}
	deps := &identity.AuthDependencies{Users: users, Redeem: redeemRepo, RefreshTokens: refreshTokenCache, Options: options, Turnstile: turnstileService, Tencent: tencentCaptchaService, Aliyun: aliyunCaptchaService, DefaultSubscriptions: defaultSubAssigner, Invalidator: authCacheInvalidator, BalanceCache: billingCache, Observer: identity.Observer{Log: logging.LegacyPrintf}, DomainRegistration: users, NormalizedEmailConflict: users, EmailAliasGuard: users, AliasLookup: users, AliasOwner: users}
	if settingService != nil {
		deps.Settings = settingService
	}
	if emailService != nil {
		deps.Email = emailService
	}
	if emailQueueService != nil {
		deps.EmailQueue = emailQueueService
	}
	if promoService != nil {
		deps.Promo = promoService
	}
	if affiliateService != nil {
		deps.Affiliate = identityPromotion{Service: affiliateService}
	}
	state := identitypostgres.NewAuthState(entClient, deps)
	core := identity.NewAuthService(deps, &identitypostgres.AuthRepository{State: state})
	state.Rules = core
	return &identityAuthGraph{Core: core, State: state, Client: entClient}
}

// provideIdentityProfiles uses application task tracking to wait for background profile operations during
// shutdown.
func provideIdentityProfiles(users *identitypostgres.UserStore, settings settingscore.Repository, keys apikey.APIKeyAuthCacheInvalidator, cache billing.BillingCache, tasks *lifecycle.Tasks) *identity.UserService {
	return identity.NewUserService(users, settings, keys, cache, func(name string, fn func()) bool { return tasks.Go(name, fn) }, time.Now)
}
