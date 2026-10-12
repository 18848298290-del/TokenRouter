package provider

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

type AntigravityAuthorizationClient interface {
	ExchangeCode(context.Context, string, string) (*google.AntigravityTokenResponse, error)
	RefreshToken(context.Context, string) (*google.AntigravityTokenResponse, error)
	GetUserInfo(context.Context, string) (*google.AntigravityUserInfo, error)
	LoadCodeAssist(context.Context, string) (*google.AntigravityLoadCodeAssistResponse, map[string]any, error)
	OnboardUser(context.Context, string, string) (string, error)
	SetUserSettings(context.Context, string) (*google.AntigravitySetUserSettingsResponse, error)
	FetchUserInfo(context.Context, string, string) (*google.AntigravityFetchUserInfoResponse, error)
}
type AntigravityAuthorizationOptions struct {
	NewClient                                              func(string) (AntigravityAuthorizationClient, error)
	ResolveProxy                                           func(context.Context, int64) (string, bool)
	GenerateState, GenerateCodeVerifier, GenerateSessionID func() (string, error)
	GenerateCodeChallenge                                  func(string) string
	BuildAuthorizationURL                                  func(string, string) string
	IsConnectionError                                      func(error) bool
	Printf                                                 func(string, ...any)
	Warn, Info                                             func(string, ...any)
}

// @project-doc docs/interfaces/antigravity_upstream.md#antigravity_native_execution
type AntigravityAuthorization struct {
	Store    *AntigravityAuthorizationSessions
	Options  AntigravityAuthorizationOptions
	activity operationActivity
}

// AntigravityAuthURLResult contains an authorization URL and its session identifiers.
type AntigravityAuthURLResult struct {
	AuthURL   string `json:"auth_url"`
	SessionID string `json:"session_id"`
	State     string `json:"state"`
}

// AntigravityExchangeCodeInput supplies an authorization code and session proof for token exchange.
type AntigravityExchangeCodeInput struct {
	SessionID string
	State     string
	Code      string
	ProxyID   *int64
}

// AntigravityTokenInfo contains tokens, expiry, and discovered account metadata.
type AntigravityTokenInfo struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	ExpiresAt        int64  `json:"expires_at"`
	TokenType        string `json:"token_type"`
	Email            string `json:"email,omitempty"`
	ProjectID        string `json:"project_id,omitempty"`
	ProjectIDMissing bool   `json:"-"`
	PlanType         string `json:"-"`
	PrivacyMode      string `json:"-"`
}

// loadCodeAssistResult contains the project ID and subscription metadata
// returned by loadProjectIDWithRetry.
type loadCodeAssistResult struct {
	ProjectID    string
	Subscription *AntigravitySubscriptionResult
}

func NewAntigravityAuthorization(options AntigravityAuthorizationOptions) *AntigravityAuthorization {
	return &AntigravityAuthorization{Store: NewAntigravityAuthorizationSessions(), Options: options}
}
func (s *AntigravityAuthorization) Start() { s.Store.Start() }
func (s *AntigravityAuthorization) StopContext(ctx context.Context) error {
	s.Store.Stop()
	return s.activity.stop(ctx, "antigravity authorization")
}

// generateAuthURL creates a Google OAuth authorization URL and stores its PKCE session.
func (s *AntigravityAuthorization) generateAuthURL(ctx context.Context, proxyID *int64) (*AntigravityAuthURLResult, error) {
	state, err := s.Options.GenerateState()
	if err != nil {
		return nil, fmt.Errorf("生成 state 失败: %w", err)
	}

	codeVerifier, err := s.Options.GenerateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("生成 code_verifier 失败: %w", err)
	}

	sessionID, err := s.Options.GenerateSessionID()
	if err != nil {
		return nil, fmt.Errorf("生成 session_id 失败: %w", err)
	}

	var proxyURL string
	if proxyID != nil {
		if proxy, ok := s.Options.ResolveProxy(ctx, *proxyID); ok {
			proxyURL = proxy
		}
	}

	session := &AntigravityAuthorizationSession{
		State:        state,
		CodeVerifier: codeVerifier,
		ProxyURL:     proxyURL,
		CreatedAt:    time.Now(),
	}
	s.Store.Set(sessionID, session)

	codeChallenge := s.Options.GenerateCodeChallenge(codeVerifier)
	authURL := s.Options.BuildAuthorizationURL(state, codeChallenge)

	return &AntigravityAuthURLResult{
		AuthURL:   authURL,
		SessionID: sessionID,
		State:     state,
	}, nil
}

