package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providerauth "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	upstreamopenai "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

type openaiOAuthClientRefreshStub struct {
	refreshCalls int32
	lastOptions  []upstreamopenai.OAuthTokenRequestOptions
	lastClientID string
}

type openAIOAuthTokenProfileResolverStub struct {
	profiles map[int64]*tlsfingerprint.Profile
}

type openAIOAuthSettingRepoStub struct {
	values map[string]string
}

type openaiOAuthClientAuthURLStub struct{}

type openaiOAuthClientStateStub struct {
	exchangeCalled int32
	lastClientID   string
	lastOptions    []upstreamopenai.OAuthTokenRequestOptions
}

type chatGPTBackendTestConfig struct {
	providers    map[string]any
	subscription func(providerID string) map[string]any
}

func TestOpenAIOAuthService_RefreshProviderToken_NoRefreshTokenUsesExistingAccessToken(t *testing.T) {
	client := &openaiOAuthClientRefreshStub{}
	deps := &OpenAIAuthorizationDependencies{}
	svc := newOpenAIAuthorizationForTest(t, nil, client, deps)
	svc.Start()
	var privacyClientCalls int32
	deps.PrivacyFactory = func(proxyURL string) (*req.Client, error) {
		atomic.AddInt32(&privacyClientCalls, 1)
		return nil, errors.New("stop before request")
	}

	expiresAt := time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339)
	provider := &providerauth.Record{
		ID:       77,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "existing-access-token",
			"expires_at":   expiresAt,
			"client_id":    "client-id-1",
		},
	}

	info, err := svc.RefreshProviderToken(context.Background(), provider)
	require.NoError(t, err)
	require.NotNil(t, info)
	require.Equal(t, "existing-access-token", info.AccessToken)
	require.Equal(t, "client-id-1", info.ClientID)
	require.Zero(t, atomic.LoadInt32(&client.refreshCalls), "已有 access token 应该复用，不能调用 refresh")
	require.Positive(t, atomic.LoadInt32(&privacyClientCalls), "已有 access token 也应该执行提供商信息补全")
}

func TestOpenAIOAuthService_RefreshProviderToken_UsesProviderTLSRouterConfig(t *testing.T) {
	profile := &tlsfingerprint.Profile{Name: "profile-55"}
	profileID := int64(55)
	client := &openaiOAuthClientRefreshStub{}
	deps := &OpenAIAuthorizationDependencies{}
	svc := newOpenAIAuthorizationForTest(t, nil, client, deps)
	svc.Start()
	deps.Routers = &openAIOAuthTokenRouterReaderStub{routers: map[int64]*egress.TLSFingerprintRouter{
		9: {
			ID:                                       9,
			Enabled:                                  true,
			ChatGPTOAuthTokenUserAgent:               " Token UA ",
			ChatGPTOAuthTokenTLSFingerprintProfileID: &profileID,
		},
	}}
	deps.Profiles = &openAIOAuthTokenProfileResolverStub{profiles: map[int64]*tlsfingerprint.Profile{55: profile}}

	provider := &providerauth.Record{
		ID:          77,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Concurrency: 3,
		Credentials: map[string]any{
			"refresh_token": "old-rt",
			"client_id":     "client-id",
		},
		Extra: map[string]any{
			"tls_fingerprint_router_id": int64(9),
		},
	}

	info, err := svc.RefreshProviderToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "new-at", info.AccessToken)
	require.Equal(t, "client-id", client.lastClientID)
	require.Len(t, client.lastOptions, 1)
	require.Equal(t, "Token UA", client.lastOptions[0].UserAgent)
	require.Same(t, profile, client.lastOptions[0].TLSProfile)
	require.Equal(t, int64(77), client.lastOptions[0].ProviderID)
	require.Equal(t, 3, client.lastOptions[0].ProviderConcurrency)
}

