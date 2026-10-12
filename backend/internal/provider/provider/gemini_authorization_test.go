package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	geminicli "github.com/TokenFlux/TokenRouter/internal/upstream/gemini/codeassist"
)

type mockGeminiOAuthClient struct {
	exchangeCodeFunc func(ctx context.Context, oauthType, code, codeVerifier, redirectURI, proxyURL string) (*geminicli.TokenResponse, error)
	refreshTokenFunc func(ctx context.Context, oauthType, refreshToken, proxyURL string) (*geminicli.TokenResponse, error)
}

type mockGeminiCodeAssistClient struct {
	loadCodeAssistFunc func(ctx context.Context, accessToken, proxyURL string, req *geminicli.LoadCodeAssistRequest) (*geminicli.LoadCodeAssistResponse, error)
	onboardUserFunc    func(ctx context.Context, accessToken, proxyURL string, req *geminicli.OnboardUserRequest) (*geminicli.OnboardUserResponse, error)
}

type mockGeminiProxyRepo struct {
	getByIDFunc func(ctx context.Context, id int64) (*egress.Proxy, error)
}

// mockDriveClient implements geminicli.DriveClient for tests.
type mockDriveClient struct {
	getStorageQuotaFunc func(ctx context.Context, accessToken, proxyURL string) (*geminicli.DriveStorageInfo, error)
}

func TestGeminiOAuthService_GenerateAuthURL_RedirectURIStrategy(t *testing.T) {
	// This test changes process environment variables and cannot run in parallel.
	// Supply a dummy CLI secret through the operator configuration path.
	t.Setenv(geminicli.GeminiCLIOAuthClientSecretEnv, "test-built-in-secret")

	type testCase struct {
		name          string
		cfg           *geminicli.OAuthConfig
		oauthType     string
		projectID     string
		wantClientID  string
		wantRedirect  string
		wantScope     string
		wantProjectID string
		wantErrSubstr string
	}

	tests := []testCase{
		{
			name: "google_one uses built-in client when not configured and redirects to upstream",
			cfg:  &geminicli.OAuthConfig{},

			oauthType:     "google_one",
			wantClientID:  geminicli.GeminiCLIOAuthClientID,
			wantRedirect:  geminicli.GeminiCLIRedirectURI,
			wantScope:     geminicli.DefaultCodeAssistScopes,
			wantProjectID: "",
		},
		{
			name: "google_one always forces built-in client even when custom client configured",
			cfg: &geminicli.OAuthConfig{
				ClientID:     "custom-client-id",
				ClientSecret: "custom-client-secret",
			},

			oauthType:     "google_one",
			wantClientID:  geminicli.GeminiCLIOAuthClientID,
			wantRedirect:  geminicli.GeminiCLIRedirectURI,
			wantScope:     geminicli.DefaultCodeAssistScopes,
			wantProjectID: "",
		},
		{
			name: "code_assist always forces built-in client even when custom client configured",
			cfg: &geminicli.OAuthConfig{
				ClientID:     "custom-client-id",
				ClientSecret: "custom-client-secret",
			},

			oauthType:     "code_assist",
			projectID:     "my-gcp-project",
			wantClientID:  geminicli.GeminiCLIOAuthClientID,
			wantRedirect:  geminicli.GeminiCLIRedirectURI,
			wantScope:     geminicli.DefaultCodeAssistScopes,
			wantProjectID: "my-gcp-project",
		},
		{
			name: "ai_studio requires custom client",
			cfg:  &geminicli.OAuthConfig{},

			oauthType:     "ai_studio",
			wantErrSubstr: "AI Studio OAuth requires a custom OAuth Client",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newGeminiAuthorizationForTest(nil, nil, nil, nil, tt.cfg)
			svc.Start()
			got, err := svc.GenerateAuthURL(context.Background(), nil, "https://example.com/auth/callback", tt.projectID, tt.oauthType, "")
			if tt.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Fatalf("expected error containing %q, got: %v", tt.wantErrSubstr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("GenerateAuthURL returned error: %v", err)
			}

			parsed, err := url.Parse(got.AuthURL)
			if err != nil {
				t.Fatalf("failed to parse auth_url: %v", err)
			}
			q := parsed.Query()

			if gotState := q.Get("state"); gotState != got.State {
				t.Fatalf("state mismatch: query=%q result=%q", gotState, got.State)
			}
			if gotClientID := q.Get("client_id"); gotClientID != tt.wantClientID {
				t.Fatalf("client_id mismatch: got=%q want=%q", gotClientID, tt.wantClientID)
			}
			if gotRedirect := q.Get("redirect_uri"); gotRedirect != tt.wantRedirect {
				t.Fatalf("redirect_uri mismatch: got=%q want=%q", gotRedirect, tt.wantRedirect)
			}
			if gotScope := q.Get("scope"); gotScope != tt.wantScope {
				t.Fatalf("scope mismatch: got=%q want=%q", gotScope, tt.wantScope)
			}
			if gotProjectID := q.Get("project_id"); gotProjectID != tt.wantProjectID {
				t.Fatalf("project_id mismatch: got=%q want=%q", gotProjectID, tt.wantProjectID)
			}
		})
	}
}

