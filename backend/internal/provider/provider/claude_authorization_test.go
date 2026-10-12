package provider

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic/oauth"
)

// Claude full OAuth and inference-scoped setup-token sessions select different
// exchange flags while retaining their PKCE and proxy data. These cases
// exercise claude_authorization.go's option bindings through the core service,
// checking session creation/consumption, token metadata, refresh input and
// optional organization information with per-test clients and proxy lookup
// fixtures.

// mockClaudeOAuthClient delegates Claude authorization calls to configured
// per-test functions.
type mockClaudeOAuthClient struct {
	// getOrgUUIDFunc supplies the session-key organization lookup when
	// configured. A nil callback makes GetOrganizationUUID panic, exposing an
	// unexpected client operation.
	getOrgUUIDFunc func(ctx context.Context, sessionKey, proxyURL string) (string, error)
	// getAuthCodeFunc supplies browser authorization-code acquisition with its
	// session, scope and PKCE inputs. A nil callback makes that client
	// operation panic.
	getAuthCodeFunc func(ctx context.Context, sessionKey, orgUUID, scope, codeChallenge, state, proxyURL string) (string, error)
	// exchangeCodeFunc supplies token exchange and receives the setup-token
	// flag. A nil callback makes exchange panic rather than returning a
	// fabricated token response.
	exchangeCodeFunc func(ctx context.Context, code, codeVerifier, state, proxyURL string, isSetupToken bool) (*oauth.TokenResponse, error)
	// refreshTokenFunc supplies token refresh with the selected proxy URL. A
	// nil callback makes refresh panic, so callers configure the exercised
	// exchange path.
	refreshTokenFunc func(ctx context.Context, refreshToken, proxyURL string) (*oauth.TokenResponse, error)
}

// mockProxyRepoForOAuth supports exact configured lookup and rejects unused
// repository operations.
type mockProxyRepoForOAuth struct {
	// getByIDFunc supplies exact proxy lookup when configured. A nil callback
	// returns the fixture not-found error; other repository operations remain
	// unsupported.
	getByIDFunc func(ctx context.Context, id int64) (*egress.Proxy, error)
}

// TestNewOAuthService constructs a core with a failing proxy fixture and a
// specific client. It checks resolver invocation, client retention, automatic
// session storage and successful explicit startup/shutdown of that instance.
func TestNewOAuthService(t *testing.T) {
	t.Parallel()

	proxyCalls := 0
	proxyRepo := &mockProxyRepoForOAuth{getByIDFunc: func(context.Context, int64) (*egress.Proxy, error) {
		proxyCalls++
		return nil, fmt.Errorf("proxy not found")
	}}
	client := &mockClaudeOAuthClient{}
	svc := newClaudeAuthorizationForTest(proxyRepo, client)
	svc.Start()

	if svc == nil {
		t.Fatal("NewOAuthService 返回 nil")
	}
	if _, ok := svc.Options.ResolveProxy(context.Background(), 1); ok {
		t.Fatal("proxyRepo 未正确设置")
	}
	if proxyCalls != 1 {
		t.Fatal("proxyRepo 未被调用")
	}
	if svc.Client != client {
		t.Fatal("oauthClient 未正确设置")
	}
	if svc.Store == nil {
		t.Fatal("sessionStore 应被自动初始化")
	}

	stopClaudeAuthorization(t, svc)
}

// TestOAuthService_GenerateAuthURL generates a browser URL without a selected
// proxy. It checks nonempty URL/session fields and a stored session carrying
// full OAuth scope for subsequent token exchange.
func TestOAuthService_GenerateAuthURL(t *testing.T) {
	t.Parallel()

	svc := newClaudeAuthorizationForTest(&mockProxyRepoForOAuth{}, &mockClaudeOAuthClient{})
	svc.Start()
	defer stopClaudeAuthorization(t, svc)

	result, err := svc.GenerateAuthURL(context.Background(), nil)
	if err != nil {
		t.Fatalf("GenerateAuthURL 返回错误: %v", err)
	}
	if result == nil {
		t.Fatal("GenerateAuthURL 返回 nil")
	}
	if result.AuthURL == "" {
		t.Fatal("AuthURL 为空")
	}
	if result.SessionID == "" {
		t.Fatal("SessionID 为空")
	}

	session, ok := svc.Store.Get(result.SessionID)
	if !ok {
		t.Fatal("session 未在 sessionStore 中找到")
	}
	if session.Scope != oauth.ScopeOAuth {
		t.Fatalf("scope 不匹配: got=%q want=%q", session.Scope, oauth.ScopeOAuth)
	}
}