// exchangeCode exchanges an authorization code after validating the stored session state.
func (s *AntigravityAuthorization) exchangeCode(ctx context.Context, input *AntigravityExchangeCodeInput) (*AntigravityTokenInfo, error) {
	session, ok := s.Store.Get(input.SessionID)
	if !ok {
		return nil, fmt.Errorf("session 不存在或已过期")
	}

	if strings.TrimSpace(input.State) == "" || input.State != session.State {
		return nil, fmt.Errorf("state 无效")
	}

	// A resolved request proxy overrides the proxy saved in the session.
	proxyURL := session.ProxyURL
	if input.ProxyID != nil {
		if proxy, ok := s.Options.ResolveProxy(ctx, *input.ProxyID); ok {
			proxyURL = proxy
		}
	}

	client, err := s.Options.NewClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create antigravity client failed: %w", err)
	}

	// Exchange using the session's PKCE verifier.
	tokenResp, err := client.ExchangeCode(ctx, input.Code, session.CodeVerifier)
	if err != nil {
		return nil, fmt.Errorf("token 交换失败: %w", err)
	}

	// Consume the session after a successful exchange.
	s.Store.Delete(input.SessionID)

	// Reserve a five-minute safety margin before token expiry.
	expiresAt := time.Now().Unix() + tokenResp.ExpiresIn - 300

	result := &AntigravityTokenInfo{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresIn:    tokenResp.ExpiresIn,
		ExpiresAt:    expiresAt,
		TokenType:    tokenResp.TokenType,
	}

	// Failure to fetch the email does not invalidate the exchanged token.
	userInfo, err := client.GetUserInfo(ctx, tokenResp.AccessToken)
	if err != nil {
		s.Options.Printf("[AntigravityOAuth] 警告: 获取用户信息失败: %v\n", err)
	} else {
		result.Email = userInfo.Email
	}

	// Retry project and subscription discovery; some account types may omit a project.
	loadResult, loadErr := s.loadProjectIDWithRetry(ctx, tokenResp.AccessToken, proxyURL, 3)
	if loadErr != nil {
		s.Options.Printf("[AntigravityOAuth] 警告: 获取 project_id 失败（重试后）: %v\n", loadErr)
		result.ProjectIDMissing = true
	}
	if loadResult != nil {
		result.ProjectID = loadResult.ProjectID
		if loadResult.Subscription != nil {
			result.PlanType = loadResult.Subscription.PlanType
		}
	}

	// Apply privacy settings with the newly exchanged token.
	result.PrivacyMode = s.SetPrivacy(ctx, result.AccessToken, result.ProjectID, proxyURL)

	return result, nil
}