func TestGeminiOAuthService_BuildProviderCredentials(t *testing.T) {
	t.Parallel()

	svc := newGeminiAuthorizationForTest(nil, nil, nil, nil, &geminicli.OAuthConfig{})
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	t.Run("完整字段", func(t *testing.T) {
		t.Parallel()
		tokenInfo := &providercore.GeminiTokenInfo{
			AccessToken:  "access-123",
			RefreshToken: "refresh-456",
			ExpiresIn:    3600,
			ExpiresAt:    1700000000,
			TokenType:    "Bearer",
			Scope:        "openid email",
			ProjectID:    "my-project",
			TierID:       "gcp_standard",
			OAuthType:    "code_assist",
			Extra: map[string]any{
				"drive_storage_limit": int64(2199023255552),
			},
		}

		creds := svc.BuildProviderCredentials(tokenInfo)

		assertCredStr(t, creds, "access_token", "access-123")
		assertCredStr(t, creds, "refresh_token", "refresh-456")
		assertCredStr(t, creds, "token_type", "Bearer")
		assertCredStr(t, creds, "scope", "openid email")
		assertCredStr(t, creds, "project_id", "my-project")
		assertCredStr(t, creds, "tier_id", "gcp_standard")
		assertCredStr(t, creds, "oauth_type", "code_assist")
		assertCredStr(t, creds, "expires_at", "1700000000")

		if _, ok := creds["drive_storage_limit"]; !ok {
			t.Fatal("extra 字段 drive_storage_limit 未包含在 creds 中")
		}
	})

	t.Run("最小字段（仅 access_token 和 expires_at）", func(t *testing.T) {
		t.Parallel()
		tokenInfo := &providercore.GeminiTokenInfo{
			AccessToken: "token-only",
			ExpiresAt:   1700000000,
		}

		creds := svc.BuildProviderCredentials(tokenInfo)

		assertCredStr(t, creds, "access_token", "token-only")
		assertCredStr(t, creds, "expires_at", "1700000000")

		for _, key := range []string{"refresh_token", "token_type", "scope", "project_id", "tier_id", "oauth_type"} {
			if _, ok := creds[key]; ok {
				t.Fatalf("不应包含空字段 %q", key)
			}
		}
	})

	t.Run("无效 tier_id 被静默跳过", func(t *testing.T) {
		t.Parallel()
		tokenInfo := &providercore.GeminiTokenInfo{
			AccessToken: "token",
			ExpiresAt:   1700000000,
			TierID:      "tier with spaces",
		}

		creds := svc.BuildProviderCredentials(tokenInfo)

		if _, ok := creds["tier_id"]; ok {
			t.Fatal("无效 tier_id 不应被存入 creds")
		}
	})

	t.Run("超长 tier_id 被静默跳过", func(t *testing.T) {
		t.Parallel()
		tokenInfo := &providercore.GeminiTokenInfo{
			AccessToken: "token",
			ExpiresAt:   1700000000,
			TierID:      strings.Repeat("x", 65),
		}

		creds := svc.BuildProviderCredentials(tokenInfo)

		if _, ok := creds["tier_id"]; ok {
			t.Fatal("超长 tier_id 不应被存入 creds")
		}
	})

	t.Run("无 extra 字段", func(t *testing.T) {
		t.Parallel()
		tokenInfo := &providercore.GeminiTokenInfo{
			AccessToken:  "token",
			ExpiresAt:    1700000000,
			RefreshToken: "rt",
		}

		creds := svc.BuildProviderCredentials(tokenInfo)

		if len(creds) != 3 { // access_token, expires_at, refresh_token
			t.Fatalf("creds 字段数量不匹配: got=%d want=3, keys=%v", len(creds), credKeys(creds))
		}
	})
}