func TestOpenAIOAuthService_RefreshProviderToken_EmptyRouterTokenConfigKeepsOldPath(t *testing.T) {
	client := &openaiOAuthClientRefreshStub{}
	deps := &OpenAIAuthorizationDependencies{}
	svc := newOpenAIAuthorizationForTest(t, nil, client, deps)
	svc.Start()
	deps.Routers = &openAIOAuthTokenRouterReaderStub{routers: map[int64]*egress.TLSFingerprintRouter{
		9: {
			ID:      9,
			Enabled: true,
		},
	}}
	deps.Profiles = nil

	provider := &providerauth.Record{
		ID:       77,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "old-rt",
			"client_id":     "client-id",
		},
		Extra: map[string]any{
			"tls_fingerprint_router_id": int64(9),
		},
	}

	_, err := svc.RefreshProviderToken(context.Background(), provider)
	require.NoError(t, err)
	require.Empty(t, client.lastOptions)
}

func TestOpenAIOAuthService_RefreshTokenWithClientIDAndRouter_UsesCodexUAFallbackWhenTLSProfileConfigured(t *testing.T) {
	profileID := int64(0)
	client := &openaiOAuthClientRefreshStub{}
	deps := &OpenAIAuthorizationDependencies{}
	svc := newOpenAIAuthorizationForTest(t, nil, client, deps)
	svc.Start()
	settingService := gateway.NewRuntimeSettings(&openAIOAuthSettingRepoStub{values: map[string]string{
		gateway.SettingKeyOpenAICodexUserAgent: " codex-custom ",
	}}, settings.ErrSettingNotFound, nil)
	deps.Routers = &openAIOAuthTokenRouterReaderStub{routers: map[int64]*egress.TLSFingerprintRouter{
		10: {
			ID:                                       10,
			Enabled:                                  true,
			ChatGPTOAuthTokenTLSFingerprintProfileID: &profileID,
		},
	}}
	deps.Profiles = &openAIOAuthTokenProfileResolverStub{profiles: map[int64]*tlsfingerprint.Profile{
		0: {Name: "built-in"},
	}}
	deps.CodexUserAgent = settingService.GetOpenAICodexUserAgent

	routerID := int64(10)
	_, err := svc.RefreshTokenWithClientIDAndRouter(context.Background(), "rt", "", "client-id", &routerID)
	require.NoError(t, err)
	require.Len(t, client.lastOptions, 1)
	require.Equal(t, "codex-custom", client.lastOptions[0].UserAgent)
	require.Equal(t, "built-in", client.lastOptions[0].TLSProfile.Name)
}