// TestOAuthService_GenerateAuthURL_WithProxy generates a session with a loaded
// HTTP proxy selection. It checks that the core stores the resolved proxy URL
// together with the newly created authorization session.
func TestOAuthService_GenerateAuthURL_WithProxy(t *testing.T) {
	t.Parallel()

	proxyRepo := &mockProxyRepoForOAuth{
		getByIDFunc: func(ctx context.Context, id int64) (*egress.Proxy, error) {
			return &egress.Proxy{
				ID:       1,
				Protocol: "http",
				Host:     "proxy.example.com",
				Port:     8080,
			}, nil
		},
	}
	svc := newClaudeAuthorizationForTest(proxyRepo, &mockClaudeOAuthClient{})
	svc.Start()
	defer stopClaudeAuthorization(t, svc)

	proxyID := int64(1)
	result, err := svc.GenerateAuthURL(context.Background(), &proxyID)
	if err != nil {
		t.Fatalf("GenerateAuthURL 返回错误: %v", err)
	}

	session, ok := svc.Store.Get(result.SessionID)
	if !ok {
		t.Fatal("session 未在 sessionStore 中找到")
	}
	if session.ProxyURL != "http://proxy.example.com:8080" {
		t.Fatalf("ProxyURL 不匹配: got=%q", session.ProxyURL)
	}
}

// TestOAuthService_GenerateSetupTokenURL generates an inference-scoped browser
// session. It checks a nonnil URL result and the stored inference scope that
// selects setup-token behavior during the later exchange.
func TestOAuthService_GenerateSetupTokenURL(t *testing.T) {
	t.Parallel()

	svc := newClaudeAuthorizationForTest(&mockProxyRepoForOAuth{}, &mockClaudeOAuthClient{})
	svc.Start()
	defer stopClaudeAuthorization(t, svc)

	result, err := svc.GenerateSetupTokenURL(context.Background(), nil)
	if err != nil {
		t.Fatalf("GenerateSetupTokenURL 返回错误: %v", err)
	}
	if result == nil {
		t.Fatal("GenerateSetupTokenURL 返回 nil")
	}

	session, ok := svc.Store.Get(result.SessionID)
	if !ok {
		t.Fatal("session 未在 sessionStore 中找到")
	}
	if session.Scope != oauth.ScopeInference {
		t.Fatalf("scope 不匹配: got=%q want=%q", session.Scope, oauth.ScopeInference)
	}
}

// TestOAuthService_ExchangeCode_SessionNotFound supplies code for an unknown
// session. It checks the exact missing-or-expired session diagnostic rather
// than invoking an unconfigured exchange client for that input.
func TestOAuthService_ExchangeCode_SessionNotFound(t *testing.T) {
	t.Parallel()

	svc := newClaudeAuthorizationForTest(&mockProxyRepoForOAuth{}, &mockClaudeOAuthClient{})
	svc.Start()
	defer stopClaudeAuthorization(t, svc)

	_, err := svc.ExchangeCode(context.Background(), &providercore.ClaudeExchangeCodeInput{
		SessionID: "nonexistent-session",
		Code:      "test-code",
	})
	if err == nil {
		t.Fatal("ExchangeCode 应返回错误（session 不存在）")
	}
	if err.Error() != "session not found or expired" {
		t.Fatalf("错误信息不匹配: got=%q", err.Error())
	}
}