func TestGeminiOAuthService_GetOAuthConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         *geminicli.OAuthConfig
		wantEnabled bool
	}{
		{
			name: "无自定义 OAuth 客户端",
			cfg:  &geminicli.OAuthConfig{},

			wantEnabled: false,
		},
		{
			name: "仅 ClientID 无 ClientSecret",
			cfg:  &geminicli.OAuthConfig{ClientID: "custom-id"},

			wantEnabled: false,
		},
		{
			name: "仅 ClientSecret 无 ClientID",
			cfg:  &geminicli.OAuthConfig{ClientSecret: "custom-secret"},

			wantEnabled: false,
		},
		{
			name: "使用内置 Gemini CLI ClientID（不算自定义）",
			cfg: &geminicli.OAuthConfig{
				ClientID:     geminicli.GeminiCLIOAuthClientID,
				ClientSecret: "some-secret",
			},

			wantEnabled: false,
		},
		{
			name: "自定义 OAuth 客户端（非内置 ID）",
			cfg: &geminicli.OAuthConfig{
				ClientID:     "my-custom-client-id",
				ClientSecret: "my-custom-client-secret",
			},

			wantEnabled: true,
		},
		{
			name: "带空白的自定义客户端",
			cfg: &geminicli.OAuthConfig{
				ClientID:     "  my-custom-client-id  ",
				ClientSecret: "  my-custom-client-secret  ",
			},

			wantEnabled: true,
		},
		{
			name: "纯空白字符串不算配置",
			cfg: &geminicli.OAuthConfig{
				ClientID:     "   ",
				ClientSecret: "   ",
			},

			wantEnabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc := newGeminiAuthorizationForTest(nil, nil, nil, nil, tt.cfg)
			svc.Start()
			defer stopGeminiAuthorization(t, svc)

			result := svc.GetOAuthConfig()
			if result.AIStudioOAuthEnabled != tt.wantEnabled {
				t.Fatalf("AIStudioOAuthEnabled = %v, want %v", result.AIStudioOAuthEnabled, tt.wantEnabled)
			}
			if len(result.RequiredRedirectURIs) != 1 || result.RequiredRedirectURIs[0] != geminicli.AIStudioOAuthRedirectURI {
				t.Fatalf("RequiredRedirectURIs 不匹配: got=%v", result.RequiredRedirectURIs)
			}
		})
	}
}

func TestGeminiOAuthService_Stop_NoPanic(t *testing.T) {
	t.Parallel()

	svc := newGeminiAuthorizationForTest(nil, nil, nil, nil, &geminicli.OAuthConfig{})
	svc.Start()

	stopGeminiAuthorization(t, svc)
	// Stopping an already stopped service is safe.
	stopGeminiAuthorization(t, svc)
}

func TestGeminiOAuthService_RefreshToken_Success(t *testing.T) {
	t.Parallel()

	client := &mockGeminiOAuthClient{
		refreshTokenFunc: func(ctx context.Context, oauthType, refreshToken, proxyURL string) (*geminicli.TokenResponse, error) {
			return &geminicli.TokenResponse{
				AccessToken:  "new-access",
				RefreshToken: "new-refresh",
				TokenType:    "Bearer",
				ExpiresIn:    3600,
				Scope:        "openid",
			}, nil
		},
	}

	svc := newGeminiAuthorizationForTest(nil, client, nil, nil, &geminicli.OAuthConfig{})
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	info, err := svc.RefreshToken(context.Background(), "code_assist", "old-refresh", "")
	if err != nil {
		t.Fatalf("RefreshToken 返回错误: %v", err)
	}
	if info.AccessToken != "new-access" {
		t.Fatalf("AccessToken 不匹配: got=%q", info.AccessToken)
	}
	if info.RefreshToken != "new-refresh" {
		t.Fatalf("RefreshToken 不匹配: got=%q", info.RefreshToken)
	}
	if info.ExpiresAt == 0 {
		t.Fatal("ExpiresAt 不应为 0")
	}
}

func TestGeminiOAuthService_RefreshToken_NonRetryableError(t *testing.T) {
	t.Parallel()

	client := &mockGeminiOAuthClient{
		refreshTokenFunc: func(ctx context.Context, oauthType, refreshToken, proxyURL string) (*geminicli.TokenResponse, error) {
			return nil, fmt.Errorf("invalid_grant: token revoked")
		},
	}

	svc := newGeminiAuthorizationForTest(nil, client, nil, nil, &geminicli.OAuthConfig{})
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	_, err := svc.RefreshToken(context.Background(), "code_assist", "revoked-token", "")
	if err == nil {
		t.Fatal("RefreshToken 应返回错误（不可重试的 invalid_grant）")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("错误应包含 invalid_grant: got=%q", err.Error())
	}
}

func TestGeminiOAuthService_RefreshToken_RetryableError(t *testing.T) {
	t.Parallel()

	callCount := 0
	client := &mockGeminiOAuthClient{
		refreshTokenFunc: func(ctx context.Context, oauthType, refreshToken, proxyURL string) (*geminicli.TokenResponse, error) {
			callCount++
			if callCount <= 2 {
				return nil, fmt.Errorf("temporary network error")
			}
			return &geminicli.TokenResponse{
				AccessToken: "recovered",
				ExpiresIn:   3600,
			}, nil
		},
	}

	svc := newGeminiAuthorizationForTest(nil, client, nil, nil, &geminicli.OAuthConfig{})
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	info, err := svc.RefreshToken(context.Background(), "code_assist", "rt", "")
	if err != nil {
		t.Fatalf("RefreshToken 应在重试后成功: %v", err)
	}
	if info.AccessToken != "recovered" {
		t.Fatalf("AccessToken 不匹配: got=%q", info.AccessToken)
	}
	if callCount < 3 {
		t.Fatalf("应至少调用 3 次（2 次失败 + 1 次成功）: got=%d", callCount)
	}
}