// refreshToken refreshes tokens with bounded retries.
func (s *AntigravityAuthorization) refreshToken(ctx context.Context, refreshToken, proxyURL string) (*AntigravityTokenInfo, error) {
	var lastErr error

	for attempt := range 4 {
		if attempt > 0 {
			backoff := min(time.Duration(1<<uint(attempt-1))*time.Second, 30*time.Second)
			time.Sleep(backoff)
		}

		client, err := s.Options.NewClient(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("create antigravity client failed: %w", err)
		}
		tokenResp, err := client.RefreshToken(ctx, refreshToken)
		if err == nil {
			now := time.Now()
			expiresAt := now.Unix() + tokenResp.ExpiresIn - 300
			s.Options.Printf("[AntigravityOAuth] Token refreshed: expires_in=%d, expires_at=%d (%s)\n",
				tokenResp.ExpiresIn, expiresAt, time.Unix(expiresAt, 0).Format("2006-01-02 15:04:05"))
			return &AntigravityTokenInfo{
				AccessToken:  tokenResp.AccessToken,
				RefreshToken: tokenResp.RefreshToken,
				ExpiresIn:    tokenResp.ExpiresIn,
				ExpiresAt:    expiresAt,
				TokenType:    tokenResp.TokenType,
			}, nil
		}

		if IsNonRetryableAntigravityOAuthError(err) {
			return nil, err
		}
		// Return connection failures immediately, including TCP timeouts, refused connections, and DNS errors.
		if s.Options.IsConnectionError(err) {
			return nil, fmt.Errorf("proxy unavailable: %w", err)
		}
		lastErr = err
	}

	return nil, fmt.Errorf("token 刷新失败 (重试后): %w", lastErr)
}

// validateRefreshToken refreshes a token and discovers its email and project metadata.
func (s *AntigravityAuthorization) validateRefreshToken(ctx context.Context, refreshToken string, proxyID *int64) (*AntigravityTokenInfo, error) {
	var proxyURL string
	if proxyID != nil {
		if proxy, ok := s.Options.ResolveProxy(ctx, *proxyID); ok {
			proxyURL = proxy
		}
	}

	// Refresh before using the token for account discovery.
	tokenInfo, err := s.refreshToken(ctx, refreshToken, proxyURL)
	if err != nil {
		return nil, err
	}

	// Email discovery is best effort.
	client, err := s.Options.NewClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create antigravity client failed: %w", err)
	}
	userInfo, err := client.GetUserInfo(ctx, tokenInfo.AccessToken)
	if err != nil {
		s.Options.Printf("[AntigravityOAuth] 警告: 获取用户信息失败: %v\n", err)
	} else {
		tokenInfo.Email = userInfo.Email
	}

	// Project and subscription discovery failures do not discard the refreshed token.
	loadResult, loadErr := s.loadProjectIDWithRetry(ctx, tokenInfo.AccessToken, proxyURL, 3)
	if loadErr != nil {
		s.Options.Printf("[AntigravityOAuth] 警告: 获取 project_id 失败（重试后）: %v\n", loadErr)
		tokenInfo.ProjectIDMissing = true
	}
	if loadResult != nil {
		tokenInfo.ProjectID = loadResult.ProjectID
		if loadResult.Subscription != nil {
			tokenInfo.PlanType = loadResult.Subscription.PlanType
		}
	}

	// Apply privacy settings with the refreshed token.
	tokenInfo.PrivacyMode = s.SetPrivacy(ctx, tokenInfo.AccessToken, tokenInfo.ProjectID, proxyURL)

	return tokenInfo, nil
}