// TestOAuthService_ExchangeCode_Success exchanges a generated full OAuth
// session. It checks the ordinary exchange flag, token/lifetime and
// organization/user fields, then confirms successful exchange removes the
// stored one-time session.
func TestOAuthService_ExchangeCode_Success(t *testing.T) {
	t.Parallel()

	exchangeCalled := false
	client := &mockClaudeOAuthClient{
		exchangeCodeFunc: func(ctx context.Context, code, codeVerifier, state, proxyURL string, isSetupToken bool) (*oauth.TokenResponse, error) {
			exchangeCalled = true
			if code != "auth-code-123" {
				t.Errorf("code 不匹配: got=%q", code)
			}
			if isSetupToken {
				t.Error("isSetupToken 应为 false（ScopeOAuth）")
			}
			return &oauth.TokenResponse{
				AccessToken:  "access-token-abc",
				TokenType:    "Bearer",
				ExpiresIn:    3600,
				RefreshToken: "refresh-token-xyz",
				Scope:        oauth.ScopeOAuth,
				Organization: &oauth.OrgInfo{UUID: "org-uuid-111"},
				Provider:     &oauth.ProviderInfo{UUID: "acc-uuid-222", EmailAddress: "test@example.com"},
			}, nil
		},
	}

	svc := newClaudeAuthorizationForTest(&mockProxyRepoForOAuth{}, client)
	svc.Start()
	defer stopClaudeAuthorization(t, svc)

	// Creating the authorization URL also creates the session required for
	// exchange.
	result, err := svc.GenerateAuthURL(context.Background(), nil)
	if err != nil {
		t.Fatalf("GenerateAuthURL 返回错误: %v", err)
	}

	tokenInfo, err := svc.ExchangeCode(context.Background(), &providercore.ClaudeExchangeCodeInput{
		SessionID: result.SessionID,
		Code:      "auth-code-123",
	})
	if err != nil {
		t.Fatalf("ExchangeCode 返回错误: %v", err)
	}

	if !exchangeCalled {
		t.Fatal("ExchangeCodeForToken 未被调用")
	}
	if tokenInfo.AccessToken != "access-token-abc" {
		t.Fatalf("AccessToken 不匹配: got=%q", tokenInfo.AccessToken)
	}
	if tokenInfo.TokenType != "Bearer" {
		t.Fatalf("TokenType 不匹配: got=%q", tokenInfo.TokenType)
	}
	if tokenInfo.RefreshToken != "refresh-token-xyz" {
		t.Fatalf("RefreshToken 不匹配: got=%q", tokenInfo.RefreshToken)
	}
	if tokenInfo.OrgUUID != "org-uuid-111" {
		t.Fatalf("OrgUUID 不匹配: got=%q", tokenInfo.OrgUUID)
	}
	if tokenInfo.AccountUUID != "acc-uuid-222" {
		t.Fatalf("AccountUUID 不匹配: got=%q", tokenInfo.AccountUUID)
	}
	if tokenInfo.EmailAddress != "test@example.com" {
		t.Fatalf("EmailAddress 不匹配: got=%q", tokenInfo.EmailAddress)
	}
	if tokenInfo.ExpiresIn != 3600 {
		t.Fatalf("ExpiresIn 不匹配: got=%d", tokenInfo.ExpiresIn)
	}
	if tokenInfo.ExpiresAt == 0 {
		t.Fatal("ExpiresAt 不应为 0")
	}

	_, ok := svc.Store.Get(result.SessionID)
	if ok {
		t.Fatal("session 应在交换成功后被删除")
	}
}

// TestOAuthService_ExchangeCode_SetupToken exchanges an inference-scoped
// session. It checks the client receives setup-token selection and that the
// returned access credential matches the configured setup-token response.
func TestOAuthService_ExchangeCode_SetupToken(t *testing.T) {
	t.Parallel()

	client := &mockClaudeOAuthClient{
		exchangeCodeFunc: func(ctx context.Context, code, codeVerifier, state, proxyURL string, isSetupToken bool) (*oauth.TokenResponse, error) {
			if !isSetupToken {
				t.Error("isSetupToken 应为 true（ScopeInference）")
			}
			return &oauth.TokenResponse{
				AccessToken: "setup-token",
				TokenType:   "Bearer",
				ExpiresIn:   3600,
				Scope:       oauth.ScopeInference,
			}, nil
		},
	}

	svc := newClaudeAuthorizationForTest(&mockProxyRepoForOAuth{}, client)
	svc.Start()
	defer stopClaudeAuthorization(t, svc)

	// Setup tokens request inference scope.
	result, err := svc.GenerateSetupTokenURL(context.Background(), nil)
	if err != nil {
		t.Fatalf("GenerateSetupTokenURL 返回错误: %v", err)
	}

	tokenInfo, err := svc.ExchangeCode(context.Background(), &providercore.ClaudeExchangeCodeInput{
		SessionID: result.SessionID,
		Code:      "setup-code",
	})
	if err != nil {
		t.Fatalf("ExchangeCode 返回错误: %v", err)
	}
	if tokenInfo.AccessToken != "setup-token" {
		t.Fatalf("AccessToken 不匹配: got=%q", tokenInfo.AccessToken)
	}
}