func TestGeminiOAuthService_RefreshProviderToken_NotGeminiOAuth(t *testing.T) {
	t.Parallel()

	svc := newGeminiAuthorizationForTest(nil, nil, nil, nil, &geminicli.OAuthConfig{})
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	provider := &providercore.Record{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
	}

	_, err := svc.RefreshProviderToken(context.Background(), provider)
	if err == nil {
		t.Fatal("应返回错误（非 Gemini OAuth 提供商）")
	}
	if !strings.Contains(err.Error(), "not a Gemini OAuth provider") {
		t.Fatalf("错误信息不匹配: got=%q", err.Error())
	}
}

func TestGeminiOAuthService_RefreshProviderToken_NoRefreshToken(t *testing.T) {
	t.Parallel()

	svc := newGeminiAuthorizationForTest(nil, nil, nil, nil, &geminicli.OAuthConfig{})
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	provider := &providercore.Record{
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "at",
			"oauth_type":   "code_assist",
		},
	}

	_, err := svc.RefreshProviderToken(context.Background(), provider)
	if err == nil {
		t.Fatal("应返回错误（无 refresh_token）")
	}
	if !strings.Contains(err.Error(), "no refresh token") {
		t.Fatalf("错误信息不匹配: got=%q", err.Error())
	}
}

func TestGeminiOAuthService_RefreshProviderToken_AIStudio(t *testing.T) {
	t.Parallel()

	client := &mockGeminiOAuthClient{
		refreshTokenFunc: func(ctx context.Context, oauthType, refreshToken, proxyURL string) (*geminicli.TokenResponse, error) {
			return &geminicli.TokenResponse{
				AccessToken:  "refreshed-at",
				RefreshToken: "refreshed-rt",
				ExpiresIn:    3600,
				TokenType:    "Bearer",
			}, nil
		},
	}

	svc := newGeminiAuthorizationForTest(&mockGeminiProxyRepo{}, client, nil, nil, &geminicli.OAuthConfig{})
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	provider := &providercore.Record{
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "old-at",
			"refresh_token": "old-rt",
			"oauth_type":    "ai_studio",
			"tier_id":       "aistudio_free",
		},
	}

	info, err := svc.RefreshProviderToken(context.Background(), provider)
	if err != nil {
		t.Fatalf("RefreshProviderToken 返回错误: %v", err)
	}
	if info.AccessToken != "refreshed-at" {
		t.Fatalf("AccessToken 不匹配: got=%q", info.AccessToken)
	}
	if info.OAuthType != "ai_studio" {
		t.Fatalf("OAuthType 不匹配: got=%q", info.OAuthType)
	}
}

func TestGeminiOAuthService_RefreshProviderToken_CodeAssist_WithProjectID(t *testing.T) {
	t.Parallel()

	client := &mockGeminiOAuthClient{
		refreshTokenFunc: func(ctx context.Context, oauthType, refreshToken, proxyURL string) (*geminicli.TokenResponse, error) {
			return &geminicli.TokenResponse{
				AccessToken:  "refreshed",
				RefreshToken: "new-rt",
				ExpiresIn:    3600,
			}, nil
		},
	}

	svc := newGeminiAuthorizationForTest(&mockGeminiProxyRepo{}, client, nil, nil, &geminicli.OAuthConfig{})
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	provider := &providercore.Record{
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "old-at",
			"refresh_token": "old-rt",
			"oauth_type":    "code_assist",
			"project_id":    "my-project",
			"tier_id":       "gcp_standard",
		},
	}

	info, err := svc.RefreshProviderToken(context.Background(), provider)
	if err != nil {
		t.Fatalf("RefreshProviderToken 返回错误: %v", err)
	}
	if info.ProjectID != "my-project" {
		t.Fatalf("ProjectID 应保留: got=%q", info.ProjectID)
	}
	if info.TierID != providercore.GeminiTierGCPStandard {
		t.Fatalf("TierID 不匹配: got=%q want=%q", info.TierID, providercore.GeminiTierGCPStandard)
	}
	if info.OAuthType != "code_assist" {
		t.Fatalf("OAuthType 不匹配: got=%q", info.OAuthType)
	}
}