func IsNonRetryableAntigravityOAuthError(err error) bool {
	msg := err.Error()
	nonRetryable := []string{
		"invalid_grant",
		"invalid_client",
		"unauthorized_client",
		"access_denied",
	}
	for _, needle := range nonRetryable {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// refreshProviderToken refreshes an Antigravity OAuth provider and its project metadata.
func (s *AntigravityAuthorization) refreshProviderToken(ctx context.Context, provider *Record) (*AntigravityTokenInfo, error) {
	if provider.Platform != PlatformAntigravity || provider.Type != ProviderTypeOAuth {
		return nil, fmt.Errorf("非 Antigravity OAuth 提供商")
	}

	refreshToken := provider.GetCredential("refresh_token")
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("无可用的 refresh_token")
	}

	var proxyURL string
	if provider.ProxyID != nil {
		if proxy, ok := s.Options.ResolveProxy(ctx, *provider.ProxyID); ok {
			proxyURL = proxy
		}
	}

	tokenInfo, err := s.refreshToken(ctx, refreshToken, proxyURL)
	if err != nil {
		return nil, err
	}

	// Retain an existing email during refresh.
	existingEmail := strings.TrimSpace(provider.GetCredential("email"))
	if existingEmail != "" {
		tokenInfo.Email = existingEmail
	}

	// Refresh project and subscription metadata through LoadCodeAssist, with retries.
	existingProjectID := strings.TrimSpace(provider.GetCredential("project_id"))
	loadResult, loadErr := s.loadProjectIDWithRetry(ctx, tokenInfo.AccessToken, proxyURL, 3)

	if loadErr != nil {
		tokenInfo.ProjectID = existingProjectID
		if existingProjectID == "" {
			tokenInfo.ProjectIDMissing = true
		}
	}
	if loadResult != nil {
		if loadResult.ProjectID != "" {
			tokenInfo.ProjectID = loadResult.ProjectID
		}
		if loadResult.Subscription != nil {
			tokenInfo.PlanType = loadResult.Subscription.PlanType
		}
	}

	return tokenInfo, nil
}

// loadProjectIDWithRetry retries project discovery and also returns subscription metadata.
func (s *AntigravityAuthorization) loadProjectIDWithRetry(ctx context.Context, accessToken, proxyURL string, maxRetries int) (*loadCodeAssistResult, error) {
	var lastErr error
	var lastSubscription *AntigravitySubscriptionResult

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := min(time.Duration(1<<uint(attempt-1))*time.Second, 8*time.Second)
			time.Sleep(backoff)
		}

		client, err := s.Options.NewClient(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("create antigravity client failed: %w", err)
		}
		loadResp, loadRaw, err := client.LoadCodeAssist(ctx, accessToken)

		if loadResp != nil {
			sub := NormalizeAntigravitySubscription(loadResp)
			lastSubscription = &sub
		}

		if err == nil && loadResp != nil && loadResp.CloudAICompanionProject != "" {
			return &loadCodeAssistResult{
				ProjectID:    loadResp.CloudAICompanionProject,
				Subscription: lastSubscription,
			}, nil
		}

		if err == nil {
			if projectID, onboardErr := tryOnboardProjectID(ctx, client, accessToken, loadRaw); onboardErr == nil && projectID != "" {
				return &loadCodeAssistResult{
					ProjectID:    projectID,
					Subscription: lastSubscription,
				}, nil
			} else if onboardErr != nil {
				lastErr = onboardErr
				continue
			}
		}

		if err != nil {
			lastErr = err
		} else if loadResp == nil {
			lastErr = fmt.Errorf("LoadCodeAssist 返回空响应")
		} else {
			lastErr = fmt.Errorf("LoadCodeAssist 返回空 project_id")
		}
	}

	if lastSubscription != nil {
		return &loadCodeAssistResult{Subscription: lastSubscription}, fmt.Errorf("获取 project_id 失败 (重试 %d 次后): %w", maxRetries, lastErr)
	}
	return nil, fmt.Errorf("获取 project_id 失败 (重试 %d 次后): %w", maxRetries, lastErr)
}

func tryOnboardProjectID(ctx context.Context, client AntigravityAuthorizationClient, accessToken string, loadRaw map[string]any) (string, error) {
	tierID := ResolveAntigravityDefaultTierID(loadRaw)
	if tierID == "" {
		return "", fmt.Errorf("loadCodeAssist 未返回可用的默认 tier")
	}

	projectID, err := client.OnboardUser(ctx, accessToken, tierID)
	if err != nil {
		return "", fmt.Errorf("onboardUser 失败 (tier=%s): %w", tierID, err)
	}
	return projectID, nil
}

