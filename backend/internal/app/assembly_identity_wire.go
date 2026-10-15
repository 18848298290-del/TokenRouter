//go:build wireinject

package app

import (
	"github.com/google/wire"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	identityprovider "github.com/TokenFlux/TokenRouter/internal/identity/provider"
	identityredis "github.com/TokenFlux/TokenRouter/internal/identity/rediscache"
)

// assembly_identity_wire.go registers identity services, challenge verifiers,
// persistence, and HTTP authentication dependencies with Wire.
// Login endpoints, JWT middleware, and administrator authorization share the
// application's authentication graph. Redis stores supply refresh-token, TOTP,
// and passkey state, while external challenge verifiers are injected into the
// identity services used by those HTTP entry points.

// identityAssemblyProviders supplies identity persistence, authentication
// services, challenge verifiers, and HTTP authorization dependencies for
// application routes.
var identityAssemblyProviders = wire.NewSet(
	provideIdentityAdminHTTP,
	identityhttp.NewTotpHandler,
	provideIdentityAuthSettings,
	provideRiskStatus,
	provideEmailCache,
	provideEmailChallenges,
	provideIdentityAdmin,
	provideTotp,
	providePasskey,
	providePasskeyHTTP,
	provideTurnstile,
	provideTencentCaptcha,
	provideAliyunCaptcha,
	identity.NewUserAttributeService,
	identitypostgres.NewPasskeyRepository,
	identitypostgres.NewUserAttributeDefinitionRepository,
	identitypostgres.NewUserAttributeValueRepository,
	identityredis.NewTotpCache,
	identityredis.NewRefreshTokenCache,
	identityredis.NewPasskeySessionStore,
	identityprovider.NewTurnstileVerifier,
	identityprovider.NewTencentCaptchaVerifier,
	identityprovider.NewAliyunCaptchaVerifier,
	provideIdentityHTTP,
	provideIdentityAuthGraph,
	provideIdentityProfiles,
	identitypostgres.NewUserStore,
	provideSecretEncryptor,
	provideOAuthSettings,
	provideGrantSettings,
	provideIdentitySettings,
	identityhttp.NewAdminKeySettingsHandler,
	providePanelUserHTTP,
	provideJWTAuth,
	provideAdminAuth,
	provideStepUpAuth,
)