// TestOpenAIPrivacyUsesNativeAccountSettingsEndpoint captures requests built by the production
// authorization options.
func TestOpenAIPrivacyUsesNativeAccountSettingsEndpoint(t *testing.T) {
	client := req.C()
	var requests []*http.Request
	client.Transport.WrapRoundTripFunc(func(http.RoundTripper) req.HttpRoundTripFunc {
		return func(request *http.Request) (*http.Response, error) {
			requests = append(requests, request)
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"training_allowed":false}`)), Request: request}, nil
		}
	})
	options := OpenAIAuthorizationOptions(&OpenAIAuthorizationDependencies{PrivacyFactory: func(string) (*req.Client, error) { return client, nil }})
	require.Equal(t, "training_off", options.DisableTraining(context.Background(), "fixture-token", ""))
	require.Len(t, requests, 1)
	require.Equal(t, http.MethodPatch, requests[0].Method)
	require.Equal(t, "https", requests[0].URL.Scheme)
	require.Equal(t, "chatgpt.com", requests[0].URL.Host)
	require.Equal(t, "/backend-api/settings/account_user_setting", requests[0].URL.Path)
	require.Equal(t, "training_allowed", requests[0].URL.Query().Get("feature"))
	require.Equal(t, "false", requests[0].URL.Query().Get("value"))
}

// TestOpenAIPATUsesNativeAccountEndpoint uses a unique proxy address to isolate the shared client
// cache while checking PAT requests.
func TestOpenAIPATUsesNativeAccountEndpoint(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("unexpected network request")
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	client, err := httpclient.GetClient(httpclient.Options{ProxyURL: proxy.URL, Timeout: 20 * time.Second, ResponseHeaderTimeout: 15 * time.Second})
	require.NoError(t, err)
	transport := client.Transport
	t.Cleanup(func() { client.Transport = transport })
	var requestedURL string
	client.Transport = req.HttpRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestedURL = request.URL.String()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Request: request, Body: io.NopCloser(strings.NewReader(`{"email":"fixture@example.test","chatgpt_user_id":"user-fixture","chatgpt_account_id":"account-fixture","chatgpt_plan_type":"plus","chatgpt_account_is_fedramp":false}`))}, nil
	})
	options := OpenAIAuthorizationOptions(&OpenAIAuthorizationDependencies{})
	info, err := options.ValidatePAT(context.Background(), "at-fixture", proxy.URL)
	require.NoError(t, err)
	require.Equal(t, "https://auth.openai.com/api/accounts/v1/user-auth-credential/whoami", requestedURL)
	require.Equal(t, "account-fixture", info.ChatGPTAccountID)
}

func TestOpenAIOAuthService_GenerateAuthURL_OpenAIKeepsCodexFlow(t *testing.T) {
	svc := newOpenAIAuthorizationForTest(t, nil, &openaiOAuthClientAuthURLStub{})
	svc.Start()
	defer stopOpenAIAuthorizationForTest(t, svc)

	result, err := svc.GenerateAuthURL(context.Background(), nil, "", capability.PlatformOpenAI)
	require.NoError(t, err)
	require.NotEmpty(t, result.AuthURL)
	require.NotEmpty(t, result.SessionID)

	parsed, err := url.Parse(result.AuthURL)
	require.NoError(t, err)
	q := parsed.Query()
	require.Equal(t, upstreamopenai.ClientID, q.Get("client_id"))
	require.Equal(t, "true", q.Get("codex_cli_simplified_flow"))

	session, ok := svc.Sessions.Get(result.SessionID)
	require.True(t, ok)
	require.Equal(t, upstreamopenai.ClientID, session.ClientID)
}

func TestOpenAIOAuthService_ExchangeCode_StateRequired(t *testing.T) {
	client := &openaiOAuthClientStateStub{}
	svc := newOpenAIAuthorizationForTest(t, nil, client)
	svc.Start()
	defer stopOpenAIAuthorizationForTest(t, svc)

	svc.Sessions.Set("sid", &providerauth.OpenAIOAuthSession{
		State:        "expected-state",
		CodeVerifier: "verifier",
		RedirectURI:  upstreamopenai.DefaultRedirectURI,
		CreatedAt:    time.Now(),
	})

	_, err := svc.ExchangeCode(context.Background(), &providerauth.OpenAIExchangeCodeInput{
		SessionID: "sid",
		Code:      "auth-code",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "oauth state is required")
	require.Equal(t, int32(0), atomic.LoadInt32(&client.exchangeCalled))
}

func TestOpenAIOAuthService_ExchangeCode_StateMismatch(t *testing.T) {
	client := &openaiOAuthClientStateStub{}
	svc := newOpenAIAuthorizationForTest(t, nil, client)
	svc.Start()
	defer stopOpenAIAuthorizationForTest(t, svc)

	svc.Sessions.Set("sid", &providerauth.OpenAIOAuthSession{
		State:        "expected-state",
		CodeVerifier: "verifier",
		RedirectURI:  upstreamopenai.DefaultRedirectURI,
		CreatedAt:    time.Now(),
	})

	_, err := svc.ExchangeCode(context.Background(), &providerauth.OpenAIExchangeCodeInput{
		SessionID: "sid",
		Code:      "auth-code",
		State:     "wrong-state",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid oauth state")
	require.Equal(t, int32(0), atomic.LoadInt32(&client.exchangeCalled))
}

func TestOpenAIOAuthService_ExchangeCode_StateMatch(t *testing.T) {
	client := &openaiOAuthClientStateStub{}
	svc := newOpenAIAuthorizationForTest(t, nil, client)
	svc.Start()
	defer stopOpenAIAuthorizationForTest(t, svc)

	svc.Sessions.Set("sid", &providerauth.OpenAIOAuthSession{
		State:        "expected-state",
		CodeVerifier: "verifier",
		RedirectURI:  upstreamopenai.DefaultRedirectURI,
		CreatedAt:    time.Now(),
	})

	info, err := svc.ExchangeCode(context.Background(), &providerauth.OpenAIExchangeCodeInput{
		SessionID: "sid",
		Code:      "auth-code",
		State:     "expected-state",
	})
	require.NoError(t, err)
	require.NotNil(t, info)
	require.Equal(t, "at", info.AccessToken)
	require.Equal(t, upstreamopenai.ClientID, info.ClientID)
	require.Equal(t, upstreamopenai.ClientID, client.lastClientID)
	require.Equal(t, int32(1), atomic.LoadInt32(&client.exchangeCalled))

	_, ok := svc.Sessions.Get("sid")
	require.False(t, ok)
}

func TestOpenAIOAuthService_ExchangeCode_UsesRequestTLSRouterConfig(t *testing.T) {
	profile := &tlsfingerprint.Profile{Name: "exchange-profile"}
	profileID := int64(42)
	client := &openaiOAuthClientStateStub{}
	deps := &OpenAIAuthorizationDependencies{}
	svc := newOpenAIAuthorizationForTest(t, nil, client, deps)
	svc.Start()
	defer stopOpenAIAuthorizationForTest(t, svc)
	deps.Routers = &openAIOAuthTokenRouterReaderStub{routers: map[int64]*egress.TLSFingerprintRouter{
		7: {
			ID:                                       7,
			Enabled:                                  true,
			ChatGPTOAuthTokenUserAgent:               " Exchange UA ",
			ChatGPTOAuthTokenTLSFingerprintProfileID: &profileID,
		},
	}}
	deps.Profiles = &openAIOAuthTokenProfileResolverStub{profiles: map[int64]*tlsfingerprint.Profile{42: profile}}

	svc.Sessions.Set("sid", &providerauth.OpenAIOAuthSession{
		State:        "expected-state",
		CodeVerifier: "verifier",
		RedirectURI:  upstreamopenai.DefaultRedirectURI,
		CreatedAt:    time.Now(),
	})

	routerID := int64(7)
	info, err := svc.ExchangeCode(context.Background(), &providerauth.OpenAIExchangeCodeInput{
		SessionID:              "sid",
		Code:                   "auth-code",
		State:                  "expected-state",
		TLSFingerprintRouterID: &routerID,
	})

	require.NoError(t, err)
	require.NotNil(t, info)
	require.Len(t, client.lastOptions, 1)
	require.Equal(t, "Exchange UA", client.lastOptions[0].UserAgent)
	require.Same(t, profile, client.lastOptions[0].TLSProfile)
}

func TestOpenAIOAuthService_ValidateCodexPersonalAccessToken(t *testing.T) {
	var gotAuthorization string
	var gotOriginator string
	var gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("authorization")
		gotOriginator = r.Header.Get("originator")
		gotUserAgent = r.Header.Get("user-agent")
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"email":"user@example.com",
			"chatgpt_user_id":"user-123",
			"chatgpt_account_id":"acct-123",
			"chatgpt_plan_type":"plus",
			"chatgpt_account_is_fedramp":true
		}`))
	}))
	defer server.Close()

	svc := newOpenAIAuthorizationForTest(t, nil, nil, &OpenAIAuthorizationDependencies{WhoamiURL: server.URL})
	svc.Start()
	defer stopOpenAIAuthorizationForTest(t, svc)

	info, err := svc.Options.ValidatePAT(context.Background(), " at-test-token ", "")
	require.NoError(t, err)
	require.Equal(t, "Bearer at-test-token", gotAuthorization)
	require.Equal(t, upstreamopenai.CodexDefaultOriginator, gotOriginator)
	require.Equal(t, upstreamopenai.CodexCLIUserAgent, gotUserAgent)
	require.Equal(t, providerauth.OpenAIAuthModePersonalAccessToken, info.AuthMode)
	require.Equal(t, "user@example.com", info.Email)
	require.Equal(t, "user-123", info.ChatGPTUserID)
	require.Equal(t, "acct-123", info.ChatGPTAccountID)
	require.Equal(t, "plus", info.PlanType)
	require.True(t, info.ChatGPTAccountFedRAMP)
	require.Zero(t, info.ExpiresAt)
	require.Empty(t, info.RefreshToken)
}

