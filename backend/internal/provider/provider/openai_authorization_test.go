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

// OpenAI authorization callbacks combine provider credentials with runtime
// TLS-router choices and account metadata endpoints. Exchange stubs capture
// client and transport inputs, while local or intercepted HTTP responses let
// these cases observe PAT identity headers, privacy endpoint selection and
// subscription metadata without depending on upstream account data.
//
// State cases distinguish missing proof, mismatched proof and a successful
// exchange that removes its session. Enrichment cases separate
// personal-account expiry from a workspace observation, reuse matching
// entitlement expiry and adopt a workspace plan with its own expiry when no
// token plan was present. Captured option slices are copied but their profile
// pointers remain borrowed. Fixture maps and capture fields are configured or
// observed serially; request counters use atomics where handlers update them.

// openaiOAuthClientRefreshStub captures token-refresh parameters and supplies
// fixed successful refresh metadata.
type openaiOAuthClientRefreshStub struct {
	refreshCalls int32 // atomic refresh callback invocations
	// lastOptions and lastClientID capture the most recent callback inputs.
	// Options are copied as a slice, profile pointers stay shared, and tests observe
	// these unsynchronized captures after the callback returns.
	lastOptions  []upstreamopenai.OAuthTokenRequestOptions // captured exchange transport options
	lastClientID string                                    // captured OAuth client identity
}

// openAIOAuthTokenProfileResolverStub supplies borrowed TLS profiles from a
// configured ID map.
type openAIOAuthTokenProfileResolverStub struct {
	// profiles supplies borrowed profile pointers by ID, including the zero ID
	// used for a built-in profile fixture. Owners keep this map stable during resolution.
	profiles map[int64]*tlsfingerprint.Profile
}

// openAIOAuthSettingRepoStub supplies single-value runtime settings for
// dynamic Codex UA lookup.
type openAIOAuthSettingRepoStub struct {
	// values supplies raw persisted strings to GetValue. Configure the map before
	// invoking the UA callback; missing keys return the settings reader's sentinel error.
	values map[string]string
}

// openaiOAuthClientAuthURLStub satisfies exchange dependencies for tests that
// only generate authorization URLs.
type openaiOAuthClientAuthURLStub struct{}

// openaiOAuthClientStateStub counts exchanges and captures inputs for
// session-state validation cases.
type openaiOAuthClientStateStub struct {
	exchangeCalled int32 // atomic code exchange invocations
	// lastClientID and lastOptions capture accepted exchange inputs after the
	// service validates state. Slice storage is copied, profiles remain borrowed,
	// and test owners observe these fields after the callback returns.
	lastClientID string                                    // accepted exchange client identity
	lastOptions  []upstreamopenai.OAuthTokenRequestOptions // accepted exchange transport options
}

// chatGPTBackendTestConfig supplies account and subscription responses for
// local privacy endpoint fixtures.
type chatGPTBackendTestConfig struct {
	// providers is the borrowed accounts/check response payload, kept stable while
	// request handlers encode it. Its account IDs name upstream accounts, not numeric provider records.
	providers map[string]any
	// subscription optionally supplies a response for the account_id query string.
	// A nil callback or result emits an empty object; callback observations need their own synchronization.
	subscription func(providerID string) map[string]any
}

