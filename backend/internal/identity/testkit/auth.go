package testkit

import (
	"context"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/notification"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
)

type affiliate struct{ source *promotion.AffiliateService }

// AuthOptions copies startup configuration for tests without reading runtime settings or the environment.
func AuthOptions(cfg *config.Config) *identity.AuthOptions {
	if cfg == nil {
		return nil
	}
	out := &identity.AuthOptions{}
	out.Default.UserBalance = cfg.Default.UserBalance
	out.Default.UserConcurrency = cfg.Default.UserConcurrency
	out.JWT = identity.SessionOptions{Secret: cfg.JWT.Secret, ExpireHour: cfg.JWT.ExpireHour, AccessTokenExpireMinutes: cfg.JWT.AccessTokenExpireMinutes, RefreshTokenExpireDays: cfg.JWT.RefreshTokenExpireDays}
	out.Server.Mode = cfg.Server.Mode
	out.Turnstile.Required = cfg.Turnstile.Required
	return out
}

// Auth wires the transaction adapter and optional capabilities supported by the supplied user repository.
func Auth(client *dbent.Client, deps *identity.AuthDependencies) *identity.AuthService {
	if deps.Observer.Log == nil {
		deps.Observer.Log = logging.LegacyPrintf
	}
	if source, ok := deps.Users.(identity.RegistrationEmailDomainRepository); ok {
		deps.DomainRegistration = source
	}
	if source, ok := deps.Users.(identity.AuthNormalizedEmailBindingConflictChecker); ok {
		deps.NormalizedEmailConflict = source
	}
	if source, ok := deps.Users.(identity.AuthEmailIdentityAliasGuardRepository); ok {
		deps.EmailAliasGuard = source
	}
	if source, ok := deps.Users.(identity.EmailAliasLookupRepository); ok {
		deps.AliasLookup = source
	}
	if source, ok := deps.Users.(identity.EmailAliasOwnerLookupRepository); ok {
		deps.AliasOwner = source
	}
	state := identitypostgres.NewAuthState(client, deps)
	core := identity.NewAuthService(deps, &identitypostgres.AuthRepository{State: state})
	state.Rules = core
	return core
}

// Email returns a nil interface when the concrete email service is nil.
func Email(value *identity.EmailChallenges) identity.AuthEmail {
	if value == nil {
		return nil
	}
	return value
}

// EmailQueue returns a nil interface when the queue pointer is nil.
func EmailQueue(value *notification.EmailQueueService) identity.AuthEmailQueue {
	if value == nil {
		return nil
	}
	return value
}

// Promo adapts a promotion service to authentication operations, preserving nil.
func Promo(value *promotion.PromoService) identity.AuthPromo {
	if value == nil {
		return nil
	}
	return value
}

// Affiliate adapts return values while delegating referral rules to the promotion service.
func Affiliate(value *promotion.AffiliateService) identity.AuthAffiliate {
	if value == nil {
		return nil
	}
	return affiliate{value}
}

func (a affiliate) EnsureUserAffiliate(ctx context.Context, id int64) error {
	_, err := a.source.EnsureUserAffiliate(ctx, id)
	return err
}

func (a affiliate) BindInviterByCode(ctx context.Context, id int64, code string) error {
	return a.source.BindInviterByCode(ctx, id, code)
}
