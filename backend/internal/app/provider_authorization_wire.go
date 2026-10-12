//go:build wireinject

package app

import (
	"github.com/google/wire"

	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	providerhttp "github.com/TokenFlux/TokenRouter/internal/provider/httpapi"
)

// Gemini and Antigravity OAuth handlers consume authorization interfaces
// resolved by the provider bindings. This HTTP set also supplies Claude and
// Qoder authorization handlers and Codex invite reset for administrator route
// mounting, alongside the identity user-attribute handler.

// providerAuthorizationHTTPProviders supplies platform authorization, Codex
// invite-reset, and identity attribute HTTP constructors used by administrator
// routes.
var providerAuthorizationHTTPProviders = wire.NewSet(
	provideClaudeAuthorizationHTTP,
	provideQoderAuthorizationHTTP,
	providerhttp.NewGeminiOAuthHandler,
	providerhttp.NewAntigravityOAuthHandler,
	providerhttp.NewCodexInviteResetHandler,
	identityhttp.NewUserAttributeHandler,
)
