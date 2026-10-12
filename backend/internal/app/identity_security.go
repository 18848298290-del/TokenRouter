package app

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/app/bootstrap"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	identityadapter "github.com/TokenFlux/TokenRouter/internal/identity/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/notification"
)

// provideSecretEncryptor creates the AES encryptor from startup configuration.
func provideSecretEncryptor(cfg *config.Config) (identity.SecretEncryptor, error) {
	return bootstrap.NewAESEncryptor(cfg)
}

// provideTotp binds identity storage, notification delivery, and settings.
func provideTotp(users *identitypostgres.UserStore, encryptor identity.SecretEncryptor, cache identity.TotpCache, settings *identityAuthSettings, email *identity.EmailChallenges, queue *notification.EmailQueueService) *identity.TotpService {
	return identity.NewTotpService(users, encryptor, cache, settings, email, queue, time.Now)
}

// providePasskey creates the SDK verifier; the HTTP adapter parses requests and converts user data.
func providePasskey(cfg *config.Config, repo identity.PasskeyRepository, sessions identity.PasskeySessionStore, users *identitypostgres.UserStore) (*identity.PasskeyService, error) {
	options := identityadapter.PasskeyOptions{Enabled: cfg.WebAuthn.Enabled, RPID: cfg.WebAuthn.RPID, RPDisplayName: cfg.WebAuthn.RPDisplayName, RPOrigins: cfg.WebAuthn.RPOrigins}
	verifier, e := identityadapter.NewPasskeyVerifier(options)
	if e != nil {
		return nil, e
	}
	return identity.NewPasskeyService(options.Enabled, verifier, repo, sessions, users, time.Now), nil
}

func providePasskeyHTTP(passkeys *identity.PasskeyService, auth *identityAuthGraph, backend *admission.BackendMode) *identityhttp.PasskeyHandler {
	return identityhttp.NewPasskeyHandler(passkeys, auth.Core, backend)
}

func provideTurnstile(settings *identity.RuntimeSettings, verifier identity.TurnstileVerifier) *identity.TurnstileService {
	s := identity.NewTurnstileService(settings, verifier)
	s.SetObserver(logging.LegacyPrintf)
	return s
}

func provideTencentCaptcha(settings *identity.RuntimeSettings, verifier identity.TencentCaptchaVerifier) *identity.TencentCaptchaService {
	s := identity.NewTencentCaptchaService(settings, verifier)
	s.SetObserver(logging.LegacyPrintf)
	return s
}

func provideAliyunCaptcha(settings *identity.RuntimeSettings, verifier identity.AliyunCaptchaVerifier) *identity.AliyunCaptchaService {
	s := identity.NewAliyunCaptchaService(settings, verifier)
	s.SetObserver(logging.LegacyPrintf)
	return s
}
