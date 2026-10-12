package app

import (
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/transport"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	providerauth "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
	"github.com/TokenFlux/TokenRouter/internal/upstream/gemini/codeassist"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// provideHTTPUpstream constructs shared transport that reads configuration on each call.
// @project-doc docs/architecture/system_architecture.md#dependency_layers
func provideHTTPUpstream(cfg *config.Config) *transport.Client {
	return transport.New(func() *transport.Options {
		return httpTransportOptions(cfg)
	})
}

func httpTransportOptions(cfg *config.Config) *transport.Options {
	if cfg == nil {
		return nil
	}
	gateway := cfg.Gateway
	http2 := gateway.OpenAIHTTP2
	return &transport.Options{
		ValidateResolvedIP:          cfg.Security.URLAllowlist.Enabled && !cfg.Security.URLAllowlist.AllowPrivateHosts,
		ConnectionPoolIsolation:     gateway.ConnectionPoolIsolation,
		MaxUpstreamClients:          gateway.MaxUpstreamClients,
		ClientIdleTTLSeconds:        gateway.ClientIdleTTLSeconds,
		MaxIdleConns:                gateway.MaxIdleConns,
		MaxIdleConnsPerHost:         gateway.MaxIdleConnsPerHost,
		MaxConnsPerHost:             gateway.MaxConnsPerHost,
		IdleConnTimeoutSeconds:      gateway.IdleConnTimeoutSeconds,
		ResponseHeaderTimeout:       gateway.ResponseHeaderTimeout,
		OpenAIResponseHeaderTimeout: gateway.OpenAIResponseHeaderTimeout,
		GrokResponseHeaderTimeout:   gateway.GrokResponseHeaderTimeout,
		OpenAIHTTP2: transport.HTTP2Options{
			Enabled:                   http2.Enabled,
			AllowProxyFallbackToHTTP1: http2.AllowProxyFallbackToHTTP1,
			FallbackErrorThreshold:    http2.FallbackErrorThreshold,
			FallbackWindowSeconds:     http2.FallbackWindowSeconds,
			FallbackTTLSeconds:        http2.FallbackTTLSeconds,
		},
	}
}

// provideClaudeUsageFetcher queries Claude usage through the shared HTTP pool.
func provideClaudeUsageFetcher(upstream httpclient.UpstreamTransport) provideradapter.ClaudeUsageClient {
	if upstream == nil {
		return anthropic.NewUsageClient(nil)
	}
	return anthropic.NewUsageClient(upstream.DoWithTLS)
}

// provideOpenAIOAuthClient binds transport to the OpenAI OAuth client.
func provideOpenAIOAuthClient(upstream httpclient.UpstreamTransport) provideradapter.OpenAIOAuthClient {
	return openai.NewOAuthClient(upstream)
}

// provideGeminiOAuthClient reads startup configuration on each OAuth call.
func provideGeminiOAuthClient(cfg *config.Config) providerauth.GeminiOAuthClient {
	return codeassist.NewOAuthClient(func() codeassist.OAuthConfig {
		return codeassist.OAuthConfig{
			ClientID:     cfg.Gemini.OAuth.ClientID,
			ClientSecret: cfg.Gemini.OAuth.ClientSecret,
			Scopes:       cfg.Gemini.OAuth.Scopes,
		}
	})
}