// TestOpenAIOAuthService_RefreshProviderToken_NoRefreshTokenUsesExistingAccessToken
// supplies unexpired access-only credentials and a failing privacy client
// factory. Token metadata retains the stored access token and client ID, makes
// no exchange call and still attempts enrichment.
func TestOpenAIOAuthService_RefreshProviderToken_NoRefreshTokenUsesExistingAccessToken(t *testing.T) {
	client := &openaiOAuthClientRefreshStub{}
	deps := &OpenAIAuthorizationDependencies{}
	svc := newOpenAIAuthorizationForTest(t, nil, client, deps)
	svc.Start()
	var privacyClientCalls int32
	// Count enrichment client construction but abort before HTTP, so the assertions
	// distinguish attempted metadata work from a token exchange or completed vendor request.
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

// TestOpenAIOAuthService_RefreshProviderToken_UsesProviderTLSRouterConfig
// supplies a provider-bound router with token UA and TLS profile selections.
// Captured refresh inputs contain the trimmed UA, borrowed profile, client ID
// and provider identity/concurrency.
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

// TestOpenAIOAuthService_RefreshProviderToken_EmptyRouterTokenConfigKeepsOldPath
// selects an enabled router with no token UA or profile configuration. Refresh
// succeeds and the client captures an empty transport-option slice.
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

// TestOpenAIOAuthService_RefreshTokenWithClientIDAndRouter_UsesCodexUAFallbackWhenTLSProfileConfigured
// selects a profile through an ID pointer containing zero and leaves router UA
// blank. The captured option carries the built-in fixture profile and the
// trimmed dynamic Codex UA.
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

// TestOpenAIPrivacyUsesNativeAccountSettingsEndpoint intercepts the production
// privacy callback's request and supplies a training-disabled response.
// Assertions protect the PATCH method, account-user-setting URL, query
// feature/value and reported training_off result.
func TestOpenAIPrivacyUsesNativeAccountSettingsEndpoint(t *testing.T) {
	client := req.C()
	var requests []*http.Request
	// Intercept requests before network dispatch and return one known privacy state,
	// keeping URL and method assertions independent of a live upstream account response.
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

// TestOpenAIPATUsesNativeAccountEndpoint obtains a proxy-selected shared
// client and intercepts its transport for PAT validation. The captured URL
// matches the production whoami endpoint, and the returned metadata carries
// the supplied account identity.
func TestOpenAIPATUsesNativeAccountEndpoint(t *testing.T) {
	// Obtain a proxy-selected cached client, then intercept its transport below;
	// the proxy handler fails the case if validation unexpectedly reaches network dispatch.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("unexpected network request")
		w.WriteHeader(http.StatusBadGateway)
	}))
	// The endpoint-selection fixture owns this proxy server and closes it on return,
	// while test cleanup restores the separately borrowed client's intercepted transport.
	defer proxy.Close()
	client, err := httpclient.GetClient(httpclient.Options{ProxyURL: proxy.URL, Timeout: 20 * time.Second, ResponseHeaderTimeout: 15 * time.Second})
	require.NoError(t, err)
	transport := client.Transport
	// Restore the cached client's transport after intercepted validation, so later
	// consumers of that selected client recover its saved request handling instead of the fixture.
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

// TestOpenAIOAuthService_GenerateAuthURL_OpenAIKeepsCodexFlow generates an
// authorization URL through production callback bindings. Its client ID and
// Codex flow query agree with the client identity retained in the created
// session.
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

// TestOpenAIOAuthService_ExchangeCode_StateRequired supplies a stored PKCE
// session while omitting submitted state. Authorization rejects the missing
// proof and the exchange fixture records zero upstream invocations.
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

// TestOpenAIOAuthService_ExchangeCode_StateMismatch supplies submitted state
// different from the stored session proof. Authorization returns the
// invalid-state error and the exchange fixture records zero upstream
// invocations.
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

// TestOpenAIOAuthService_ExchangeCode_StateMatch supplies matching state for a
// session with no stored client ID. One exchange returns token metadata using
// the default client, and a later session lookup finds the entry removed.
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

// TestOpenAIOAuthService_ExchangeCode_UsesRequestTLSRouterConfig supplies an
// exchange router ID with UA and profile selections. Successful exchange
// captures the trimmed router UA and the same configured profile pointer.
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