// TestOAuthService_ExchangeCode_ClientError returns an invalid-code error from
// the configured exchange client. It checks that the core propagates that
// exact upstream diagnostic after a valid session is supplied.
func TestOAuthService_ExchangeCode_ClientError(t *testing.T) {
	t.Parallel()

	client := &mockClaudeOAuthClient{
		exchangeCodeFunc: func(ctx context.Context, code, codeVerifier, state, proxyURL string, isSetupToken bool) (*oauth.TokenResponse, error) {
			return nil, fmt.Errorf("upstream error: invalid code")
		},
	}

	svc := newClaudeAuthorizationForTest(&mockProxyRepoForOAuth{}, client)
	svc.Start()
	defer stopClaudeAuthorization(t, svc)

	result, _ := svc.GenerateAuthURL(context.Background(), nil)
	_, err := svc.ExchangeCode(context.Background(), &providercore.ClaudeExchangeCodeInput{
		SessionID: result.SessionID,
		Code:      "bad-code",
	})
	if err == nil {
		t.Fatal("ExchangeCode 应返回错误")
	}
	if err.Error() != "upstream error: invalid code" {
		t.Fatalf("错误信息不匹配: got=%q", err.Error())
	}
}

// TestOAuthService_RefreshToken refreshes an explicitly supplied token with no
// proxy. It checks the client input, rotated access/refresh values, returned
// lifetime and a nonzero computed expiry on the converted response.
func TestOAuthService_RefreshToken(t *testing.T) {
	t.Parallel()

	client := &mockClaudeOAuthClient{
		refreshTokenFunc: func(ctx context.Context, refreshToken, proxyURL string) (*oauth.TokenResponse, error) {
			if refreshToken != "my-refresh-token" {
				t.Errorf("refreshToken 不匹配: got=%q", refreshToken)
			}
			if proxyURL != "" {
				t.Errorf("proxyURL 应为空: got=%q", proxyURL)
			}
			return &oauth.TokenResponse{
				AccessToken:  "new-access-token",
				TokenType:    "Bearer",
				ExpiresIn:    7200,
				RefreshToken: "new-refresh-token",
				Scope:        oauth.ScopeOAuth,
			}, nil
		},
	}

	svc := newClaudeAuthorizationForTest(&mockProxyRepoForOAuth{}, client)
	svc.Start()
	defer stopClaudeAuthorization(t, svc)

	tokenInfo, err := svc.RefreshToken(context.Background(), "my-refresh-token", "")
	if err != nil {
		t.Fatalf("RefreshToken 返回错误: %v", err)
	}
	if tokenInfo.AccessToken != "new-access-token" {
		t.Fatalf("AccessToken 不匹配: got=%q", tokenInfo.AccessToken)
	}
	if tokenInfo.RefreshToken != "new-refresh-token" {
		t.Fatalf("RefreshToken 不匹配: got=%q", tokenInfo.RefreshToken)
	}
	if tokenInfo.ExpiresIn != 7200 {
		t.Fatalf("ExpiresIn 不匹配: got=%d", tokenInfo.ExpiresIn)
	}
	if tokenInfo.ExpiresAt == 0 {
		t.Fatal("ExpiresAt 不应为 0")
	}
}

// TestOAuthService_RefreshToken_Error injects an expired-token invalid_grant
// response. It checks that refresh returns an error after the rejected
// exchange reaches its configured client.
func TestOAuthService_RefreshToken_Error(t *testing.T) {
	t.Parallel()

	client := &mockClaudeOAuthClient{
		refreshTokenFunc: func(ctx context.Context, refreshToken, proxyURL string) (*oauth.TokenResponse, error) {
			return nil, fmt.Errorf("invalid_grant: token expired")
		},
	}

	svc := newClaudeAuthorizationForTest(&mockProxyRepoForOAuth{}, client)
	svc.Start()
	defer stopClaudeAuthorization(t, svc)

	_, err := svc.RefreshToken(context.Background(), "expired-token", "")
	if err == nil {
		t.Fatal("RefreshToken 应返回错误")
	}
}

