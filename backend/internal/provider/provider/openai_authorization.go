package provider

import (
	"context"
	"log/slog"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// OpenAIOAuthClient exchanges and refreshes credentials for the authorization service.
type OpenAIOAuthClient interface {
	ExchangeCode(context.Context, string, string, string, string, string, ...openai.OAuthTokenRequestOptions) (*wire.OAuthTokenResponse, error)
	RefreshToken(context.Context, string, string, ...openai.OAuthTokenRequestOptions) (*wire.OAuthTokenResponse, error)
	RefreshTokenWithClientID(context.Context, string, string, string, ...openai.OAuthTokenRequestOptions) (*wire.OAuthTokenResponse, error)
}

type OpenAITokenRouterReader interface {
	GetRuntimeRouter(int64) *egress.TLSFingerprintRouter
}

type OpenAITokenProfileResolver interface {
	ResolveTokenTLSProfileByID(int64) (*tlsfingerprint.Profile, bool)
}

// OpenAIAuthorizationDependencies contains application-provided authorization infrastructure.
type OpenAIAuthorizationDependencies struct {
	Proxies          egress.ProxyRepository
	Client           OpenAIOAuthClient
	PrivacyFactory   openai.PrivacyClientFactory
	PrivacyEndpoints openai.PrivacyEndpoints
	WhoamiURL        string
	CodexUserAgent   func(context.Context) string
	Routers          OpenAITokenRouterReader
	Profiles         OpenAITokenProfileResolver
}

// OpenAIAuthorizationOptions binds settings, proxy, TLS, and privacy lookups as callbacks for
// authorization operations.
func OpenAIAuthorizationOptions(deps *OpenAIAuthorizationDependencies) provider.OpenAIAuthOptions {
	privacy := func() openai.PrivacyClient {
		endpoints := deps.PrivacyEndpoints
		if endpoints.Settings == "" {
			endpoints.Settings = "https://chatgpt.com/backend-api/settings/account_user_setting"
		}
		if endpoints.Providers == "" {
			endpoints.Providers = "https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27"
		}
		if endpoints.Subscriptions == "" {
			endpoints.Subscriptions = "https://chatgpt.com/backend-api/subscriptions"
		}
		return openai.PrivacyClient{Endpoints: endpoints}
	}
	return provider.OpenAIAuthOptions{
		ClientID:                         openai.ClientID,
		DefaultRedirectURI:               openai.DefaultRedirectURI,
		GenerateState:                    openai.GenerateState,
		GenerateCodeVerifier:             openai.GenerateCodeVerifier,
		GenerateSessionID:                openai.GenerateSessionID,
		GenerateCodeChallenge:            openai.GenerateCodeChallenge,
		OAuthClientConfigByPlatform:      openai.OAuthClientConfigByPlatform,
		BuildAuthorizationURLForPlatform: openai.BuildAuthorizationURLForPlatform,
		ProxyAvailable:                   func() bool { return deps.Proxies != nil },
		ProxyURL: func(ctx context.Context, id int64) (string, bool, error) {
			proxy, err := deps.Proxies.GetByID(ctx, id)
			if err != nil || proxy == nil {
				return "", false, err
			}
			return proxy.URL(), true, nil
		},
		Exchange: func(ctx context.Context, code, verifier, redirect, proxy, clientID string, routerID int64) (*wire.OAuthTokenResponse, error) {
			options := deps.tokenRequestOptions(ctx, routerID, nil)
			return deps.Client.ExchangeCode(ctx, code, verifier, redirect, proxy, clientID, options...)
		},
		Refresh: func(ctx context.Context, refresh, proxy, clientID string, routerID int64, value *provider.Record) (*wire.OAuthTokenResponse, error) {
			options := deps.tokenRequestOptions(ctx, routerID, value)
			return deps.Client.RefreshTokenWithClientID(ctx, refresh, proxy, clientID, options...)
		},
		ParseIDToken:     openai.ParseIDToken,
		DecodeIDToken:    openai.DecodeIDToken,
		PrivacyAvailable: func() bool { return deps.PrivacyFactory != nil },
		FetchProviderInfo: func(ctx context.Context, token, proxy, org string) *wire.ChatGPTAccountInfo {
			return privacy().FetchChatGPTAccountInfo(ctx, deps.PrivacyFactory, token, proxy, org)
		},
		FetchSubscription: func(ctx context.Context, token, proxy, id string) string {
			return privacy().FetchChatGPTSubscriptionExpiresAt(ctx, deps.PrivacyFactory, token, proxy, id)
		},
		DisableTraining: func(ctx context.Context, token, proxy string) string {
			return privacy().DisableOpenAITraining(ctx, deps.PrivacyFactory, token, proxy)
		},
		ValidatePAT: func(ctx context.Context, token, proxy string) (*provider.OpenAITokenInfo, error) {
			endpoint := deps.WhoamiURL
			if endpoint == "" {
				endpoint = "https://auth.openai.com/api/accounts/v1/user-auth-credential/whoami"
			}
			result, err := openai.ValidatePersonalAccessToken(ctx, token, proxy, endpoint)
			if err != nil {
				return nil, err
			}
			return provider.OpenAIPATTokenInfo(strings.TrimSpace(token), result), nil
		},
		Warn: slog.Warn,
	}
}

// tokenRequestOptions resolves the selected enabled TLS router, returning nil when it has no
// applicable options.
func (deps *OpenAIAuthorizationDependencies) tokenRequestOptions(ctx context.Context, routerID int64, value *provider.Record) []openai.OAuthTokenRequestOptions {
	if routerID <= 0 || deps.Routers == nil {
		return nil
	}
	router := deps.Routers.GetRuntimeRouter(routerID)
	if router == nil || !router.Enabled {
		return nil
	}
	userAgent := strings.TrimSpace(router.ChatGPTOAuthTokenUserAgent)
	profileID := router.ChatGPTOAuthTokenTLSFingerprintProfileID
	if userAgent == "" && profileID == nil {
		return nil
	}
	option := openai.OAuthTokenRequestOptions{UserAgent: userAgent}
	if option.UserAgent == "" && deps.CodexUserAgent != nil {
		option.UserAgent = strings.TrimSpace(deps.CodexUserAgent(ctx))
	}
	if value != nil {
		option.ProviderID = value.ID
		option.ProviderConcurrency = value.Concurrency
	}
	if profileID != nil && deps.Profiles != nil {
		if profile, ok := deps.Profiles.ResolveTokenTLSProfileByID(*profileID); ok {
			option.TLSProfile = profile
		}
	}
	if option.UserAgent == "" && option.TLSProfile == nil {
		return nil
	}
	return []openai.OAuthTokenRequestOptions{option}
}