func TestOpenAIOAuthService_ValidateCodexPersonalAccessTokenRequiresATPrefix(t *testing.T) {
	svc := newOpenAIAuthorizationForTest(t, nil, nil)
	svc.Start()
	defer stopOpenAIAuthorizationForTest(t, svc)

	_, err := svc.Options.ValidatePAT(context.Background(), "eyJ.jwt", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "at-")
}

func TestOpenAIOAuthService_BuildProviderCredentialsForPAT(t *testing.T) {
	svc := newOpenAIAuthorizationForTest(t, nil, nil)
	svc.Start()
	defer stopOpenAIAuthorizationForTest(t, svc)

	credentials := providerauth.BuildOpenAIProviderCredentials(&providerauth.OpenAITokenInfo{
		AccessToken:           "at-test-token",
		AuthMode:              providerauth.OpenAIAuthModePersonalAccessToken,
		Email:                 "user@example.com",
		ChatGPTAccountID:      "acct-123",
		ChatGPTUserID:         "user-123",
		ChatGPTAccountFedRAMP: true,
		PlanType:              "plus",
	})

	require.Equal(t, "at-test-token", credentials["access_token"])
	require.Equal(t, providerauth.OpenAIAuthModePersonalAccessToken, credentials["auth_mode"])
	require.Equal(t, "personal_access_token", credentials["openai_auth_mode"])
	require.Equal(t, "Bearer", credentials["token_type"])
	require.Equal(t, true, credentials["chatgpt_account_is_fedramp"])
	require.NotContains(t, credentials, "expires_at")
	require.NotContains(t, credentials, "refresh_token")
	require.NotContains(t, credentials, "id_token")
}