func TestGeminiOAuthService_RefreshProviderToken_DefaultOAuthType(t *testing.T) {
	t.Parallel()

	client := &mockGeminiOAuthClient{
		refreshTokenFunc: func(ctx context.Context, oauthType, refreshToken, proxyURL string) (*geminicli.TokenResponse, error) {
			if oauthType != "code_assist" {
				t.Errorf("默认 oauthType 应为 code_assist: got=%q", oauthType)
			}
			return &geminicli.TokenResponse{
				AccessToken: "refreshed",
				ExpiresIn:   3600,
			}, nil
		},
	}

	svc := newGeminiAuthorizationForTest(&mockGeminiProxyRepo{}, client, nil, nil, &geminicli.OAuthConfig{})
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	// Legacy credentials omit oauth_type.
	provider := &providercore.Record{
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "old-rt",
			"project_id":    "proj",
			"tier_id":       "STANDARD",
		},
	}

	info, err := svc.RefreshProviderToken(context.Background(), provider)
	if err != nil {
		t.Fatalf("RefreshProviderToken 返回错误: %v", err)
	}
	if info.OAuthType != "code_assist" {
		t.Fatalf("OAuthType 应默认为 code_assist: got=%q", info.OAuthType)
	}
}

func TestGeminiOAuthService_RefreshProviderToken_WithProxy(t *testing.T) {
	t.Parallel()

	proxyRepo := &mockGeminiProxyRepo{
		getByIDFunc: func(ctx context.Context, id int64) (*egress.Proxy, error) {
			return &egress.Proxy{
				Protocol: "http",
				Host:     "proxy.test",
				Port:     3128,
			}, nil
		},
	}

	client := &mockGeminiOAuthClient{
		refreshTokenFunc: func(ctx context.Context, oauthType, refreshToken, proxyURL string) (*geminicli.TokenResponse, error) {
			if proxyURL != "http://proxy.test:3128" {
				t.Errorf("proxyURL 不匹配: got=%q", proxyURL)
			}
			return &geminicli.TokenResponse{
				AccessToken: "refreshed",
				ExpiresIn:   3600,
			}, nil
		},
	}

	svc := newGeminiAuthorizationForTest(proxyRepo, client, nil, nil, &geminicli.OAuthConfig{})
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	proxyID := int64(5)
	provider := &providercore.Record{
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		ProxyID:  &proxyID,
		Credentials: map[string]any{
			"refresh_token": "rt",
			"oauth_type":    "code_assist",
			"project_id":    "proj",
		},
	}

	_, err := svc.RefreshProviderToken(context.Background(), provider)
	if err != nil {
		t.Fatalf("RefreshProviderToken 返回错误: %v", err)
	}
}

func TestGeminiOAuthService_RefreshProviderToken_CodeAssist_NoProjectID_AutoDetect(t *testing.T) {
	t.Parallel()

	client := &mockGeminiOAuthClient{
		refreshTokenFunc: func(ctx context.Context, oauthType, refreshToken, proxyURL string) (*geminicli.TokenResponse, error) {
			return &geminicli.TokenResponse{
				AccessToken: "at",
				ExpiresIn:   3600,
			}, nil
		},
	}

	codeAssist := &mockGeminiCodeAssistClient{
		loadCodeAssistFunc: func(ctx context.Context, accessToken, proxyURL string, req *geminicli.LoadCodeAssistRequest) (*geminicli.LoadCodeAssistResponse, error) {
			return &geminicli.LoadCodeAssistResponse{
				CloudAICompanionProject: "auto-project-123",
				CurrentTier:             &geminicli.TierInfo{ID: "STANDARD"},
			}, nil
		},
	}

	svc := newGeminiAuthorizationForTest(&mockGeminiProxyRepo{}, client, codeAssist, nil, &geminicli.OAuthConfig{
		// Inject discovery failure here; platform HTTP tests cover response parsing.
	})

	svc.Options.FetchProject = func(context.Context, string, string) (string, error) {
		return "", errors.New("fixture resource manager unavailable")
	}
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	provider := &providercore.Record{
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt",
			"oauth_type":    "code_assist",
			// Omitting project_id triggers project discovery.
		},
	}

	info, err := svc.RefreshProviderToken(context.Background(), provider)
	if err != nil {
		t.Fatalf("RefreshProviderToken 返回错误: %v", err)
	}
	if info.ProjectID != "auto-project-123" {
		t.Fatalf("ProjectID 应为自动检测值: got=%q", info.ProjectID)
	}
	if info.TierID != providercore.GeminiTierGCPStandard {
		t.Fatalf("TierID 不匹配: got=%q", info.TierID)
	}
}

