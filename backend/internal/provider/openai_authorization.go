package provider

import (
	"context"
	"crypto/subtle"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

type OpenAIAuthOptions struct {
	ClientID, DefaultRedirectURI                           string
	GenerateState, GenerateCodeVerifier, GenerateSessionID func() (string, error)
	GenerateCodeChallenge                                  func(string) string
	OAuthClientConfigByPlatform                            func(string) (string, bool)
	BuildAuthorizationURLForPlatform                       func(string, string, string, string) string
	ProxyAvailable                                         func() bool
	ProxyURL                                               func(context.Context, int64) (string, bool, error)
	Exchange                                               func(context.Context, string, string, string, string, string, int64) (*wire.OAuthTokenResponse, error)
	Refresh                                                func(context.Context, string, string, string, int64, *Record) (*wire.OAuthTokenResponse, error)
	ParseIDToken, DecodeIDToken                            func(string) (*wire.OAuthIDTokenClaims, error)
	PrivacyAvailable                                       func() bool
	FetchProviderInfo                                      func(context.Context, string, string, string) *wire.ChatGPTAccountInfo
	FetchSubscription                                      func(context.Context, string, string, string) string
	DisableTraining                                        func(context.Context, string, string) string
	ValidatePAT                                            func(context.Context, string, string) (*OpenAITokenInfo, error)
	Warn                                                   func(string, ...any)
}
type OpenAIAuthorization struct {
	activity operationActivity
	Sessions *OpenAISessionStore
	Options  OpenAIAuthOptions
}

func NewOpenAIAuthorization(sessions *OpenAISessionStore, options OpenAIAuthOptions) *OpenAIAuthorization {
	return &OpenAIAuthorization{Sessions: sessions, Options: options}
}

func openAIInt64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

// generateAuthURL creates an authorization URL and stores its PKCE session.
func (s *OpenAIAuthorization) generateAuthURL(ctx context.Context, proxyID *int64, redirectURI, platform string) (*OpenAIAuthURLResult, error) {
	// Generate state and PKCE values before registering the session.
	state, err := s.Options.GenerateState()
	if err != nil {
		return nil, infraerrors.Newf(500, "OPENAI_OAUTH_STATE_FAILED", "failed to generate state: %v", err)
	}

	codeVerifier, err := s.Options.GenerateCodeVerifier()
	if err != nil {
		return nil, infraerrors.Newf(500, "OPENAI_OAUTH_VERIFIER_FAILED", "failed to generate code verifier: %v", err)
	}

	codeChallenge := s.Options.GenerateCodeChallenge(codeVerifier)

	sessionID, err := s.Options.GenerateSessionID()
	if err != nil {
		return nil, infraerrors.Newf(500, "OPENAI_OAUTH_SESSION_FAILED", "failed to generate session ID: %v", err)
	}

	var proxyURL string
	if proxyID != nil {
		resolvedProxyURL, proxyFound, err := s.Options.ProxyURL(ctx, *proxyID)
		if err != nil {
			return nil, infraerrors.Newf(400, "OPENAI_OAUTH_PROXY_NOT_FOUND", "proxy not found: %v", err)
		}
		if proxyFound {
			proxyURL = resolvedProxyURL
		}
	}

	// An empty redirect uses the configured default.
	if redirectURI == "" {
		redirectURI = s.Options.DefaultRedirectURI
	}
	normalizedPlatform := NormalizeOpenAIOAuthPlatform(platform)
	clientID, _ := s.Options.OAuthClientConfigByPlatform(normalizedPlatform)

	// Store the exchange parameters with the session.
	session := &OpenAIOAuthSession{
		State:        state,
		CodeVerifier: codeVerifier,
		ClientID:     clientID,
		RedirectURI:  redirectURI,
		ProxyURL:     proxyURL,
		CreatedAt:    time.Now(),
	}
	s.Sessions.Set(sessionID, session)

	authURL := s.Options.BuildAuthorizationURLForPlatform(state, codeChallenge, redirectURI, normalizedPlatform)

	return &OpenAIAuthURLResult{
		AuthURL:   authURL,
		SessionID: sessionID,
	}, nil
}

// exchangeCode validates state, exchanges the code, and deletes the session after a successful exchange.
func (s *OpenAIAuthorization) exchangeCode(ctx context.Context, input *OpenAIExchangeCodeInput) (*OpenAITokenInfo, error) {
	// Expired sessions cannot be exchanged.
	session, ok := s.Sessions.Get(input.SessionID)
	if !ok {
		return nil, infraerrors.New(400, "OPENAI_OAUTH_SESSION_NOT_FOUND", "session not found or expired")
	}
	if input.State == "" {
		return nil, infraerrors.New(400, "OPENAI_OAUTH_STATE_REQUIRED", "oauth state is required")
	}
	if subtle.ConstantTimeCompare([]byte(input.State), []byte(session.State)) != 1 {
		return nil, infraerrors.New(400, "OPENAI_OAUTH_INVALID_STATE", "invalid oauth state")
	}

	// A resolved input proxy overrides the session proxy.
	proxyURL := session.ProxyURL
	if input.ProxyID != nil {
		resolvedProxyURL, proxyFound, err := s.Options.ProxyURL(ctx, *input.ProxyID)
		if err != nil {
			return nil, infraerrors.Newf(400, "OPENAI_OAUTH_PROXY_NOT_FOUND", "proxy not found: %v", err)
		}
		if proxyFound {
			proxyURL = resolvedProxyURL
		}
	}

	// A nonempty input redirect overrides the session redirect.
	redirectURI := session.RedirectURI
	if input.RedirectURI != "" {
		redirectURI = input.RedirectURI
	}
	clientID := strings.TrimSpace(session.ClientID)
	if clientID == "" {
		clientID = s.Options.ClientID
	}

	tokenResp, err := s.Options.Exchange(ctx, input.Code, session.CodeVerifier, redirectURI, proxyURL, clientID, openAIInt64Value(input.TLSFingerprintRouterID))
	if err != nil {
		return nil, err
	}

	// Parse account metadata from the returned ID token when available.
	var userInfo *wire.OAuthUserInfo
	if tokenResp.IDToken != "" {
		claims, parseErr := s.Options.ParseIDToken(tokenResp.IDToken)
		if parseErr != nil {
			s.Options.Warn("openai_oauth_id_token_parse_failed", "error", parseErr)
		} else {
			userInfo = claims.GetUserInfo()
		}
	}

	// Consume the session only after the exchange succeeds.
	s.Sessions.Delete(input.SessionID)

	tokenInfo := &OpenAITokenInfo{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		IDToken:      tokenResp.IDToken,
		ExpiresIn:    tokenResp.ExpiresIn,
		ExpiresAt:    time.Now().Unix() + tokenResp.ExpiresIn,
		ClientID:     clientID,
	}

	if userInfo != nil {
		tokenInfo.Email = userInfo.Email
		tokenInfo.ChatGPTAccountID = userInfo.ChatGPTAccountID
		tokenInfo.ChatGPTUserID = userInfo.ChatGPTUserID
		tokenInfo.OrganizationID = userInfo.OrganizationID
		tokenInfo.PlanType = userInfo.PlanType
	}

	s.enrichTokenInfo(ctx, tokenInfo, proxyURL)

	return tokenInfo, nil
}

// refreshTokenWithClientIDAndRouter refreshes imported OAuth credentials using the selected TLS router.
func (s *OpenAIAuthorization) refreshTokenWithClientIDAndRouter(ctx context.Context, refreshToken string, proxyURL string, clientID string, routerID *int64) (*OpenAITokenInfo, error) {
	return s.refreshTokenWithParameters(ctx, refreshToken, proxyURL, clientID, openAIInt64Value(routerID), nil)
}

func (s *OpenAIAuthorization) refreshTokenWithParameters(ctx context.Context, refreshToken string, proxyURL string, clientID string, routerID int64, provider *Record) (*OpenAITokenInfo, error) {
	if provider != nil && routerID <= 0 {
		routerID = provider.GetTLSFingerprintRouterID()
	}
	tokenResp, err := s.Options.Refresh(ctx, refreshToken, proxyURL, clientID, routerID, provider)
	if err != nil {
		return nil, err
	}

	// Parse account metadata from the returned ID token when available.
	var userInfo *wire.OAuthUserInfo
	if tokenResp.IDToken != "" {
		claims, parseErr := s.Options.ParseIDToken(tokenResp.IDToken)
		if parseErr != nil {
			s.Options.Warn("openai_oauth_id_token_parse_failed", "error", parseErr)
		} else {
			userInfo = claims.GetUserInfo()
		}
	}

	tokenInfo := &OpenAITokenInfo{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		IDToken:      tokenResp.IDToken,
		ExpiresIn:    tokenResp.ExpiresIn,
		ExpiresAt:    time.Now().Unix() + tokenResp.ExpiresIn,
	}
	if trimmed := strings.TrimSpace(clientID); trimmed != "" {
		tokenInfo.ClientID = trimmed
	}

	if userInfo != nil {
		tokenInfo.Email = userInfo.Email
		tokenInfo.ChatGPTAccountID = userInfo.ChatGPTAccountID
		tokenInfo.ChatGPTUserID = userInfo.ChatGPTUserID
		tokenInfo.OrganizationID = userInfo.OrganizationID
		tokenInfo.PlanType = userInfo.PlanType
	}

	s.enrichTokenInfo(ctx, tokenInfo, proxyURL)

	return tokenInfo, nil
}

// enrichTokenInfo supplements token metadata and requests disabled training on a best-effort basis.
// Account lookup fills missing plan and email values; subscription lookup supplies expiry when needed.
// Missing access tokens or unavailable privacy support skip enrichment.
func (s *OpenAIAuthorization) enrichTokenInfo(ctx context.Context, tokenInfo *OpenAITokenInfo, proxyURL string) {
	if tokenInfo.AccessToken == "" || !s.Options.PrivacyAvailable() {
		return
	}

	// Use the access token's poid when the token metadata has no organization ID.
	orgID := tokenInfo.OrganizationID
	if orgID == "" {
		if atClaims, err := s.Options.DecodeIDToken(tokenInfo.AccessToken); err == nil && atClaims.OpenAIAuth != nil {
			orgID = atClaims.OpenAIAuth.POID
		}
	}
	// A workspace expiry cannot describe a personal subscription.
	// Query the personal subscription endpoint when accounts/check returns a different account.
	forcePersonalSubscriptionLookup := false
	if info := s.Options.FetchProviderInfo(ctx, tokenInfo.AccessToken, proxyURL, orgID); info != nil {
		// Keep the personal plan from the ID token when present.
		// accounts/check can select a workspace with an internal billing plan name,
		// such as self_serve_business_usage_based, instead of the personal plan.
		appliedProviderInfoPlanType := ShouldApplyChatGPTAccountInfoPlanType(tokenInfo.PlanType, info.PlanType)
		if appliedProviderInfoPlanType {
			tokenInfo.PlanType = info.PlanType
		}
		// Plan and expiry must describe the same subscription. If the personal plan is retained,
		// accept account expiry only when the account identity matches or cannot be distinguished.
		if info.SubscriptionExpiresAt != "" {
			if appliedProviderInfoPlanType || ChatGPTAccountInfoBelongsToTokenProvider(tokenInfo, info) {
				tokenInfo.SubscriptionExpiresAt = info.SubscriptionExpiresAt
			} else {
				forcePersonalSubscriptionLookup = true
			}
		}
		if tokenInfo.Email == "" && info.Email != "" {
			tokenInfo.Email = info.Email
		}
	}
	if forcePersonalSubscriptionLookup || strings.TrimSpace(tokenInfo.SubscriptionExpiresAt) == "" {
		if expiresAt := s.Options.FetchSubscription(ctx, tokenInfo.AccessToken, proxyURL, ResolveChatGPTSubscriptionProviderID(tokenInfo, orgID)); expiresAt != "" {
			tokenInfo.SubscriptionExpiresAt = expiresAt
		}
	}

	// Record the result of the best-effort request to disable training.
	tokenInfo.PrivacyMode = s.Options.DisableTraining(ctx, tokenInfo.AccessToken, proxyURL)
}

func ShouldApplyChatGPTAccountInfoPlanType(current, candidate string) bool {
	return strings.TrimSpace(candidate) != "" && strings.TrimSpace(current) == ""
}

// ChatGPTAccountInfoBelongsToTokenProvider compares the account ID from accounts/check with token metadata.
// A missing ID on either side is treated as a match for compatibility.
func ChatGPTAccountInfoBelongsToTokenProvider(tokenInfo *OpenAITokenInfo, info *wire.ChatGPTAccountInfo) bool {
	personalID := strings.TrimSpace(tokenInfo.ChatGPTAccountID)
	sourceID := strings.TrimSpace(info.ProviderID)
	if personalID == "" || sourceID == "" {
		return true
	}
	return strings.EqualFold(personalID, sourceID)
}

func ResolveChatGPTSubscriptionProviderID(tokenInfo *OpenAITokenInfo, orgID string) string {
	for _, candidate := range []string{
		tokenInfo.ChatGPTAccountID,
		tokenInfo.OrganizationID,
		orgID,
	} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// refreshProviderToken refreshes OAuth credentials, validates PATs, or enriches an existing access token.
func (s *OpenAIAuthorization) refreshProviderToken(ctx context.Context, provider *Record) (*OpenAITokenInfo, error) {
	if provider.Platform != PlatformOpenAI {
		return nil, infraerrors.New(400, "OPENAI_OAUTH_INVALID_PROVIDER", "provider is not an OpenAI provider")
	}
	if provider.Type != ProviderTypeOAuth {
		return nil, infraerrors.New(400, "OPENAI_OAUTH_INVALID_PROVIDER_TYPE", "provider is not an OAuth provider")
	}

	var proxyURL string
	if provider.ProxyID != nil && s.Options.ProxyAvailable() {
		resolvedProxyURL, proxyFound, err := s.Options.ProxyURL(ctx, *provider.ProxyID)
		if err == nil && proxyFound {
			proxyURL = resolvedProxyURL
		}
	}

	accessToken := provider.GetCredential("access_token")
	if provider.IsOpenAIPersonalAccessToken() {
		if accessToken == "" {
			return nil, infraerrors.New(400, "OPENAI_CODEX_PAT_REQUIRED", "access token is required")
		}
		return s.Options.ValidatePAT(ctx, accessToken, proxyURL)
	}

	refreshToken := provider.GetCredential("refresh_token")
	if refreshToken == "" {
		if accessToken != "" {
			tokenInfo := &OpenAITokenInfo{
				AccessToken:           accessToken,
				RefreshToken:          "",
				IDToken:               provider.GetCredential("id_token"),
				ClientID:              provider.GetCredential("client_id"),
				Email:                 provider.GetCredential("email"),
				ChatGPTAccountID:      provider.GetCredential("chatgpt_account_id"),
				ChatGPTUserID:         provider.GetCredential("chatgpt_user_id"),
				OrganizationID:        provider.GetCredential("organization_id"),
				PlanType:              provider.GetCredential("plan_type"),
				SubscriptionExpiresAt: provider.GetCredential("subscription_expires_at"),
			}
			if expiresAt := provider.GetCredentialAsTime("expires_at"); expiresAt != nil {
				tokenInfo.ExpiresAt = expiresAt.Unix()
				tokenInfo.ExpiresIn = int64(time.Until(*expiresAt).Seconds())
			}
			s.enrichTokenInfo(ctx, tokenInfo, proxyURL)
			return tokenInfo, nil
		}
		return nil, infraerrors.New(400, "OPENAI_OAUTH_NO_REFRESH_TOKEN", "no refresh token available")
	}

	clientID := provider.GetCredential("client_id")
	return s.refreshTokenWithParameters(ctx, refreshToken, proxyURL, clientID, provider.GetTLSFingerprintRouterID(), provider)
}

func NormalizeOpenAIOAuthPlatform(platform string) string {
	return PlatformOpenAI
}

// Start starts authorization-session cleanup unless this service has stopped.
func (s *OpenAIAuthorization) Start() {
	if s == nil || s.Sessions == nil {
		return
	}
	s.activity.mu.Lock()
	defer s.activity.mu.Unlock()
	if s.activity.stopped {
		return
	}
	s.Sessions.Start()
}

// StopContext cancels and waits for authorization operations, then stops session cleanup.
func (s *OpenAIAuthorization) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if err := s.activity.stop(ctx, "OpenAI authorization"); err != nil {
		return err
	}
	if s.Sessions != nil {
		s.Sessions.Stop()
	}
	return nil
}

// GenerateAuthURL creates a tracked authorization session and URL.
func (s *OpenAIAuthorization) GenerateAuthURL(ctx context.Context, proxyID *int64, redirectURI, platform string) (*OpenAIAuthURLResult, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrProbeStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	return s.generateAuthURL(ctx, proxyID, redirectURI, platform)
}

// ExchangeCode exchanges an authorization code under the service's shutdown tracking.
func (s *OpenAIAuthorization) ExchangeCode(ctx context.Context, input *OpenAIExchangeCodeInput) (*OpenAITokenInfo, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrProbeStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	return s.exchangeCode(ctx, input)
}

// RefreshTokenWithClientIDAndRouter refreshes OAuth credentials under the service's shutdown tracking.
func (s *OpenAIAuthorization) RefreshTokenWithClientIDAndRouter(ctx context.Context, refreshToken string, proxyURL string, clientID string, routerID *int64) (*OpenAITokenInfo, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrProbeStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	return s.refreshTokenWithClientIDAndRouter(ctx, refreshToken, proxyURL, clientID, routerID)
}

// RefreshProviderToken obtains refreshed token metadata for the provider's authentication mode.
func (s *OpenAIAuthorization) RefreshProviderToken(ctx context.Context, provider *Record) (*OpenAITokenInfo, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrProbeStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	return s.refreshProviderToken(ctx, provider)
}

// EnrichTokenInfo supplements metadata in place, skipping the operation if the service has stopped.
func (s *OpenAIAuthorization) EnrichTokenInfo(ctx context.Context, tokenInfo *OpenAITokenInfo, proxyURL string) {
	ctx, finish, err := s.activity.begin(ctx, ErrProbeStopped)
	if err != nil {
		return
	}
	defer finish()
	s.enrichTokenInfo(ctx, tokenInfo, proxyURL)
}

// ValidatePersonalAccessToken validates a PAT under the authorization service's shutdown tracking.
func (s *OpenAIAuthorization) ValidatePersonalAccessToken(ctx context.Context, token, proxyURL string) (*OpenAITokenInfo, error) {
	ctx, done, err := s.activity.begin(ctx, ErrProbeStopped)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.Options.ValidatePAT(ctx, token, proxyURL)
}