func TestFetchChatGPTSubscriptionExpiresAt(t *testing.T) {
	const wantExpiresAt = "2026-06-10T02:52:15Z"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/backend-api/subscriptions", r.URL.Path)
		require.Equal(t, "acc_123", r.URL.Query().Get("account_id"))
		require.Equal(t, "Bearer access-token", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type":    "plus",
			"active_until": wantExpiresAt,
			"will_renew":   true,
			"id":           "sub_123",
		})
	}))
	defer server.Close()

	client := upstreamopenai.PrivacyClient{Endpoints: upstreamopenai.PrivacyEndpoints{Subscriptions: server.URL + "/backend-api/subscriptions"}}

	got := client.FetchChatGPTSubscriptionExpiresAt(context.Background(), func(proxyURL string) (*req.Client, error) {
		return req.C().SetTimeout(5 * time.Second), nil
	}, "access-token", "", "acc_123")

	require.Equal(t, wantExpiresAt, got)
}

func TestFetchChatGPTAccountInfo_SkipsExpiredWorkspaceCandidate(t *testing.T) {
	expiredAt := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/backend-api/accounts/check/v4-2023-04-27", r.URL.Path)
		require.Equal(t, "Bearer access-token", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accounts": map[string]any{
				"org-expired-workspace": map[string]any{
					"account": map[string]any{
						"plan_type":  "self_serve_business_usage_based",
						"is_default": true,
					},
					"entitlement": map[string]any{
						"expires_at": expiredAt,
					},
				},
				"personal-provider": map[string]any{
					"account": map[string]any{
						"plan_type": "free",
					},
				},
			},
		})
	}))
	defer server.Close()

	client := upstreamopenai.PrivacyClient{Endpoints: upstreamopenai.PrivacyEndpoints{Providers: server.URL + "/backend-api/accounts/check/v4-2023-04-27"}}

	got := client.FetchChatGPTAccountInfo(context.Background(), func(proxyURL string) (*req.Client, error) {
		return req.C().SetTimeout(5 * time.Second), nil
	}, "access-token", "", "org-expired-workspace")

	require.NotNil(t, got)
	require.Equal(t, "free", got.PlanType)
	require.Empty(t, got.SubscriptionExpiresAt)
}