func TestGeminiOAuthService_RefreshProviderToken_CodeAssist_NoProjectID_FailsEmpty(t *testing.T) {
	t.Parallel()

	client := &mockGeminiOAuthClient{
		refreshTokenFunc: func(ctx context.Context, oauthType, refreshToken, proxyURL string) (*geminicli.TokenResponse, error) {
			return &geminicli.TokenResponse{
				AccessToken: "at",
				ExpiresIn:   3600,
			}, nil
		},
	}

	// A current tier with no project exercises discovery for an already onboarded user.
	// This reaches the failing resource-manager lookup without waiting for onboarding retries.
	codeAssist := &mockGeminiCodeAssistClient{
		loadCodeAssistFunc: func(ctx context.Context, accessToken, proxyURL string, req *geminicli.LoadCodeAssistRequest) (*geminicli.LoadCodeAssistResponse, error) {
			return &geminicli.LoadCodeAssistResponse{
				CurrentTier: &geminicli.TierInfo{ID: "STANDARD"},
			}, nil
		},
	}

	svc := newGeminiAuthorizationForTest(&mockGeminiProxyRepo{}, client, codeAssist, nil, &geminicli.OAuthConfig{
		// Inject discovery failure here; platform HTTP tests cover response parsing.
	})

	svc.Options.FetchProject = func(context.Context, string, string) (string, error) {
		return "", errors.New("fixture resource manager unavailable")
	}
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	provider := &providercore.Record{
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt",
			"oauth_type":    "code_assist",
		},
	}

	_, err := svc.RefreshProviderToken(context.Background(), provider)
	if err == nil {
		t.Fatal("应返回错误（无法检测 project_id）")
	}
	if !strings.Contains(err.Error(), "project_id") {
		t.Fatalf("错误信息应包含 project_id: got=%q", err.Error())
	}
}

func TestGeminiOAuthService_RefreshProviderToken_GoogleOne_FreshCache(t *testing.T) {
	t.Parallel()

	client := &mockGeminiOAuthClient{
		refreshTokenFunc: func(ctx context.Context, oauthType, refreshToken, proxyURL string) (*geminicli.TokenResponse, error) {
			return &geminicli.TokenResponse{
				AccessToken: "at",
				ExpiresIn:   3600,
			}, nil
		},
	}

	svc := newGeminiAuthorizationForTest(&mockGeminiProxyRepo{}, client, nil, nil, &geminicli.OAuthConfig{})
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	provider := &providercore.Record{
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt",
			"oauth_type":    "google_one",
			"project_id":    "proj",
			"tier_id":       "google_ai_pro",
		},
		Extra: map[string]any{
			// The tier cache is within its 24-hour freshness window.
			"drive_tier_updated_at": time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
		},
	}

	info, err := svc.RefreshProviderToken(context.Background(), provider)
	if err != nil {
		t.Fatalf("RefreshProviderToken 返回错误: %v", err)
	}
	if info.TierID != providercore.GeminiTierGoogleAIPro {
		t.Fatalf("TierID 应使用缓存值: got=%q want=%q", info.TierID, providercore.GeminiTierGoogleAIPro)
	}
}

func TestGeminiOAuthService_RefreshProviderToken_GoogleOne_NoTierID_DefaultsFree(t *testing.T) {
	t.Parallel()

	client := &mockGeminiOAuthClient{
		refreshTokenFunc: func(ctx context.Context, oauthType, refreshToken, proxyURL string) (*geminicli.TokenResponse, error) {
			return &geminicli.TokenResponse{
				AccessToken: "at",
				ExpiresIn:   3600,
			}, nil
		},
	}

	svc := newGeminiAuthorizationForTest(&mockGeminiProxyRepo{}, client, nil, &mockDriveClient{}, &geminicli.OAuthConfig{})
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	provider := &providercore.Record{
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt",
			"oauth_type":    "google_one",
			"project_id":    "proj",
		},
	}

	info, err := svc.RefreshProviderToken(context.Background(), provider)
	if err != nil {
		t.Fatalf("RefreshProviderToken 返回错误: %v", err)
	}
	// With no stored tier and an unavailable Drive lookup, use the free tier.
	if info.TierID != providercore.GeminiTierGoogleOneFree {
		t.Fatalf("TierID 应为默认 free: got=%q", info.TierID)
	}
}

func TestGeminiOAuthService_RefreshProviderToken_UnauthorizedClient_Fallback(t *testing.T) {
	t.Parallel()

	callCount := 0
	client := &mockGeminiOAuthClient{
		refreshTokenFunc: func(ctx context.Context, oauthType, refreshToken, proxyURL string) (*geminicli.TokenResponse, error) {
			callCount++
			if oauthType == "code_assist" {
				return nil, fmt.Errorf("unauthorized_client: client mismatch")
			}
			return &geminicli.TokenResponse{
				AccessToken: "recovered",
				ExpiresIn:   3600,
			}, nil
		},
	}

	// A custom OAuth client enables recovery through AI Studio.
	cfg := &geminicli.OAuthConfig{
		ClientID:     "custom-id",
		ClientSecret: "custom-secret",
	}

	svc := newGeminiAuthorizationForTest(&mockGeminiProxyRepo{}, client, nil, nil, cfg)
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	provider := &providercore.Record{
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt",
			"oauth_type":    "code_assist",
			"project_id":    "proj",
			"tier_id":       "gcp_standard",
		},
	}

	info, err := svc.RefreshProviderToken(context.Background(), provider)
	if err != nil {
		t.Fatalf("RefreshProviderToken 应在 fallback 后成功: %v", err)
	}
	if info.AccessToken != "recovered" {
		t.Fatalf("AccessToken 不匹配: got=%q", info.AccessToken)
	}
}

