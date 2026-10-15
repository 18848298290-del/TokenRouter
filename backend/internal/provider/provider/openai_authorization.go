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

// openai_authorization.go supplies OpenAI authorization callbacks using
// current proxy, TLS, and privacy dependencies. The provider core owns
// authorization sessions and metadata. The application dependency object is
// captured by those callbacks and read at invocation, allowing configured
// router and profile choices to determine each token request's transport
// options.
//
// An enabled router needs a token UA or a selected profile before dynamic
// Codex UA lookup is considered. A profile-ID pointer counts as selected even
// when it points to zero, and missing profile resolution can still leave a
// usable UA option. Provider ID and concurrency accompany usable
// provider-backed options. These interacting choices determine whether
// customization reaches the OAuth client, while privacy and PAT endpoint
// defaults are resolved independently.

// OpenAIOAuthClient supplies upstream code exchange and refresh for shared
// provider authorization.
type OpenAIOAuthClient interface {
	// ExchangeCode trades code and PKCE verifier for token metadata using redirect,
	// proxy and client ID. Callers own session proof validation and subsequent
	// persistence; implementations apply the supplied context to their request handling.
	ExchangeCode(context.Context, string, string, string, string, string, ...openai.OAuthTokenRequestOptions) (*wire.OAuthTokenResponse, error)
	// RefreshToken exchanges a refresh token through the selected proxy using the
	// client's default OAuth identity. The caller owns any response merging and
	// credential persistence after the operation returns.
	RefreshToken(context.Context, string, string, ...openai.OAuthTokenRequestOptions) (*wire.OAuthTokenResponse, error)
	// RefreshTokenWithClientID refreshes with the supplied OAuth client identity and
	// optional transport parameters. The client interprets an empty identity, while
	// callers retain responsibility for storing a successful response.
	RefreshTokenWithClientID(context.Context, string, string, string, ...openai.OAuthTokenRequestOptions) (*wire.OAuthTokenResponse, error)
}

// OpenAITokenRouterReader supplies the runtime TLS router selected for token
// exchange.
type OpenAITokenRouterReader interface {
	// GetRuntimeRouter returns the selected runtime router or nil when unavailable.
	// tokenRequestOptions also checks Enabled before using this shared router's token
	// transport settings; callers treat returned configuration as immutable during use.
	GetRuntimeRouter(int64) *egress.TLSFingerprintRouter
}

// OpenAITokenProfileResolver supplies token transport profiles selected by a
// router's profile ID.
type OpenAITokenProfileResolver interface {
	// ResolveTokenTLSProfileByID returns a token-exchange TLS profile and whether it
	// was resolved. Consumers borrow the pointer and can retain usable UA options
	// when profile selection is unavailable.
	ResolveTokenTLSProfileByID(int64) (*tlsfingerprint.Profile, bool)
}

// OpenAIAuthorizationDependencies contains borrowed transport and lookup
// services captured by authorization callbacks.
type OpenAIAuthorizationDependencies struct {
	// Proxies is required when an operation selects a proxy lookup. Refresh
	// checks availability first, while generation and exchange query a supplied ID directly.
	Proxies egress.ProxyRepository
	// Client is required for used OAuth exchanges and is borrowed from the
	// application. Callback construction neither initializes it nor owns its shutdown.
	Client OpenAIOAuthClient
	// PrivacyFactory gates metadata and training enrichment. Nil disables that
	// enrichment; a configured factory owns proxy-specific privacy client construction.
	PrivacyFactory openai.PrivacyClientFactory
	// PrivacyEndpoints supplies optional overrides read at each callback invocation.
	// Empty members receive the corresponding production settings, account and subscription URLs.
	PrivacyEndpoints openai.PrivacyEndpoints
	// WhoamiURL overrides PAT validation's endpoint when nonempty. Empty input
	// selects the production account-credential whoami URL when validation is invoked.
	WhoamiURL string
	// CodexUserAgent optionally fills a blank router UA after a profile selection
	// exists. Routers with no token configuration return before this callback is consulted.
	CodexUserAgent func(context.Context) string
	// Routers is optional and resolves positive token router IDs at invocation.
	// Missing or disabled router selections produce no request options.
	Routers OpenAITokenRouterReader
	// Profiles optionally resolves the selected token profile. Missing profiles
	// can still leave an option usable when its router or dynamic UA is nonempty.
	Profiles OpenAITokenProfileResolver
}

// OpenAIAuthorizationOptions returns callbacks that read the captured
// dependency object when authorization operations run. Supply nonnil deps,
// callable clients for used exchanges and proxy readers when selected; callers
// synchronize dependency changes with concurrent callback use.
//
// Empty privacy or whoami URLs receive production endpoint defaults at
// invocation. Privacy availability follows the factory, PAT validation trims
// the supplied token and uses whoami, and token exchanges resolve enabled
// router options through tokenRequestOptions. The returned callbacks share
// these dependencies rather than allocating authorization sessions or owning
// their shutdown.
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
			// Endpoint selection is read at validation time so a supplied whoami URL
			// reaches the PAT client; otherwise use its account-credential endpoint default.
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

// tokenRequestOptions returns applicable exchange transport options for a
// positive, enabled router selection, or nil when no usable options remain. A
// router with neither token UA nor profile selection returns nil before the
// dynamic Codex UA reader is consulted.
//
// When a profile ID is selected and router UA is blank, a configured dynamic
// reader can supply the UA. Profile resolution may fail while a usable UA
// still permits an option. Provider identity and concurrency are attached when
// value is nonnil, and the resolved profile pointer remains borrowed. Callers
// keep the dependency object and returned profile stable while requests
// consume them.
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
	// A selected profile enables token customization even with no router UA;
	// use the dynamic Codex identity for that path when its reader is available.
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