// TestOAuthService_RefreshProviderToken_NoRefreshToken supplies an OAuth
// record containing access input only. It checks the exact missing
// refresh-token error before the unconfigured client can perform a provider
// refresh.
func TestOAuthService_RefreshProviderToken_NoRefreshToken(t *testing.T) {
	t.Parallel()

	svc := newClaudeAuthorizationForTest(&mockProxyRepoForOAuth{}, &mockClaudeOAuthClient{})
	svc.Start()
	defer stopClaudeAuthorization(t, svc)

	provider := &providercore.Record{
		ID:       1,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "some-token",
		},
	}
	_, err := svc.RefreshProviderToken(context.Background(), provider)
	if err == nil {
		t.Fatal("RefreshProviderToken 应返回错误（无 refresh_token）")
	}
	if err.Error() != "no refresh token available" {
		t.Fatalf("错误信息不匹配: got=%q", err.Error())
	}
}

// TestOAuthService_RefreshProviderToken_EmptyRefreshToken supplies an
// explicitly empty refresh credential on an OAuth record. It checks provider
// refresh returns an error for that empty input rather than attempting
// exchange.
func TestOAuthService_RefreshProviderToken_EmptyRefreshToken(t *testing.T) {
	t.Parallel()

	svc := newClaudeAuthorizationForTest(&mockProxyRepoForOAuth{}, &mockClaudeOAuthClient{})
	svc.Start()
	defer stopClaudeAuthorization(t, svc)

	provider := &providercore.Record{
		ID:       2,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "some-token",
			"refresh_token": "",
		},
	}
	_, err := svc.RefreshProviderToken(context.Background(), provider)
	if err == nil {
		t.Fatal("RefreshProviderToken 应返回错误（refresh_token 为空）")
	}
}

// TestOAuthService_RefreshProviderToken_Success refreshes a record using its
// saved refresh token. It checks the supplied client sees that credential and
// the converted provider result returns the configured replacement access
// token.
func TestOAuthService_RefreshProviderToken_Success(t *testing.T) {
	t.Parallel()

	client := &mockClaudeOAuthClient{
		refreshTokenFunc: func(ctx context.Context, refreshToken, proxyURL string) (*oauth.TokenResponse, error) {
			if refreshToken != "provider-refresh-token" {
				t.Errorf("refreshToken 不匹配: got=%q", refreshToken)
			}
			return &oauth.TokenResponse{
				AccessToken:  "refreshed-access",
				TokenType:    "Bearer",
				ExpiresIn:    3600,
				RefreshToken: "new-refresh",
			}, nil
		},
	}

	svc := newClaudeAuthorizationForTest(&mockProxyRepoForOAuth{}, client)
	svc.Start()
	defer stopClaudeAuthorization(t, svc)

	provider := &providercore.Record{
		ID:       3,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "old-access",
			"refresh_token": "provider-refresh-token",
		},
	}

	tokenInfo, err := svc.RefreshProviderToken(context.Background(), provider)
	if err != nil {
		t.Fatalf("RefreshProviderToken 返回错误: %v", err)
	}
	if tokenInfo.AccessToken != "refreshed-access" {
		t.Fatalf("AccessToken 不匹配: got=%q", tokenInfo.AccessToken)
	}
}

// TestOAuthService_RefreshProviderToken_WithProxy refreshes a provider bound
// to a credentialed SOCKS5 proxy. It checks the client receives the complete
// resolved proxy URL and the provider refresh operation succeeds.
func TestOAuthService_RefreshProviderToken_WithProxy(t *testing.T) {
	t.Parallel()

	proxyRepo := &mockProxyRepoForOAuth{
		getByIDFunc: func(ctx context.Context, id int64) (*egress.Proxy, error) {
			return &egress.Proxy{
				Protocol: "socks5",
				Host:     "socks.example.com",
				Port:     1080,
				Username: "user",
				Password: "pass",
			}, nil
		},
	}

	client := &mockClaudeOAuthClient{
		refreshTokenFunc: func(ctx context.Context, refreshToken, proxyURL string) (*oauth.TokenResponse, error) {
			if proxyURL != "socks5://user:pass@socks.example.com:1080" {
				t.Errorf("proxyURL 不匹配: got=%q", proxyURL)
			}
			return &oauth.TokenResponse{
				AccessToken: "refreshed",
				ExpiresIn:   3600,
			}, nil
		},
	}

	svc := newClaudeAuthorizationForTest(proxyRepo, client)
	svc.Start()
	defer stopClaudeAuthorization(t, svc)

	proxyID := int64(10)
	provider := &providercore.Record{
		ID:       4,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		ProxyID:  &proxyID,
		Credentials: map[string]any{
			"refresh_token": "rt-with-proxy",
		},
	}

	_, err := svc.RefreshProviderToken(context.Background(), provider)
	if err != nil {
		t.Fatalf("RefreshProviderToken 返回错误: %v", err)
	}
}

