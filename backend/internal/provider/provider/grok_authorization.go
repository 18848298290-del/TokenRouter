package provider

import (
	"context"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// grok_authorization.go supplies Grok OAuth and JWT operations with optional
// proxy lookup and password-auth configuration. Grok authorization can resolve
// a selected proxy without treating a missing proxy row as a repository
// failure. The returned options bind the platform's OAuth and JWT operations
// to that lookup, while an omitted password-auth reader keeps password login
// disabled for the provider workflow.

// GrokAuthorizationOptions binds platform authorization operations and
// optional proxy lookup.
// A nil passwordEnabled callback selects disabled password authentication. When
// proxies is supplied, not-found and nil records become an unresolved proxy,
// while other repository errors remain visible to the core authorization service.
func GrokAuthorizationOptions(proxies egress.ProxyRepository, passwordEnabled func() bool) provider.GrokAuthorizationOptions {
	if passwordEnabled == nil {
		passwordEnabled = func() bool { return false }
	}
	options := provider.GrokAuthorizationOptions{
		PasswordAuthEnabled:     passwordEnabled,
		GenerateState:           grok.GenerateState,
		GenerateNonce:           grok.GenerateNonce,
		GenerateCodeVerifier:    grok.GenerateCodeVerifier,
		GenerateSessionID:       grok.GenerateSessionID,
		EffectiveRedirectURI:    grok.EffectiveRedirectURI,
		GenerateCodeChallenge:   grok.GenerateCodeChallenge,
		BuildAuthorizationURL:   grok.BuildAuthorizationURL,
		EffectiveClientID:       grok.EffectiveClientID,
		EffectiveScope:          grok.EffectiveScope,
		ParseAuthorizationInput: grok.ParseAuthorizationInput,
		DecodeJWTClaims:         grok.DecodeJWTClaims,
		JWTClaimString:          grok.JWTClaimString,
		SubscriptionTierFromJWT: grok.SubscriptionTierFromJWT,
		DefaultClientID:         grok.DefaultClientID,
		DefaultCLIBaseURL:       grok.DefaultCLIBaseURL,
	}
	if proxies != nil {
		options.LookupProxy = func(ctx context.Context, id int64) (string, bool, error) {
			proxy, err := proxies.GetByID(ctx, id)
			// A missing proxy is an unresolved selection, allowing the core
			// workflow
			// to report that input separately from an actual proxy-repository
			// outage.
			if errors.Is(err, egress.ErrProxyNotFound) {
				return "", false, nil
			}
			if err != nil {
				return "", false, err
			}
			if proxy == nil {
				return "", false, nil
			}
			return proxy.URL(), true, nil
		}
	}
	return options
}