// TestOpenAIOAuthService_ValidateCodexPersonalAccessToken serves whoami
// metadata for a padded PAT input. Assertions protect the trimmed Bearer
// token, canonical Codex identity headers, account metadata and absence of
// OAuth expiry or refresh state.
func TestOpenAIOAuthService_ValidateCodexPersonalAccessToken(t *testing.T) {
	var gotAuthorization string
	var gotOriginator string
	var gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("authorization")
		gotOriginator = r.Header.Get("originator")
		gotUserAgent = r.Header.Get("user-agent")
		w.Header().Set("content-type", "application/json")
		// The client checks required whoami metadata after this fixed response is
		// written. Discard the write result and assess the body through PAT validation.
		_, _ = w.Write([]byte(`{
			"email":"user@example.com",
			"chatgpt_user_id":"user-123",
			"chatgpt_account_id":"acct-123",
			"chatgpt_plan_type":"plus",
			"chatgpt_account_is_fedramp":true
		}`))
	}))
	// The local whoami server owns its request handlers for this validation call;
	// close it on return after the client has consumed the asserted metadata response.
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

// TestOpenAIOAuthService_ValidateCodexPersonalAccessTokenRequiresATPrefix
// supplies a JWT-like value through the production PAT callback. Validation
// rejects its missing at- prefix, protecting the selected PAT input format.
func TestOpenAIOAuthService_ValidateCodexPersonalAccessTokenRequiresATPrefix(t *testing.T) {
	svc := newOpenAIAuthorizationForTest(t, nil, nil)
	svc.Start()
	defer stopOpenAIAuthorizationForTest(t, svc)

	_, err := svc.Options.ValidatePAT(context.Background(), "eyJ.jwt", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "at-")
}

// TestOpenAIOAuthService_BuildProviderCredentialsForPAT builds credential
// updates from canonical PAT metadata. Authentication markers, Bearer type and
// FedRAMP state are present while OAuth expiry, refresh-token and ID-token
// fields are absent.
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

// TestFetchChatGPTSubscriptionExpiresAt serves a local subscription response
// after checking the requested account selector and Bearer token. The returned
// expiry equals active_until from that response, preserving the upstream
// timestamp text.
func TestFetchChatGPTSubscriptionExpiresAt(t *testing.T) {
	const wantExpiresAt = "2026-06-10T02:52:15Z"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/backend-api/subscriptions", r.URL.Path)
		require.Equal(t, "acc_123", r.URL.Query().Get("account_id"))
		require.Equal(t, "Bearer access-token", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		// Return the fixed subscription fields for the client's expiry assertion.
		// Encoding errors are discarded; the test observes the decoded expiry result.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type":    "plus",
			"active_until": wantExpiresAt,
			"will_renew":   true,
			"id":           "sub_123",
		})
	}))
	// Subscription lookup consumes the fixture's response before this function returns;
	// close its server afterward so endpoint handlers and sockets remain test-owned.
	defer server.Close()

	client := upstreamopenai.PrivacyClient{Endpoints: upstreamopenai.PrivacyEndpoints{Subscriptions: server.URL + "/backend-api/subscriptions"}}

	got := client.FetchChatGPTSubscriptionExpiresAt(context.Background(), func(proxyURL string) (*req.Client, error) {
		return req.C().SetTimeout(5 * time.Second), nil
	}, "access-token", "", "acc_123")

	require.Equal(t, wantExpiresAt, got)
}