// TestOAuthService_ExchangeCode_NilOrg returns a token response without
// organization or provider identity objects. It checks exchange still succeeds
// and the converted organization/account UUID fields remain empty for that
// response.
func TestOAuthService_ExchangeCode_NilOrg(t *testing.T) {
	t.Parallel()

	client := &mockClaudeOAuthClient{
		exchangeCodeFunc: func(ctx context.Context, code, codeVerifier, state, proxyURL string, isSetupToken bool) (*oauth.TokenResponse, error) {
			return &oauth.TokenResponse{
				AccessToken:  "token-no-org",
				TokenType:    "Bearer",
				ExpiresIn:    3600,
				Organization: nil,
				Provider:     nil,
			}, nil
		},
	}

	svc := newClaudeAuthorizationForTest(&mockProxyRepoForOAuth{}, client)
	svc.Start()
	defer stopClaudeAuthorization(t, svc)

	result, _ := svc.GenerateAuthURL(context.Background(), nil)
	tokenInfo, err := svc.ExchangeCode(context.Background(), &providercore.ClaudeExchangeCodeInput{
		SessionID: result.SessionID,
		Code:      "code",
	})
	if err != nil {
		t.Fatalf("ExchangeCode 返回错误: %v", err)
	}
	if tokenInfo.OrgUUID != "" {
		t.Fatalf("OrgUUID 应为空: got=%q", tokenInfo.OrgUUID)
	}
	if tokenInfo.AccountUUID != "" {
		t.Fatalf("AccountUUID 应为空: got=%q", tokenInfo.AccountUUID)
	}
}

// TestOAuthService_Stop_NoPanic starts one authorization service and stops it
// twice. It checks repeated stop returns through the assertion helper without
// a lifecycle error or duplicate-stop panic on that same instance.
func TestOAuthService_Stop_NoPanic(t *testing.T) {
	t.Parallel()

	svc := newClaudeAuthorizationForTest(&mockProxyRepoForOAuth{}, &mockClaudeOAuthClient{})
	svc.Start()

	stopClaudeAuthorization(t, svc)

	// Stopping an already stopped service is safe.
	stopClaudeAuthorization(t, svc)
}

// GetOrganizationUUID delegates organization lookup to the configured fixture
// callback. Missing configuration panics at this operation. The returned
// organization value and error come directly from the callback using the
// caller's session and proxy.
func (m *mockClaudeOAuthClient) GetOrganizationUUID(ctx context.Context, sessionKey, proxyURL string) (string, error) {
	if m.getOrgUUIDFunc != nil {
		return m.getOrgUUIDFunc(ctx, sessionKey, proxyURL)
	}
	panic("GetOrganizationUUID not implemented")
}

// GetAuthorizationCode delegates session/scope/PKCE inputs to its fixture
// callback. A missing callback panics, revealing unexpected browser-code
// acquisition. The callback controls the returned code and error without
// additional fixture normalization.
func (m *mockClaudeOAuthClient) GetAuthorizationCode(ctx context.Context, sessionKey, orgUUID, scope, codeChallenge, state, proxyURL string) (string, error) {
	if m.getAuthCodeFunc != nil {
		return m.getAuthCodeFunc(ctx, sessionKey, orgUUID, scope, codeChallenge, state, proxyURL)
	}
	panic("GetAuthorizationCode not implemented")
}

// ExchangeCodeForToken delegates the complete code, proof and setup flag to
// its callback. A missing callback panics. Returned token response ownership
// and error behavior belong to the configured test function invoked with those
// exact inputs.
func (m *mockClaudeOAuthClient) ExchangeCodeForToken(ctx context.Context, code, codeVerifier, state, proxyURL string, isSetupToken bool) (*oauth.TokenResponse, error) {
	if m.exchangeCodeFunc != nil {
		return m.exchangeCodeFunc(ctx, code, codeVerifier, state, proxyURL, isSetupToken)
	}
	panic("ExchangeCodeForToken not implemented")
}