func TestFetchChatGPTAccountInfo_SkipsDeactivatedWorkspaceCandidate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/backend-api/accounts/check/v4-2023-04-27", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accounts": map[string]any{
				"org-deactivated-workspace": map[string]any{
					"account": map[string]any{
						"plan_type":      "self_serve_business_usage_based",
						"is_default":     true,
						"is_deactivated": true,
					},
				},
				"personal-provider": map[string]any{
					"account": map[string]any{
						"plan_type": "pro",
					},
				},
			},
		})
	}))
	defer server.Close()

	client := upstreamopenai.PrivacyClient{Endpoints: upstreamopenai.PrivacyEndpoints{Providers: server.URL + "/backend-api/accounts/check/v4-2023-04-27"}}

	got := client.FetchChatGPTAccountInfo(context.Background(), func(proxyURL string) (*req.Client, error) {
		return req.C().SetTimeout(5 * time.Second), nil
	}, "access-token", "", "org-deactivated-workspace")

	require.NotNil(t, got)
	require.Equal(t, "pro", got.PlanType)
}

func TestFetchChatGPTAccountInfo_ReportsObjectProviderID(t *testing.T) {
	futureAt := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	endpoints := configureChatGPTBackendTestServer(t, chatGPTBackendTestConfig{
		providers: map[string]any{
			"accounts": map[string]any{
				"default": map[string]any{
					"account": map[string]any{
						"account_id": "personal-provider-a",
						"plan_type":  "plus",
						"is_default": true,
					},
					"entitlement": map[string]any{"expires_at": futureAt},
				},
			},
		},
	})

	client := upstreamopenai.PrivacyClient{Endpoints: endpoints}
	got := client.FetchChatGPTAccountInfo(
		context.Background(),
		newLocalPrivacyClientFactory(),
		"access-token",
		"",
		"",
	)
	require.NotNil(t, got)
	require.Equal(t, "plus", got.PlanType)
	require.Equal(t, "personal-provider-a", got.ProviderID)
}

func TestEnrichTokenInfo_WorkspaceExpiryDoesNotOverridePersonalSubscription(t *testing.T) {
	const (
		personalProviderID  = "personal-provider-a"
		workspaceProviderID = "personal-workspace-b"
		personalActiveUntil = "2027-03-01T00:00:00Z"
	)
	workspaceExpiresAt := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	var subscriptionCalls atomic.Int32
	var requestedProviderID atomic.Value
	endpoints := configureChatGPTBackendTestServer(t, chatGPTBackendTestConfig{
		providers: map[string]any{
			"accounts": map[string]any{
				workspaceProviderID: map[string]any{
					"account": map[string]any{
						"account_id": workspaceProviderID,
						"plan_type":  "pro",
						"is_default": true,
					},
					"entitlement": map[string]any{"expires_at": workspaceExpiresAt},
				},
			},
		},
		subscription: func(providerID string) map[string]any {
			subscriptionCalls.Add(1)
			requestedProviderID.Store(providerID)
			return map[string]any{
				"plan_type":    "pro",
				"active_until": personalActiveUntil,
				"will_renew":   true,
			}
		},
	})

	tokenInfo := &providerauth.OpenAITokenInfo{
		AccessToken:      "access-token",
		ChatGPTAccountID: personalProviderID,
		OrganizationID:   workspaceProviderID,
		PlanType:         "pro",
	}
	service := newOpenAIAuthorizationForTest(t, nil, nil, &OpenAIAuthorizationDependencies{PrivacyFactory: newLocalPrivacyClientFactory(), PrivacyEndpoints: endpoints})
	service.EnrichTokenInfo(context.Background(), tokenInfo, "")

	require.Equal(t, "pro", tokenInfo.PlanType)
	require.Equal(t, personalActiveUntil, tokenInfo.SubscriptionExpiresAt)
	require.NotEqual(t, workspaceExpiresAt, tokenInfo.SubscriptionExpiresAt)
	require.Equal(t, int32(1), subscriptionCalls.Load())
	require.Equal(t, personalProviderID, requestedProviderID.Load())
}

