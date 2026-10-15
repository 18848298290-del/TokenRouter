package provider

import (
	"context"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/gemini/codeassist"
)

// gemini_authorization.go binds Gemini callback addresses, OAuth
// configuration, and platform operations to provider authorization. Gemini
// authorization uses distinct CLI and AI Studio callback addresses and
// effective OAuth client settings. The provider workflow receives the dynamic
// configuration reader alongside platform PKCE, authorization URL and project
// discovery operations, resolving those inputs when its authorization steps
// run.

// GeminiAuthorizationOptions binds OAuth configuration and platform
// authorization operations.
// Config retains the supplied callback for workflow-time reads, and ResolveProxy
// retains the resolver. Built-in client and redirect constants support the core's
// variant selection; this factory itself performs no project or token query.
func GeminiAuthorizationOptions(config func() google.OAuthConfig, resolveProxy func(context.Context, int64) (string, bool)) provider.GeminiAuthorizationOptions {
	return provider.GeminiAuthorizationOptions{
		Config:                config,
		BuiltinClientID:       codeassist.GeminiCLIOAuthClientID,
		CLIRedirectURI:        codeassist.GeminiCLIRedirectURI,
		AIStudioRedirectURI:   codeassist.AIStudioOAuthRedirectURI,
		GenerateState:         codeassist.GenerateState,
		GenerateCodeVerifier:  codeassist.GenerateCodeVerifier,
		GenerateSessionID:     codeassist.GenerateSessionID,
		GenerateCodeChallenge: codeassist.GenerateCodeChallenge,
		EffectiveOAuthConfig:  codeassist.EffectiveOAuthConfig,
		BuildAuthorizationURL: codeassist.BuildAuthorizationURL,
		FetchProject:          codeassist.FetchProjectIDFromResourceManager,
		ResolveProxy:          resolveProxy,
		Logf:                  func(format string, args ...any) { logging.LegacyPrintf("service.gemini_oauth", format, args...) },
		Printf:                func(format string, args ...any) { _, _ = fmt.Printf(format, args...) },
	}
}