// TestFetchChatGPTAccountInfo_SkipsExpiredWorkspaceCandidate serves an expired
// default workspace alongside a personal free account. Lookup returns the
// personal plan with empty subscription expiry, protecting exclusion of the
// expired candidate.
func TestFetchChatGPTAccountInfo_SkipsExpiredWorkspaceCandidate(t *testing.T) {
	expiredAt := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/backend-api/accounts/check/v4-2023-04-27", r.URL.Path)
		require.Equal(t, "Bearer access-token", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		// Discard encoding errors for this configured candidate response; the test
		// checks the selected personal plan and empty expiry after excluding the
		// expired workspace.
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
	// Keep the server alive through candidate selection, then close its handlers
	// and sockets after the client's account metadata observation has completed.
	defer server.Close()

	client := upstreamopenai.PrivacyClient{Endpoints: upstreamopenai.PrivacyEndpoints{Providers: server.URL + "/backend-api/accounts/check/v4-2023-04-27"}}

	got := client.FetchChatGPTAccountInfo(context.Background(), func(proxyURL string) (*req.Client, error) {
		return req.C().SetTimeout(5 * time.Second), nil
	}, "access-token", "", "org-expired-workspace")

	require.NotNil(t, got)
	require.Equal(t, "free", got.PlanType)
	require.Empty(t, got.SubscriptionExpiresAt)
}

// TestFetchChatGPTAccountInfo_SkipsDeactivatedWorkspaceCandidate serves a
// deactivated default workspace alongside a personal pro account. Lookup
// returns the active personal plan, protecting rejection of the deactivated
// candidate.
func TestFetchChatGPTAccountInfo_SkipsDeactivatedWorkspaceCandidate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/backend-api/accounts/check/v4-2023-04-27", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		// Encoding errors are discarded because this case evaluates the selected plan
		// from the response after excluding the deactivated workspace candidate.
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
	// Account selection reads this configured response before cleanup; closing the
	// server at function return ends the fixture's handler and connection lifetime.
	defer server.Close()

	client := upstreamopenai.PrivacyClient{Endpoints: upstreamopenai.PrivacyEndpoints{Providers: server.URL + "/backend-api/accounts/check/v4-2023-04-27"}}

	got := client.FetchChatGPTAccountInfo(context.Background(), func(proxyURL string) (*req.Client, error) {
		return req.C().SetTimeout(5 * time.Second), nil
	}, "access-token", "", "org-deactivated-workspace")

	require.NotNil(t, got)
	require.Equal(t, "pro", got.PlanType)
}

// TestFetchChatGPTAccountInfo_ReportsObjectProviderID serves an account whose
// object key is default but whose account_id names a personal account. Lookup
// returns that inner identity with its plan, keeping object keys distinct from
// account identifiers.
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

// TestEnrichTokenInfo_WorkspaceExpiryDoesNotOverridePersonalSubscription
// retains a personal pro plan while account lookup selects a different pro
// workspace. Enrichment obtains personal subscription expiry through one
// lookup using the personal account ID, replacing no plan metadata.
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

// TestEnrichTokenInfo_ProviderMatchKeepsEntitlementExpiry supplies matching
// personal identities and an account entitlement expiry. Enrichment retains
// that expiry and the subscription callback remains uncalled, protecting reuse
// of matching account metadata.
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

// TestEnrichTokenInfo_WorkspacePlanKeepsItsOwnExpiry omits token plan metadata
// while account lookup selects a workspace with entitlement expiry. Enrichment
// adopts that workspace plan and expiry together and makes no subscription
// lookup.
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

// ExchangeCode returns the fixed unsupported-operation error for this refresh
// fixture. These tests use refresh callbacks, so a code-exchange call receives
// failure rather than any configured token metadata.
func (s *openaiOAuthClientRefreshStub) ExchangeCode(ctx context.Context, code, codeVerifier, redirectURI, proxyURL, clientID string, options ...upstreamopenai.OAuthTokenRequestOptions) (*upstreamopenai.TokenResponse, error) {
	return nil, errors.New("not implemented")
}

// RefreshToken counts a logical invocation and captures a copied option slice
// before returning the unsupported-operation error. Profile pointers remain
// shared; the successful refresh fixture path uses RefreshTokenWithClientID.
func (s *openaiOAuthClientRefreshStub) RefreshToken(ctx context.Context, refreshToken, proxyURL string, options ...upstreamopenai.OAuthTokenRequestOptions) (*upstreamopenai.TokenResponse, error) {
	atomic.AddInt32(&s.refreshCalls, 1)
	s.lastOptions = append([]upstreamopenai.OAuthTokenRequestOptions(nil), options...)
	return nil, errors.New("not implemented")
}

