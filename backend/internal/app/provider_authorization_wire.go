//go:build wireinject

package app

import (
	"github.com/google/wire"

	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	providerhttp "github.com/TokenFlux/TokenRouter/internal/provider/httpapi"
)

// providerAuthorizationHTTPProviders groups platform authorization HTTP constructors.
var providerAuthorizationHTTPProviders = wire.NewSet(
	provideClaudeAuthorizationHTTP,
	provideQoderAuthorizationHTTP,
	providerhttp.NewGeminiOAuthHandler,
	providerhttp.NewAntigravityOAuthHandler,
	providerhttp.NewCodexInviteResetHandler,
	identityhttp.NewUserAttributeHandler,
)
