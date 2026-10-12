package provider

import (
	"context"
	"log"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic/oauth"
)

// Claude authorization distinguishes full OAuth, API-key creation and inference
// scopes when preparing provider sessions. The core authorization workflow receives
// the platform's PKCE and URL operations together with those scope choices,
// standard logging and the supplied proxy resolver for later exchanges.

// ClaudeAuthorizationOptions supplies platform OAuth scopes and authorization
// primitives.
// It retains the supplied proxy resolver in the returned options. Session
// creation, token exchange and lifecycle handling remain with the core service
// that consumes these callbacks.
func ClaudeAuthorizationOptions(resolveProxy func(context.Context, int64) (string, bool)) provider.ClaudeAuthorizationOptions {
	return provider.ClaudeAuthorizationOptions{
		ScopeOAuth:            oauth.ScopeOAuth,
		ScopeAPI:              oauth.ScopeAPI,
		ScopeInference:        oauth.ScopeInference,
		GenerateState:         oauth.GenerateState,
		GenerateCodeVerifier:  oauth.GenerateCodeVerifier,
		GenerateCodeChallenge: oauth.GenerateCodeChallenge,
		GenerateSessionID:     oauth.GenerateSessionID,
		BuildAuthorizationURL: oauth.BuildAuthorizationURL,
		Logf:                  log.Printf,
		ResolveProxy:          resolveProxy,
	}
}