// RefreshTokenWithClientID counts a logical invocation, captures client ID and
// copies the supplied option slice before returning fixed token metadata.
// Profile pointers remain borrowed, and tests serialize capture-field reads
// with callback completion.
func (s *openaiOAuthClientRefreshStub) RefreshTokenWithClientID(ctx context.Context, refreshToken, proxyURL string, clientID string, options ...upstreamopenai.OAuthTokenRequestOptions) (*upstreamopenai.TokenResponse, error) {
	atomic.AddInt32(&s.refreshCalls, 1)
	s.lastClientID = clientID
	s.lastOptions = append([]upstreamopenai.OAuthTokenRequestOptions(nil), options...)
	return &upstreamopenai.TokenResponse{AccessToken: "new-at", RefreshToken: "new-rt", ExpiresIn: 3600}, nil
}

// ResolveTokenTLSProfileByID returns the configured profile pointer and
// membership flag, or nil and false for a nil receiver. The map and profiles
// remain borrowed, and test owners keep them stable while callback options are
// resolved.
func (s *openAIOAuthTokenProfileResolverStub) ResolveTokenTLSProfileByID(id int64) (*tlsfingerprint.Profile, bool) {
	if s == nil {
		return nil, false
	}
	profile, ok := s.profiles[id]
	return profile, ok
}

// Get panics because this runtime-UA fixture supports single-value reads
// through GetValue. Reaching entity lookup is an unexpected call shape and
// fails the test at that access point.
func (s *openAIOAuthSettingRepoStub) Get(context.Context, string) (*settings.Setting, error) {
	panic("unexpected Get call")
}

// GetValue returns the configured raw setting string or
// settings.ErrSettingNotFound when its key is absent. Context is ignored, and
// the runtime setting consumer owns trimming and its missing-value default.
func (s *openAIOAuthSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := s.values[key]; ok {
		return value, nil
	}
	return "", settings.ErrSettingNotFound
}

// Set panics for writes through this runtime-UA read fixture. Configuration is
// installed directly in values before use, so a consumer attempting
// persistence reaches this deliberate test failure.
func (s *openAIOAuthSettingRepoStub) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}

// GetMultiple panics when a consumer requests batched lookup from this
// single-value UA fixture. Supported reads use GetValue, and this method makes
// an unexpected wider read fail immediately.
func (s *openAIOAuthSettingRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	panic("unexpected GetMultiple call")
}

// SetMultiple panics for batch persistence through this configured
// setting-reader fixture. Test values are installed before callback use, so
// attempted runtime writes produce an immediate unexpected-call failure.
func (s *openAIOAuthSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple call")
}