func TestEnrichTokenInfo_ProviderMatchKeepsEntitlementExpiry(t *testing.T) {
	const personalProviderID = "personal-provider-a"
	entitlementExpiresAt := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	var subscriptionCalls atomic.Int32
	endpoints := configureChatGPTBackendTestServer(t, chatGPTBackendTestConfig{
		providers: map[string]any{
			"accounts": map[string]any{
				personalProviderID: map[string]any{
					"account": map[string]any{
						"account_id": personalProviderID,
						"plan_type":  "plus",
						"is_default": true,
					},
					"entitlement": map[string]any{"expires_at": entitlementExpiresAt},
				},
			},
		},
		subscription: func(string) map[string]any {
			subscriptionCalls.Add(1)
			return nil
		},
	})

	tokenInfo := &providerauth.OpenAITokenInfo{
		AccessToken:      "access-token",
		ChatGPTAccountID: personalProviderID,
		OrganizationID:   personalProviderID,
		PlanType:         "plus",
	}
	service := newOpenAIAuthorizationForTest(t, nil, nil, &OpenAIAuthorizationDependencies{PrivacyFactory: newLocalPrivacyClientFactory(), PrivacyEndpoints: endpoints})
	service.EnrichTokenInfo(context.Background(), tokenInfo, "")

	require.Equal(t, entitlementExpiresAt, tokenInfo.SubscriptionExpiresAt)
	require.Zero(t, subscriptionCalls.Load())
}

func TestEnrichTokenInfo_WorkspacePlanKeepsItsOwnExpiry(t *testing.T) {
	const workspaceProviderID = "workspace-b"
	workspaceExpiresAt := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	var subscriptionCalls atomic.Int32
	endpoints := configureChatGPTBackendTestServer(t, chatGPTBackendTestConfig{
		providers: map[string]any{
			"accounts": map[string]any{
				workspaceProviderID: map[string]any{
					"account": map[string]any{
						"account_id": workspaceProviderID,
						"plan_type":  "self_serve_business_usage_based",
						"is_default": true,
					},
					"entitlement": map[string]any{"expires_at": workspaceExpiresAt},
				},
			},
		},
		subscription: func(string) map[string]any {
			subscriptionCalls.Add(1)
			return nil
		},
	})

	tokenInfo := &providerauth.OpenAITokenInfo{
		AccessToken:      "access-token",
		ChatGPTAccountID: "personal-provider-a",
		OrganizationID:   workspaceProviderID,
	}
	service := newOpenAIAuthorizationForTest(t, nil, nil, &OpenAIAuthorizationDependencies{PrivacyFactory: newLocalPrivacyClientFactory(), PrivacyEndpoints: endpoints})
	service.EnrichTokenInfo(context.Background(), tokenInfo, "")

	require.Equal(t, "self_serve_business_usage_based", tokenInfo.PlanType)
	require.Equal(t, workspaceExpiresAt, tokenInfo.SubscriptionExpiresAt)
	require.Zero(t, subscriptionCalls.Load())
}

func (s *openaiOAuthClientRefreshStub) ExchangeCode(ctx context.Context, code, codeVerifier, redirectURI, proxyURL, clientID string, options ...upstreamopenai.OAuthTokenRequestOptions) (*upstreamopenai.TokenResponse, error) {
	return nil, errors.New("not implemented")
}

func (s *openaiOAuthClientRefreshStub) RefreshToken(ctx context.Context, refreshToken, proxyURL string, options ...upstreamopenai.OAuthTokenRequestOptions) (*upstreamopenai.TokenResponse, error) {
	atomic.AddInt32(&s.refreshCalls, 1)
	s.lastOptions = append([]upstreamopenai.OAuthTokenRequestOptions(nil), options...)
	return nil, errors.New("not implemented")
}

func (s *openaiOAuthClientRefreshStub) RefreshTokenWithClientID(ctx context.Context, refreshToken, proxyURL string, clientID string, options ...upstreamopenai.OAuthTokenRequestOptions) (*upstreamopenai.TokenResponse, error) {
	atomic.AddInt32(&s.refreshCalls, 1)
	s.lastClientID = clientID
	s.lastOptions = append([]upstreamopenai.OAuthTokenRequestOptions(nil), options...)
	return &upstreamopenai.TokenResponse{AccessToken: "new-at", RefreshToken: "new-rt", ExpiresIn: 3600}, nil
}