// RefreshToken delegates refresh credential and proxy selection to its fixture
// callback. It panics when that callback is absent. Returned token data and
// errors are passed directly to the tested core conversion path without
// fixture rewriting.
func (m *mockClaudeOAuthClient) RefreshToken(ctx context.Context, refreshToken, proxyURL string) (*oauth.TokenResponse, error) {
	if m.refreshTokenFunc != nil {
		return m.refreshTokenFunc(ctx, refreshToken, proxyURL)
	}
	panic("RefreshToken not implemented")
}

// Create panics when tested authorization attempts to create a proxy row. This
// fixture supplies configured lookup; reaching creation exposes a proxy
// persistence operation that the authorization case did not configure.
func (m *mockProxyRepoForOAuth) Create(ctx context.Context, proxy *egress.Proxy) error {
	panic("Create not implemented")
}

// GetByID delegates exact lookup to its configured callback, or returns not
// found. The callback receives the caller context and selected ID unchanged.
// Its returned proxy remains borrowed by the resolver used to construct the
// authorization options.
func (m *mockProxyRepoForOAuth) GetByID(ctx context.Context, id int64) (*egress.Proxy, error) {
	if m.getByIDFunc != nil {
		return m.getByIDFunc(ctx, id)
	}
	return nil, fmt.Errorf("proxy not found")
}

// ListByIDs panics on attempted batch proxy hydration in these authorization
// cases. The fixtures bind one selected ProxyID to GetByID, so a batched
// lookup exposes a repository path the per-session scenario did not supply.
func (m *mockProxyRepoForOAuth) ListByIDs(ctx context.Context, ids []int64) ([]egress.Proxy, error) {
	panic("ListByIDs not implemented")
}

// Update panics if authorization tries to modify a proxy record. These cases
// consume configured proxy selections through lookup; the fixture implements
// the repository method but provides no proxy configuration write behavior.
func (m *mockProxyRepoForOAuth) Update(ctx context.Context, proxy *egress.Proxy) error {
	panic("Update not implemented")
}

// Delete panics on proxy removal attempted by an authorization scenario. The
// proxy fixtures outlive the synchronous lookup they supply; this substitute
// provides no deletion operation for callers managing stored proxy lifetime.
func (m *mockProxyRepoForOAuth) Delete(ctx context.Context, id int64) error {
	panic("Delete not implemented")
}

// List panics when a caller requests paginated proxy inventory from this
// fixture. Authorization cases resolve a known optional ID directly, so
// inventory selection requires another repository implementation configured
// for that separate operation.
func (m *mockProxyRepoForOAuth) List(ctx context.Context, params pagination.PaginationParams) ([]egress.Proxy, *pagination.PaginationResult, error) {
	panic("List not implemented")
}

// ListWithFilters panics on filtered proxy inventory access from authorization
// work. This fixture has no paged filter result. Its exact-ID callback
// supports the selected session proxy rather than protocol/status/search-based
// inventory discovery.
func (m *mockProxyRepoForOAuth) ListWithFilters(ctx context.Context, params pagination.PaginationParams, protocol, status, search string) ([]egress.Proxy, *pagination.PaginationResult, error) {
	panic("ListWithFilters not implemented")
}

// ListWithFiltersAndProviderCount panics on filtered inventory and count
// aggregation. The OAuth fixtures configure individual proxy lookup, so this
// method exposes an unexpected dependency on management inventory or
// provider-count enrichment.
func (m *mockProxyRepoForOAuth) ListWithFiltersAndProviderCount(ctx context.Context, params pagination.PaginationParams, protocol, status, search string) ([]egress.ProxyWithProviderCount, *pagination.PaginationResult, error) {
	panic("ListWithFiltersAndProviderCount not implemented")
}

// ListActive panics on inventory selection across enabled proxy rows. The
// authorization cases pass an optional saved ID for direct resolution. Callers
// cannot use this lookup fixture to select an active proxy from a pool.
func (m *mockProxyRepoForOAuth) ListActive(ctx context.Context) ([]egress.Proxy, error) {
	panic("ListActive not implemented")
}