// GetAll panics when a consumer attempts complete settings enumeration in the
// UA fixture. The tested lookup uses GetValue for its selected key, keeping
// this unsupported method observable as failure.
func (s *openAIOAuthSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

// Delete panics for setting removal through this runtime-UA fixture. The
// configured value map is read by GetValue, and the tests do not provide a
// persistence operation for consumers.
func (s *openAIOAuthSettingRepoStub) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

// ExchangeCode returns an unsupported-operation error for the URL-generation
// fixture. Generating PKCE parameters and a stored session requires no
// exchange, so any attempted token call receives this fixed failure.
func (s *openaiOAuthClientAuthURLStub) ExchangeCode(ctx context.Context, code, codeVerifier, redirectURI, proxyURL, clientID string, options ...upstreamopenai.OAuthTokenRequestOptions) (*upstreamopenai.TokenResponse, error) {
	return nil, errors.New("not implemented")
}

// RefreshToken returns an unsupported-operation error for the URL-generation
// fixture. Its tests construct authorization URLs and session state, leaving
// OAuth renewal outside this stub's supported behavior.
func (s *openaiOAuthClientAuthURLStub) RefreshToken(ctx context.Context, refreshToken, proxyURL string, options ...upstreamopenai.OAuthTokenRequestOptions) (*upstreamopenai.TokenResponse, error) {
	return nil, errors.New("not implemented")
}

// RefreshTokenWithClientID returns an unsupported-operation error for this
// authorization-URL fixture regardless of client identity. Its consumers
// generate and inspect session parameters, so no refresh metadata is
// configured here.
func (s *openaiOAuthClientAuthURLStub) RefreshTokenWithClientID(ctx context.Context, refreshToken, proxyURL string, clientID string, options ...upstreamopenai.OAuthTokenRequestOptions) (*upstreamopenai.TokenResponse, error) {
	return nil, errors.New("not implemented")
}

// ExchangeCode counts a logical invocation, captures client identity and
// copies options before returning fixed successful token metadata. State
// validation is performed by the service before calling this stub; captured
// profile pointers remain shared.
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

// RefreshToken returns the unsupported-operation error for this code-exchange
// fixture. Only ExchangeCode has a configured success result, so attempting
// renewal fails without producing token metadata or exchange counts.
func (s *openaiOAuthClientStateStub) RefreshToken(ctx context.Context, refreshToken, proxyURL string, options ...upstreamopenai.OAuthTokenRequestOptions) (*upstreamopenai.TokenResponse, error) {
	return nil, errors.New("not implemented")
}

// RefreshTokenWithClientID delegates to the fixture's unsupported RefreshToken
// method, discarding client ID and options. It returns the same renewal error
// and leaves the exchange capture fields unchanged.
func (s *openaiOAuthClientStateStub) RefreshTokenWithClientID(ctx context.Context, refreshToken, proxyURL string, clientID string, options ...upstreamopenai.OAuthTokenRequestOptions) (*upstreamopenai.TokenResponse, error) {
	return s.RefreshToken(ctx, refreshToken, proxyURL)
}

// configureChatGPTBackendTestServer returns local account, subscription and
// training endpoints and registers server closure with t.Cleanup. The account
// map is borrowed, and callers keep it stable for the server's request
// handlers.
//
// Subscription requests pass the account_id query value to the optional
// callback; absence or a nil result yields an empty object. Training requests
// return the fixed false value, and other paths return 404. Callback-owned
// observations must handle concurrent server requests themselves.
func configureChatGPTBackendTestServer(t *testing.T, config chatGPTBackendTestConfig) upstreamopenai.PrivacyEndpoints {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/backend-api/accounts/check/v4-2023-04-27":
			// Send the configured account observation for the enrichment tests. Encoding
			// errors are discarded, leaving each consumer to check its resulting metadata.
			_ = json.NewEncoder(w).Encode(config.providers)
		case "/backend-api/subscriptions":
			// Missing subscription fixture data yields a successful empty response,
			// letting enrichment keep any earlier expiry rather than treating this route as absent.
			body := map[string]any{}
			if config.subscription != nil {
				if result := config.subscription(r.URL.Query().Get("account_id")); result != nil {
					body = result
				}
			}
			// Send the selected subscription data for the client's expiry lookup. Discard
			// encoding errors because these callers assess the returned subscription metadata.
			_ = json.NewEncoder(w).Encode(body)
		case "/backend-api/settings/account_user_setting":
			// Provide the training-setting response for enrichment's final optional step.
			// Its encode error is ignored because these consumers assert account and
			// subscription metadata, leaving the training result unasserted.
			_ = json.NewEncoder(w).Encode(map[string]any{"value": false})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	// Close the local server through test cleanup after consumers finish their
	// endpoint calls, so retained handlers and sockets do not outlive this fixture.
	t.Cleanup(server.Close)
	return upstreamopenai.PrivacyEndpoints{
		Providers:     server.URL + "/backend-api/accounts/check/v4-2023-04-27",
		Subscriptions: server.URL + "/backend-api/subscriptions",
		Settings:      server.URL + "/backend-api/settings/account_user_setting",
	}
}

// newLocalPrivacyClientFactory returns a callback creating a fresh request
// client with a five-second timeout for local endpoint fixtures. The proxy
// argument is ignored, so tests provide their desired endpoint overrides
// separately.
func newLocalPrivacyClientFactory() upstreamopenai.PrivacyClientFactory {
	return func(string) (*req.Client, error) {
		return req.C().SetTimeout(5 * time.Second), nil
	}
}
