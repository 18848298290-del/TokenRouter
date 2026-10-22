package app

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	providerauth "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// runtime_auth.go registers authentication caches, platform authorization
// maintenance, and credential-session cleanup with the lifecycle manager.
// Authentication caches and platform authorization maintenance start before
// HTTP serving and stop before shared storage. Qoder credential builds have an
// earlier stop phase than authorization-store maintenance, letting managed
// session work finish before completion queues and connection resources are
// closed.

// authRuntimeReady marks registration of authentication, authorization, and
// TLS subscription lifecycle hooks.
type authRuntimeReady struct{}

// provideAuthRuntime registers authentication caches, authorization
// maintenance, TLS subscriptions, and credential-session cleanup. Call before
// manager startup or shutdown. Most callbacks skip service calls when their
// dependency is nil; all hooks remain registered. Qoder credential and
// error-rule callbacks are bound directly and retain
// their implementations' nil and stop behavior.
func provideAuthRuntime(
	apiKeyService *apikey.APIKeyService,
	errorPassthrough *errorpolicy.ErrorPassthroughService,
	oauth *providerauth.ClaudeAuthorization,
	openaiOAuth *providerauth.OpenAIAuthorization,
	geminiOAuth *providerauth.GeminiAuthorization,
	antigravityOAuth *providerauth.AntigravityAuthorization,
	qoderOAuth *provideradapter.QoderAuthorization,
	qoderTokens *provideradapter.QoderTokenProvider,

	grokOAuth *providerauth.GrokAuthorization,
	tlsFingerprintProfile *egress.TLSFingerprintProfileService,
	tlsFingerprintRouter *egress.TLSFingerprintRouterService,
	manager *lifecycle.Manager,
) *authRuntimeReady {
	manager.Register(lifecycle.Hook{Name: "APIKeyService", StartOrder: 195, StopOrder: shutdownStageAPIKeys, Start: func(ctx context.Context) error {
		if apiKeyService != nil {
			apiKeyService.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if apiKeyService != nil {
			return apiKeyService.StopContext(ctx)
		}
		return nil
	}})

	manager.Register(lifecycle.Hook{Name: "OAuthService", StartOrder: 190, StopOrder: shutdownStageAuthAndSubscriptions, Start: func(ctx context.Context) error {
		if oauth != nil {
			oauth.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if oauth != nil {
			return oauth.StopContext(ctx)
		}
		return nil
	}})
	manager.Register(lifecycle.Hook{Name: "OpenAIOAuthService", StartOrder: 190, StopOrder: shutdownStageAuthAndSubscriptions, Start: func(ctx context.Context) error {
		if openaiOAuth != nil {
			openaiOAuth.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if openaiOAuth != nil {
			return openaiOAuth.StopContext(ctx)
		}
		return nil
	}})
	manager.Register(lifecycle.Hook{Name: "GeminiOAuthService", StartOrder: 190, StopOrder: shutdownStageAuthAndSubscriptions, Start: func(ctx context.Context) error {
		if geminiOAuth != nil {
			geminiOAuth.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if geminiOAuth != nil {
			return geminiOAuth.StopContext(ctx)
		}
		return nil
	}})
	manager.Register(lifecycle.Hook{Name: "AntigravityOAuthService", StartOrder: 190, StopOrder: shutdownStageAuthAndSubscriptions, Start: func(ctx context.Context) error {
		if antigravityOAuth != nil {
			antigravityOAuth.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if antigravityOAuth != nil {
			return antigravityOAuth.StopContext(ctx)
		}
		return nil
	}})
	// Credential builds are managed operations started on demand. Cancel and
	// wait for them after earlier request barriers and before completion consumers
	// or shared connections enter their stop phases.
	manager.Register(lifecycle.Hook{Name: "QoderCredentialSessions", StopOrder: shutdownStageAggregatesSessionsAndSearch, Stop: qoderTokens.StopContext})
	manager.Register(lifecycle.Hook{Name: "QoderOAuthService", StartOrder: 190, StopOrder: shutdownStageAuthAndSubscriptions, Start: func(ctx context.Context) error {
		if qoderOAuth != nil {
			qoderOAuth.Core.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if qoderOAuth != nil {
			return qoderOAuth.Core.StopContext(ctx)
		}
		return nil
	}})
	manager.Register(lifecycle.Hook{Name: "GrokOAuthService", StartOrder: 190, StopOrder: shutdownStageAuthAndSubscriptions, Start: func(ctx context.Context) error {
		if grokOAuth != nil {
			grokOAuth.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if grokOAuth != nil {
			return grokOAuth.StopContext(ctx)
		}
		return nil
	}})

	manager.Register(lifecycle.Hook{Name: "TLSFingerprintProfileService", StartOrder: 190, StopOrder: shutdownStageAuthAndSubscriptions, Start: func(ctx context.Context) error {
		if tlsFingerprintProfile != nil {
			tlsFingerprintProfile.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if tlsFingerprintProfile != nil {
			tlsFingerprintProfile.Stop()
		}
		return nil
	}})
	manager.Register(lifecycle.Hook{Name: "TLSFingerprintRouterService", StartOrder: 190, StopOrder: shutdownStageAuthAndSubscriptions, Start: func(ctx context.Context) error {
		if tlsFingerprintRouter != nil {
			tlsFingerprintRouter.Start()
		}
		return nil
	}, Stop: func(ctx context.Context) error {
		if tlsFingerprintRouter != nil {
			tlsFingerprintRouter.Stop()
		}
		return nil
	}})

	manager.Register(lifecycle.Hook{
		Name: "ErrorPassthroughService", StartOrder: 190, StopOrder: shutdownStageAuthAndSubscriptions,
		Start: errorPassthrough.StartContext,
		Stop:  errorPassthrough.StopContext,
	})
	return &authRuntimeReady{}
}