func ResolveAntigravityDefaultTierID(loadRaw map[string]any) string {
	if len(loadRaw) == 0 {
		return ""
	}

	rawTiers, ok := loadRaw["allowedTiers"]
	if !ok {
		return ""
	}

	tiers, ok := rawTiers.([]any)
	if !ok {
		return ""
	}

	for _, rawTier := range tiers {
		tier, ok := rawTier.(map[string]any)
		if !ok {
			continue
		}
		if isDefault, _ := tier["isDefault"].(bool); !isDefault {
			continue
		}
		if id, ok := tier["id"].(string); ok {
			id = strings.TrimSpace(id)
			if id != "" {
				return id
			}
		}
	}

	return ""
}

// fillProjectID discovers a project using the supplied access token without refreshing it.
func (s *AntigravityAuthorization) fillProjectID(ctx context.Context, provider *Record, accessToken string) (string, error) {
	var proxyURL string
	if provider.ProxyID != nil {
		if proxy, ok := s.Options.ResolveProxy(ctx, *provider.ProxyID); ok {
			proxyURL = proxy
		}
	}
	result, err := s.loadProjectIDWithRetry(ctx, accessToken, proxyURL, 3)
	if result != nil {
		return result.ProjectID, err
	}
	return "", err
}

// BuildProviderCredentials converts token information to stored credential fields.
func (s *AntigravityAuthorization) BuildProviderCredentials(tokenInfo *AntigravityTokenInfo) map[string]any {
	creds := map[string]any{
		"access_token": tokenInfo.AccessToken,
		"expires_at":   strconv.FormatInt(tokenInfo.ExpiresAt, 10),
	}
	if tokenInfo.RefreshToken != "" {
		creds["refresh_token"] = tokenInfo.RefreshToken
	}
	if tokenInfo.TokenType != "" {
		creds["token_type"] = tokenInfo.TokenType
	}
	if tokenInfo.Email != "" {
		creds["email"] = tokenInfo.Email
	}
	if tokenInfo.ProjectID != "" {
		creds["project_id"] = tokenInfo.ProjectID
	}
	if tokenInfo.PlanType != "" {
		creds["plan_type"] = tokenInfo.PlanType
	}
	return creds
}

func (s *AntigravityAuthorization) GenerateAuthURL(ctx context.Context, proxyID *int64) (*AntigravityAuthURLResult, error) {
	operation, done, err := s.activity.begin(ctx, fmt.Errorf("antigravity authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	return s.generateAuthURL(operation, proxyID)
}

func (s *AntigravityAuthorization) ExchangeCode(ctx context.Context, input *AntigravityExchangeCodeInput) (*AntigravityTokenInfo, error) {
	operation, done, err := s.activity.begin(ctx, fmt.Errorf("antigravity authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	return s.exchangeCode(operation, input)
}

func (s *AntigravityAuthorization) RefreshToken(ctx context.Context, refreshToken, proxyURL string) (*AntigravityTokenInfo, error) {
	operation, done, err := s.activity.begin(ctx, fmt.Errorf("antigravity authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	return s.refreshToken(operation, refreshToken, proxyURL)
}

func (s *AntigravityAuthorization) ValidateRefreshToken(ctx context.Context, refreshToken string, proxyID *int64) (*AntigravityTokenInfo, error) {
	operation, done, err := s.activity.begin(ctx, fmt.Errorf("antigravity authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	return s.validateRefreshToken(operation, refreshToken, proxyID)
}

func (s *AntigravityAuthorization) RefreshProviderToken(ctx context.Context, provider *Record) (*AntigravityTokenInfo, error) {
	operation, done, err := s.activity.begin(ctx, fmt.Errorf("antigravity authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	return s.refreshProviderToken(operation, provider)
}

func (s *AntigravityAuthorization) FillProjectID(ctx context.Context, provider *Record, accessToken string) (string, error) {
	operation, done, err := s.activity.begin(ctx, fmt.Errorf("antigravity authorization is stopped"))
	if err != nil {
		return "", err
	}
	defer done()
	return s.fillProjectID(operation, provider, accessToken)
}