// ListActiveWithProviderCount panics on active proxy inventory with provider
// counts. No inventory or aggregate data is configured in this fixture. The
// tested authorization workflows consume the proxy returned for one exact
// selected ID.
func (m *mockProxyRepoForOAuth) ListActiveWithProviderCount(ctx context.Context) ([]egress.ProxyWithProviderCount, error) {
	panic("ListActiveWithProviderCount not implemented")
}

// ExistsByHostPortAuth panics on proxy identity deduplication in these
// authorization fixtures. Session tests reuse a configured proxy ID through
// lookup. Host, port and credential uniqueness queries belong to a separately
// configured repository case.
func (m *mockProxyRepoForOAuth) ExistsByHostPortAuth(ctx context.Context, host string, port int, username, password string) (bool, error) {
	panic("ExistsByHostPortAuth not implemented")
}

// CountProvidersByProxyID panics when authorization requests proxy attachment
// counts. The fixture supplies one lookup result and records no provider
// membership information, so an aggregate count call is an unsupported
// repository operation.
func (m *mockProxyRepoForOAuth) CountProvidersByProxyID(ctx context.Context, proxyID int64) (int64, error) {
	panic("CountProvidersByProxyID not implemented")
}

// ListProviderSummariesByProxyID panics on associated-provider summary
// enumeration. The fixture configures the proxy selected by an authorization
// session only. It carries no provider-list observation for callers performing
// proxy management analysis.
func (m *mockProxyRepoForOAuth) ListProviderSummariesByProxyID(ctx context.Context, proxyID int64) ([]egress.ProxyProviderSummary, error) {
	panic("ListProviderSummariesByProxyID not implemented")
}

// SweepExpiredProxies panics on attempted expiry maintenance through the OAuth
// fixture. The selected proxy row is a test-supplied lookup value. Maintenance
// callers need a repository that supplies expiry removal behavior and
// affected-row counts.
func (m *mockProxyRepoForOAuth) SweepExpiredProxies(ctx context.Context, now time.Time) (int64, error) {
	panic("SweepExpiredProxies not implemented")
}

// ListAllForFallback panics on fallback proxy inventory discovery from
// authorization work. These cases resolve the optional stored ProxyID
// directly. The fixture exposes no alternate candidates for a caller
// implementing proxy fallback selection.
func (m *mockProxyRepoForOAuth) ListAllForFallback(ctx context.Context) ([]egress.Proxy, error) {
	panic("ListAllForFallback not implemented")
}

// CountExpired panics on expired-proxy statistics requested from this lookup
// fixture. It configures no expiry inventory and cannot provide a meaningful
// aggregate result; the tested OAuth flow reads only its selected proxy row.
func (m *mockProxyRepoForOAuth) CountExpired(ctx context.Context) (int64, error) {
	panic("CountExpired not implemented")
}

// CountExpiringSoon panics on near-expiry proxy counting through this
// authorization fixture. Its configured rows support exact selected-ID lookup.
// The supplied time has no statistics consumer here because expiry inventory
// was not configured.
func (m *mockProxyRepoForOAuth) CountExpiringSoon(ctx context.Context, now time.Time) (int64, error) {
	panic("CountExpiringSoon not implemented")
}

// newClaudeAuthorizationForTest binds per-case clients to the production
// Claude options. Its proxy resolver converts lookup errors or nil rows into
// an unresolved selection and returns a loaded row's URL on success.
// Construction allocates the core; each case owns startup and stopping.
func newClaudeAuthorizationForTest(proxies *mockProxyRepoForOAuth, client providercore.ClaudeOAuthClient) *providercore.ClaudeAuthorization {
	options := ClaudeAuthorizationOptions(func(ctx context.Context, id int64) (string, bool) {
		proxy, err := proxies.GetByID(ctx, id)
		// An unavailable fixture proxy remains an unresolved selection for the
		// core.
		// Convert lookup failure here so these cases exercise the resolver
		// result rather than repository errors.
		if err != nil || proxy == nil {
			return "", false
		}
		return proxy.URL(), true
	})
	return providercore.NewClaudeAuthorization(client, options)
}

// stopClaudeAuthorization stops the supplied core and reports shutdown errors
// to the test. It uses Background without a deadline, so cooperative
// completion is awaited. Callers use it after startup or in deferred cleanup;
// failure terminates the calling test through Fatal.
func stopClaudeAuthorization(t *testing.T, value *providercore.ClaudeAuthorization) {
	t.Helper()
	if err := value.StopContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}