func TestGeminiOAuthService_RefreshProviderToken_UnauthorizedClient_NoFallback(t *testing.T) {
	t.Parallel()

	client := &mockGeminiOAuthClient{
		refreshTokenFunc: func(ctx context.Context, oauthType, refreshToken, proxyURL string) (*geminicli.TokenResponse, error) {
			return nil, fmt.Errorf("unauthorized_client: client mismatch")
		},
	}

	// Without a custom OAuth client, the alternate exchange is unavailable.
	svc := newGeminiAuthorizationForTest(&mockGeminiProxyRepo{}, client, nil, nil, &geminicli.OAuthConfig{})
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	provider := &providercore.Record{
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt",
			"oauth_type":    "code_assist",
			"project_id":    "proj",
		},
	}

	_, err := svc.RefreshProviderToken(context.Background(), provider)
	if err == nil {
		t.Fatal("应返回错误（无 fallback）")
	}
	if !strings.Contains(err.Error(), "OAuth client mismatch") {
		t.Fatalf("错误应包含 OAuth client mismatch: got=%q", err.Error())
	}
}

func TestGeminiOAuthService_ExchangeCode_SessionNotFound(t *testing.T) {
	t.Parallel()

	svc := newGeminiAuthorizationForTest(nil, nil, nil, nil, &geminicli.OAuthConfig{})
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	_, err := svc.ExchangeCode(context.Background(), &providercore.GeminiExchangeCodeInput{
		SessionID: "nonexistent",
		State:     "some-state",
		Code:      "some-code",
	})
	if err == nil {
		t.Fatal("应返回错误（session 不存在）")
	}
	if !strings.Contains(err.Error(), "session not found") {
		t.Fatalf("错误信息不匹配: got=%q", err.Error())
	}
}

func TestGeminiOAuthService_ExchangeCode_InvalidState(t *testing.T) {
	t.Parallel()

	svc := newGeminiAuthorizationForTest(nil, nil, nil, nil, &geminicli.OAuthConfig{})
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	// Set CreatedAt so TTL validation accepts this manually created session.
	svc.Store.Set("test-session", &providercore.GeminiOAuthSession{
		State:        "correct-state",
		CodeVerifier: "verifier",
		OAuthType:    "ai_studio",
		CreatedAt:    time.Now(),
	})

	_, err := svc.ExchangeCode(context.Background(), &providercore.GeminiExchangeCodeInput{
		SessionID: "test-session",
		State:     "wrong-state",
		Code:      "code",
	})
	if err == nil {
		t.Fatal("应返回错误（state 不匹配）")
	}
	if !strings.Contains(err.Error(), "invalid state") {
		t.Fatalf("错误信息不匹配: got=%q", err.Error())
	}
}

func TestGeminiOAuthService_ExchangeCode_EmptyState(t *testing.T) {
	t.Parallel()

	svc := newGeminiAuthorizationForTest(nil, nil, nil, nil, &geminicli.OAuthConfig{})
	svc.Start()
	defer stopGeminiAuthorization(t, svc)

	svc.Store.Set("test-session", &providercore.GeminiOAuthSession{
		State:        "correct-state",
		CodeVerifier: "verifier",
		CreatedAt:    time.Now(),
	})

	_, err := svc.ExchangeCode(context.Background(), &providercore.GeminiExchangeCodeInput{
		SessionID: "test-session",
		State:     "",
		Code:      "code",
	})
	if err == nil {
		t.Fatal("应返回错误（空 state）")
	}
}

func (m *mockGeminiOAuthClient) ExchangeCode(ctx context.Context, oauthType, code, codeVerifier, redirectURI, proxyURL string) (*geminicli.TokenResponse, error) {
	if m.exchangeCodeFunc != nil {
		return m.exchangeCodeFunc(ctx, oauthType, code, codeVerifier, redirectURI, proxyURL)
	}
	panic("ExchangeCode not implemented")
}

func (m *mockGeminiOAuthClient) RefreshToken(ctx context.Context, oauthType, refreshToken, proxyURL string) (*geminicli.TokenResponse, error) {
	if m.refreshTokenFunc != nil {
		return m.refreshTokenFunc(ctx, oauthType, refreshToken, proxyURL)
	}
	panic("RefreshToken not implemented")
}

func (m *mockGeminiCodeAssistClient) LoadCodeAssist(ctx context.Context, accessToken, proxyURL string, req *geminicli.LoadCodeAssistRequest) (*geminicli.LoadCodeAssistResponse, error) {
	if m.loadCodeAssistFunc != nil {
		return m.loadCodeAssistFunc(ctx, accessToken, proxyURL, req)
	}
	panic("LoadCodeAssist not implemented")
}