func (s *openAIOAuthTokenProfileResolverStub) ResolveTokenTLSProfileByID(id int64) (*tlsfingerprint.Profile, bool) {
	if s == nil {
		return nil, false
	}
	profile, ok := s.profiles[id]
	return profile, ok
}

func (s *openAIOAuthSettingRepoStub) Get(context.Context, string) (*settings.Setting, error) {
	panic("unexpected Get call")
}

func (s *openAIOAuthSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := s.values[key]; ok {
		return value, nil
	}
	return "", settings.ErrSettingNotFound
}

func (s *openAIOAuthSettingRepoStub) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}

func (s *openAIOAuthSettingRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	panic("unexpected GetMultiple call")
}

func (s *openAIOAuthSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple call")
}

func (s *openAIOAuthSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (s *openAIOAuthSettingRepoStub) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

func (s *openaiOAuthClientAuthURLStub) ExchangeCode(ctx context.Context, code, codeVerifier, redirectURI, proxyURL, clientID string, options ...upstreamopenai.OAuthTokenRequestOptions) (*upstreamopenai.TokenResponse, error) {
	return nil, errors.New("not implemented")
}

func (s *openaiOAuthClientAuthURLStub) RefreshToken(ctx context.Context, refreshToken, proxyURL string, options ...upstreamopenai.OAuthTokenRequestOptions) (*upstreamopenai.TokenResponse, error) {
	return nil, errors.New("not implemented")
}

func (s *openaiOAuthClientAuthURLStub) RefreshTokenWithClientID(ctx context.Context, refreshToken, proxyURL string, clientID string, options ...upstreamopenai.OAuthTokenRequestOptions) (*upstreamopenai.TokenResponse, error) {
	return nil, errors.New("not implemented")
}

func (s *openaiOAuthClientStateStub) ExchangeCode(ctx context.Context, code, codeVerifier, redirectURI, proxyURL, clientID string, options ...upstreamopenai.OAuthTokenRequestOptions) (*upstreamopenai.TokenResponse, error) {
	atomic.AddInt32(&s.exchangeCalled, 1)
	s.lastClientID = clientID
	s.lastOptions = append([]upstreamopenai.OAuthTokenRequestOptions(nil), options...)
	return &upstreamopenai.TokenResponse{
		AccessToken:  "at",
		RefreshToken: "rt",
		ExpiresIn:    3600,
	}, nil
}

func (s *openaiOAuthClientStateStub) RefreshToken(ctx context.Context, refreshToken, proxyURL string, options ...upstreamopenai.OAuthTokenRequestOptions) (*upstreamopenai.TokenResponse, error) {
	return nil, errors.New("not implemented")
}

func (s *openaiOAuthClientStateStub) RefreshTokenWithClientID(ctx context.Context, refreshToken, proxyURL string, clientID string, options ...upstreamopenai.OAuthTokenRequestOptions) (*upstreamopenai.TokenResponse, error) {
	return s.RefreshToken(ctx, refreshToken, proxyURL)
}

// configureChatGPTBackendTestServer serves local account, subscription, and privacy endpoints.
func configureChatGPTBackendTestServer(t *testing.T, config chatGPTBackendTestConfig) upstreamopenai.PrivacyEndpoints {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/backend-api/accounts/check/v4-2023-04-27":
			_ = json.NewEncoder(w).Encode(config.providers)
		case "/backend-api/subscriptions":
			body := map[string]any{}
			if config.subscription != nil {
				if result := config.subscription(r.URL.Query().Get("account_id")); result != nil {
					body = result
				}
			}
			_ = json.NewEncoder(w).Encode(body)
		case "/backend-api/settings/account_user_setting":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": false})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	t.Cleanup(server.Close)
	return upstreamopenai.PrivacyEndpoints{
		Providers:     server.URL + "/backend-api/accounts/check/v4-2023-04-27",
		Subscriptions: server.URL + "/backend-api/subscriptions",
		Settings:      server.URL + "/backend-api/settings/account_user_setting",
	}
}

func newLocalPrivacyClientFactory() upstreamopenai.PrivacyClientFactory {
	return func(string) (*req.Client, error) {
		return req.C().SetTimeout(5 * time.Second), nil
	}
}