func (m *mockGeminiCodeAssistClient) OnboardUser(ctx context.Context, accessToken, proxyURL string, req *geminicli.OnboardUserRequest) (*geminicli.OnboardUserResponse, error) {
	if m.onboardUserFunc != nil {
		return m.onboardUserFunc(ctx, accessToken, proxyURL, req)
	}
	panic("OnboardUser not implemented")
}

func (m *mockGeminiProxyRepo) Create(ctx context.Context, proxy *egress.Proxy) error {
	panic("not impl")
}

func (m *mockGeminiProxyRepo) GetByID(ctx context.Context, id int64) (*egress.Proxy, error) {
	if m.getByIDFunc != nil {
		return m.getByIDFunc(ctx, id)
	}
	return nil, fmt.Errorf("proxy not found")
}

func (m *mockGeminiProxyRepo) ListByIDs(ctx context.Context, ids []int64) ([]egress.Proxy, error) {
	panic("not impl")
}

func (m *mockGeminiProxyRepo) Update(ctx context.Context, proxy *egress.Proxy) error {
	panic("not impl")
}

func (m *mockGeminiProxyRepo) Delete(ctx context.Context, id int64) error { panic("not impl") }

func (m *mockGeminiProxyRepo) List(ctx context.Context, params pagination.PaginationParams) ([]egress.Proxy, *pagination.PaginationResult, error) {
	panic("not impl")
}

func (m *mockGeminiProxyRepo) ListWithFilters(ctx context.Context, params pagination.PaginationParams, protocol, status, search string) ([]egress.Proxy, *pagination.PaginationResult, error) {
	panic("not impl")
}

func (m *mockGeminiProxyRepo) ListWithFiltersAndProviderCount(ctx context.Context, params pagination.PaginationParams, protocol, status, search string) ([]egress.ProxyWithProviderCount, *pagination.PaginationResult, error) {
	panic("not impl")
}

func (m *mockGeminiProxyRepo) ListActive(ctx context.Context) ([]egress.Proxy, error) {
	panic("not impl")
}

func (m *mockGeminiProxyRepo) ListActiveWithProviderCount(ctx context.Context) ([]egress.ProxyWithProviderCount, error) {
	panic("not impl")
}

func (m *mockGeminiProxyRepo) ExistsByHostPortAuth(ctx context.Context, host string, port int, username, password string) (bool, error) {
	panic("not impl")
}

func (m *mockGeminiProxyRepo) CountProvidersByProxyID(ctx context.Context, proxyID int64) (int64, error) {
	panic("not impl")
}

func (m *mockGeminiProxyRepo) ListProviderSummariesByProxyID(ctx context.Context, proxyID int64) ([]egress.ProxyProviderSummary, error) {
	panic("not impl")
}

func (m *mockGeminiProxyRepo) SweepExpiredProxies(ctx context.Context, now time.Time) (int64, error) {
	panic("not impl")
}

func (m *mockGeminiProxyRepo) ListAllForFallback(ctx context.Context) ([]egress.Proxy, error) {
	panic("not impl")
}

func (m *mockGeminiProxyRepo) CountExpired(ctx context.Context) (int64, error) {
	panic("not impl")
}

func (m *mockGeminiProxyRepo) CountExpiringSoon(ctx context.Context, now time.Time) (int64, error) {
	panic("not impl")
}

func (m *mockDriveClient) GetStorageQuota(ctx context.Context, accessToken, proxyURL string) (*geminicli.DriveStorageInfo, error) {
	if m.getStorageQuotaFunc != nil {
		return m.getStorageQuotaFunc(ctx, accessToken, proxyURL)
	}
	return nil, fmt.Errorf("drive API not available in test")
}

func assertCredStr(t *testing.T, creds map[string]any, key, want string) {
	t.Helper()
	raw, ok := creds[key]
	if !ok {
		t.Fatalf("creds 缺少 key=%q", key)
	}
	got, ok := raw.(string)
	if !ok {
		t.Fatalf("creds[%q] 不是 string: %T", key, raw)
	}
	if got != want {
		t.Fatalf("creds[%q] = %q, want %q", key, got, want)
	}
}

func credKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func newGeminiAuthorizationForTest(proxies *mockGeminiProxyRepo, client providercore.GeminiOAuthClient, discovery providercore.GeminiCodeAssistClient, drive providercore.GeminiDriveClient, cfg *geminicli.OAuthConfig) *providercore.GeminiAuthorization {
	var resolve func(context.Context, int64) (string, bool)
	if proxies != nil {
		resolve = func(ctx context.Context, id int64) (string, bool) {
			proxy, err := proxies.GetByID(ctx, id)
			if err != nil || proxy == nil {
				return "", false
			}
			return proxy.URL(), true
		}
	}
	return providercore.NewGeminiAuthorization(client, discovery, drive, GeminiAuthorizationOptions(func() geminicli.OAuthConfig { return *cfg }, resolve))
}

func stopGeminiAuthorization(t *testing.T, value *providercore.GeminiAuthorization) {
	t.Helper()
	if err := value.StopContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}
