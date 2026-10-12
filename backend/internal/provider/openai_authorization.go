package provider

import (
	"context"
	"crypto/subtle"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// OpenAI code exchange binds a submitted state value to the PKCE verifier and
// client parameters captured for its authorization URL. Invalid or expired
// sessions and mismatched state are rejected before upstream exchange. A
// successful response removes the session before optional account enrichment,
// while exchange errors retain it for another attempt. Lookup and deletion
// remain separate, so this sequence does not atomically claim a session
// against competing exchanges.
//
// Account enrichment can observe a workspace different from the personal
// account described by token metadata. A nonblank token plan is retained, and
// an account expiry accompanies that plan only for matching or
// indistinguishable identities. Otherwise subscription lookup uses the
// personal account first. Payload decoding supplies metadata hints rather than
// signature authentication; authorization callbacks and the upstream exchange
// determine accepted credentials.

// OpenAIAuthOptions supplies authorization defaults and callbacks used by the
// shared OpenAI service.
type OpenAIAuthOptions struct {
	// ClientID supplies exchange's default when the stored session client is
	// blank after trimming. DefaultRedirectURI supplies URL generation's selected
	// redirect when input is empty, and that value is saved in the session.
	ClientID, DefaultRedirectURI string
	// GenerateState, GenerateCodeVerifier and GenerateSessionID run before session
	// publication. Each must be callable; its error aborts URL creation before Set.
	// GenerateState creates authorization state proof. GenerateCodeVerifier creates
	// the PKCE verifier secret. GenerateSessionID creates the session lookup identity.
	GenerateState, GenerateCodeVerifier, GenerateSessionID func() (string, error)
	// GenerateCodeChallenge derives the URL proof from the generated PKCE verifier.
	// It must be callable during URL creation; the verifier itself stays in Sessions.
	GenerateCodeChallenge func(string) string
	// OAuthClientConfigByPlatform supplies the stored client ID after platform
	// normalization. Its boolean result is ignored here; URL construction selects flow details.
	OAuthClientConfigByPlatform func(string) (string, bool)
	// BuildAuthorizationURLForPlatform receives state, PKCE challenge, redirect and
	// normalized platform in that order, pairing the URL with the stored session parameters.
	BuildAuthorizationURLForPlatform func(string, string, string, string) string
	// ProxyAvailable gates provider-refresh proxy lookup. URL generation and code
	// exchange instead call ProxyURL whenever their input contains a proxy ID.
	ProxyAvailable func() bool
	// ProxyURL reports a URL and whether the proxy exists. Generation and exchange
	// propagate errors; provider refresh retains an empty argument after lookup failure.
	ProxyURL func(context.Context, int64) (string, bool, error)
	// Exchange receives code, verifier, redirect, proxy, client ID and router ID
	// in that order. Successful responses must be nonnil, and the callback owns I/O cancellation.
	Exchange func(context.Context, string, string, string, string, string, int64) (*wire.OAuthTokenResponse, error)
	// Refresh receives refresh token, proxy, client ID, router ID and optional
	// provider context. Imported-token calls pass nil provider; success supplies a nonnil response.
	Refresh func(context.Context, string, string, string, int64, *Record) (*wire.OAuthTokenResponse, error)
	// ParseIDToken and DecodeIDToken extract account metadata and organization hints.
	// Production bindings check expiry in ParseIDToken and decode payloads directly in
	// DecodeIDToken; both production functions leave JWT signature verification to authentication.
	// ParseIDToken extracts returned ID-token claims. DecodeIDToken extracts the
	// access-token organization hint when token metadata did not supply an organization.
	// Successful parsing returns nonnil claims for the subsequent field access.
	ParseIDToken, DecodeIDToken func(string) (*wire.OAuthIDTokenClaims, error)
	// PrivacyAvailable gates all account, subscription and training enrichment.
	// When it returns true, the three enrichment callbacks must also be callable.
	PrivacyAvailable func() bool
	// FetchProviderInfo returns optional account metadata for token, proxy and
	// organization. Nil leaves token metadata available, while personal-plan identity controls expiry adoption.
	FetchProviderInfo func(context.Context, string, string, string) *wire.ChatGPTAccountInfo
	// FetchSubscription supplies expiry for the selected account ID after account
	// enrichment needs a subscription lookup. Empty results retain any already known expiry.
	FetchSubscription func(context.Context, string, string, string) string
	// DisableTraining returns the reported training-setting result after account
	// and subscription attempts. The returned string replaces tokenInfo.PrivacyMode directly.
	DisableTraining func(context.Context, string, string) string
	// ValidatePAT validates a supplied access token using the selected proxy and
	// returns nonnil PAT metadata on success for subsequent credential assembly.
	// Production uses whoami validation with required identity and FedRAMP fields.
	ValidatePAT func(context.Context, string, string) (*OpenAITokenInfo, error)
	// Warn is required when optional ID-token parsing fails. The service logs
	// that metadata error while retaining the successful upstream token response.
	Warn func(string, ...any)
}

// OpenAIAuthorization coordinates PKCE sessions, token metadata enrichment and
// tracked authorization operations.
type OpenAIAuthorization struct {
	// activity tracks accepted callbacks through completion and rejects registrations
	// after stopping. Cancellation alone cannot force a callback to return or undo its effects.
	activity operationActivity
	// Sessions is borrowed and initialized for URL generation and exchange.
	// Authorization owns starting and stopping its cleanup worker after operation tracking drains.
	Sessions *OpenAISessionStore
	// Options is configured before concurrent use. Its callbacks and captured
	// dependencies remain shared and need concurrency handling appropriate to their operations.
	Options OpenAIAuthOptions
}

// NewOpenAIAuthorization stores the supplied sessions and callback options for
// shared authorization work. Configure callbacks before concurrent use;
// construction leaves session cleanup idle until Start is called and borrows
// the supplied session store.
func NewOpenAIAuthorization(sessions *OpenAISessionStore, options OpenAIAuthOptions) *OpenAIAuthorization {
	return &OpenAIAuthorization{Sessions: sessions, Options: options}
}

// openAIInt64Value returns the pointed value or zero when the optional ID is
// absent. Token exchange adapters interpret nonpositive router IDs as having
// no selected TLS-router options.
func openAIInt64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

// generateAuthURL returns an upstream authorization URL and stores the
// parameters needed to exchange its eventual code. State, verifier and
// session-ID generation and any selected proxy lookup complete before
// publishing the session, so their failures leave no registered entry.
//
// An empty redirect receives DefaultRedirectURI, and every supplied platform
// normalizes to OpenAI before client and URL selection. The immutable session
// retains state, verifier, client, redirect and resolved proxy under the
// generated ID, pairing the later submitted proof with the original PKCE
// exchange parameters. Supply initialized Sessions and callable generation
// callbacks.
func (s *OpenAIAuthorization) generateAuthURL(ctx context.Context, proxyID *int64, redirectURI, platform string) (*OpenAIAuthURLResult, error) {
	// Generate all PKCE and lookup values before publishing their association;
	// failed generation cannot leave a stored session with only part of its exchange proof.
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

	// URL generation and later code exchange need the same redirect, so select
	// the default here and retain that chosen value in the published session.
	if redirectURI == "" {
		redirectURI = s.Options.DefaultRedirectURI
	}
	normalizedPlatform := NormalizeOpenAIOAuthPlatform(platform)
	clientID, _ := s.Options.OAuthClientConfigByPlatform(normalizedPlatform)

	// Retain the selected client, redirect and proxy beside the verifier and state;
	// the code-exchange round trip can then recover this URL's complete parameter set.
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

// exchangeCode validates session age and state, exchanges the supplied code
// and returns assembled token metadata. Supply nonnil input and initialized
// callbacks; state is required and compared byte-for-byte with the stored
// session before any exchange.
//
// A found input proxy overrides the session proxy, while an absent lookup
// retains it and an error aborts. A nonempty redirect overrides the stored
// redirect. The trimmed session client ID wins, with ClientID used when it is
// blank. Exchange failure leaves the session available; a successful response
// removes it before enrichment, even when optional ID-token parsing fails.
//
// Get and Delete are separate operations, so simultaneous callers can reach
// exchange with the same session. Cancellation is passed to callbacks, and a
// successful callback that returns late still proceeds through this flow.
// Metadata parsing here does not authenticate JWT signatures.
func (s *OpenAIAuthorization) exchangeCode(ctx context.Context, input *OpenAIExchangeCodeInput) (*OpenAITokenInfo, error) {
	// Session lookup checks age before providing the stored proof, so expiry
	// remains effective even when the periodic eviction worker has not removed this entry.
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

	// A found input proxy overrides the stored exchange route; an absent lookup
	// leaves the session URL available rather than discarding that earlier selection.
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

	// A supplied redirect can match a caller's completed authorization flow;
	// omitted input retains the exact redirect captured when its session was generated.
	redirectURI := session.RedirectURI
	if input.RedirectURI != "" {
		redirectURI = input.RedirectURI
	}
	// Sessions constructed without a client identity still need a usable exchange
	// client; trimming lets their blank value select the service default below.
	clientID := strings.TrimSpace(session.ClientID)
	if clientID == "" {
		clientID = s.Options.ClientID
	}

	tokenResp, err := s.Options.Exchange(ctx, input.Code, session.CodeVerifier, redirectURI, proxyURL, clientID, openAIInt64Value(input.TLSFingerprintRouterID))
	if err != nil {
		return nil, err
	}

	// ID-token hints enrich a successful upstream result. Parsing errors remain
	// metadata failures, so log them and retain the returned access and refresh tokens.
	var userInfo *wire.OAuthUserInfo
	if tokenResp.IDToken != "" {
		claims, parseErr := s.Options.ParseIDToken(tokenResp.IDToken)
		if parseErr != nil {
			s.Options.Warn("openai_oauth_id_token_parse_failed", "error", parseErr)
		} else {
			userInfo = claims.GetUserInfo()
		}
	}

	// Remove future lookup access after upstream success, including metadata-parse
	// failure; exchange errors return earlier so the same stored proof can be retried.
	s.Sessions.Delete(input.SessionID)

	tokenInfo := &OpenAITokenInfo{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		IDToken:      tokenResp.IDToken,
		ExpiresIn:    tokenResp.ExpiresIn,
		// Convert response lifetime seconds into the absolute credential-expiry timestamp.
		ExpiresAt: time.Now().Unix() + tokenResp.ExpiresIn,
		ClientID:  clientID,
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

// refreshTokenWithClientIDAndRouter returns refreshed metadata for imported
// OAuth credentials using the supplied client, proxy and optional router. Nil
// router IDs become zero, and the callback determines how an empty client ID
// is interpreted.
func (s *OpenAIAuthorization) refreshTokenWithClientIDAndRouter(ctx context.Context, refreshToken string, proxyURL string, clientID string, routerID *int64) (*OpenAITokenInfo, error) {
	return s.refreshTokenWithParameters(ctx, refreshToken, proxyURL, clientID, openAIInt64Value(routerID), nil)
}

// refreshTokenWithParameters returns token and account metadata after Refresh
// succeeds with the supplied exchange parameters. A provider supplies its
// configured TLS router when routerID is nonpositive; imported credentials can
// pass nil provider.
//
// Optional ID-token parsing failures are logged while the successful token
// response remains usable. Response lifetime seconds are added to the assembly
// time's Unix timestamp, including zero or negative values. A nonblank input
// client ID is trimmed into the result. Enrichment then
// mutates the assembled metadata; callbacks receive cancellation but own its
// I/O handling.
func (s *OpenAIAuthorization) refreshTokenWithParameters(ctx context.Context, refreshToken string, proxyURL string, clientID string, routerID int64, provider *Record) (*OpenAITokenInfo, error) {
	// Provider-backed refresh retains its configured router when no positive
	// selection was supplied, while imported-token calls have no provider binding to recover.
	if provider != nil && routerID <= 0 {
		routerID = provider.GetTLSFingerprintRouterID()
	}
	tokenResp, err := s.Options.Refresh(ctx, refreshToken, proxyURL, clientID, routerID, provider)
	if err != nil {
		return nil, err
	}

	// ID-token hints enrich a successful upstream result. Parsing errors remain
	// metadata failures, so log them and retain the returned access and refresh tokens.
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
		// Convert response lifetime seconds into the absolute credential-expiry timestamp.
		ExpiresAt: time.Now().Unix() + tokenResp.ExpiresIn,
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

// enrichTokenInfo supplements tokenInfo in place and records the
// training-setting result through optional privacy infrastructure. Empty
// access tokens or unavailable privacy support skip all enrichment, and
// callers keep this mutable result exclusively owned during the operation.
//
// Organization selection first uses the token metadata, then a decoded
// access-token poid hint. A nonblank token plan wins over account lookup.
// Account expiry can accompany that retained plan when account IDs match
// case-insensitively after trimming, or either ID is missing for
// compatibility. An expiry supplied for a mismatched identity triggers personal
// subscription lookup even if an expiry was already present. A plan adopted
// from account lookup uses that same account's expiry.
//
// Subscription lookup prefers the token's personal account ID before its
// organization and the organization hint. Empty lookup results leave earlier
// metadata available. Training configuration is attempted after those lookups,
// and payload decoding supplies hints without authenticating the access token.
func (s *OpenAIAuthorization) enrichTokenInfo(ctx context.Context, tokenInfo *OpenAITokenInfo, proxyURL string) {
	if tokenInfo.AccessToken == "" || !s.Options.PrivacyAvailable() {
		return
	}

	// Decoded poid supplies an organization hint when token metadata omitted it;
	// this payload extraction selects account lookup context and does not authenticate the JWT.
	orgID := tokenInfo.OrganizationID
	if orgID == "" {
		// An unavailable payload hint leaves the optional organization selector empty;
		// metadata lookup still proceeds because decoding is separate from accepting the upstream token.
		if atClaims, err := s.Options.DecodeIDToken(tokenInfo.AccessToken); err == nil && atClaims.OpenAIAuth != nil {
			orgID = atClaims.OpenAIAuth.POID
		}
	}
	// A retained personal plan needs expiry from the same account or the
	// compatibility missing-ID case; a mismatched account expiry forces subscription lookup.
	forcePersonalSubscriptionLookup := false
	if info := s.Options.FetchProviderInfo(ctx, tokenInfo.AccessToken, proxyURL, orgID); info != nil {
		// accounts/check can select a workspace with its own plan name. Fill a
		// missing token plan from that observation while retaining any nonblank token plan.
		appliedProviderInfoPlanType := ShouldApplyChatGPTAccountInfoPlanType(tokenInfo.PlanType, info.PlanType)
		if appliedProviderInfoPlanType {
			tokenInfo.PlanType = info.PlanType
		}
		// Pair adopted account plans with that account's expiry. When the token plan
		// remains, compare identities before accepting expiry so workspace metadata cannot mislabel it.
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

	// Attempt training configuration after metadata lookups so their empty results
	// still permit this privacy action; publish its reported outcome on the token result.
	tokenInfo.PrivacyMode = s.Options.DisableTraining(ctx, tokenInfo.AccessToken, proxyURL)
}

// ShouldApplyChatGPTAccountInfoPlanType reports whether a nonblank candidate
// can fill an empty current plan after trimming. Existing nonblank token
// metadata remains authoritative, even when account lookup reports a different
// plan.
func ShouldApplyChatGPTAccountInfoPlanType(current, candidate string) bool {
	return strings.TrimSpace(candidate) != "" && strings.TrimSpace(current) == ""
}

// ChatGPTAccountInfoBelongsToTokenProvider compares nonnil token and account
// metadata identities after trimming, using a case-insensitive match. A
// missing ID on either side returns true for compatibility, allowing the
// enrichment caller to pair that account expiry with the retained plan.
func ChatGPTAccountInfoBelongsToTokenProvider(tokenInfo *OpenAITokenInfo, info *wire.ChatGPTAccountInfo) bool {
	personalID := strings.TrimSpace(tokenInfo.ChatGPTAccountID)
	sourceID := strings.TrimSpace(info.ProviderID)
	if personalID == "" || sourceID == "" {
		return true
	}
	return strings.EqualFold(personalID, sourceID)
}

// ResolveChatGPTSubscriptionProviderID returns the first nonblank trimmed
// personal account, organization or organization-hint value from nonnil
// tokenInfo. An empty result leaves subscription lookup without an account
// selector; personal identity takes priority over workspace hints.
func ResolveChatGPTSubscriptionProviderID(tokenInfo *OpenAITokenInfo, orgID string) string {
	// Personal identity wins over workspace and decoded organization hints, so
	// a retained personal plan's expiry lookup targets that account whenever it is known.
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

// refreshProviderToken returns authorization metadata for a nonnil OpenAI
// record whose type is exactly OAuth. PAT mode validates its stored access
// token, renewable OAuth uses Refresh, and access-only records are enriched
// with their stored metadata, including parsed expiry that may already be
// past.
//
// A selected provider proxy is looked up when available; failures or missing
// results leave the proxy argument empty. Missing both access and refresh
// tokens produces the refresh-token error, while PAT mode has its own
// missing-access error. Returned metadata is assembled independently of
// credential persistence, which belongs to callers.
func (s *OpenAIAuthorization) refreshProviderToken(ctx context.Context, provider *Record) (*OpenAITokenInfo, error) {
	if provider.Platform != PlatformOpenAI {
		return nil, infraerrors.New(400, "OPENAI_OAUTH_INVALID_PROVIDER", "provider is not an OpenAI provider")
	}
	if provider.Type != ProviderTypeOAuth {
		return nil, infraerrors.New(400, "OPENAI_OAUTH_INVALID_PROVIDER_TYPE", "provider is not an OAuth provider")
	}

	var proxyURL string
	// Provider refresh can proceed after proxy lookup failure; pass an empty
	// proxy argument to the callback so optional lookup does not discard stored credentials.
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
			// Access-only imports can still supply account metadata through enrichment;
			// retain their token and recorded fields while bypassing a missing refresh exchange.
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
				// Report remaining whole seconds, truncating fractional lifetime toward zero.
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

// NormalizeOpenAIOAuthPlatform returns OpenAI for every supplied platform
// value. Authorization URL generation therefore selects one OpenAI client
// flow, and callers enforce endpoint eligibility before asking this helper to
// choose parameters.
func NormalizeOpenAIOAuthPlatform(platform string) string {
	return PlatformOpenAI
}

// Start enables authorization-session eviction unless the service's activity
// state has permanently stopped. Nil services or session stores return
// immediately, and repeated calls delegate worker reuse to the session store.
//
// The activity mutex remains held across the stop-state check and session
// startup, so shutdown cannot set the service barrier between those two steps.
// Session startup records its worker before releasing its own runtime guard.
// Authorization exchanges remain tracked independently of whether periodic
// eviction has started.
func (s *OpenAIAuthorization) Start() {
	if s == nil || s.Sessions == nil {
		return
	}
	// Keep the activity guard across session startup so the service stop barrier
	// cannot be published between this eligibility check and worker registration.
	s.activity.mu.Lock()
	defer s.activity.mu.Unlock()
	if s.activity.stopped {
		return
	}
	s.Sessions.Start()
}

// StopContext rejects new authorization work, cancels registered calls and
// waits for their completion before stopping session eviction. A nil service
// returns nil; active shutdown waits require a nonnil context.
//
// The first activity-stop result is retained, including a deadline failure.
// Concurrent stop waiters can time out before that retained result is published.
// When the owner's wait fails, session cleanup remains running and subsequent calls
// return the retained failure after the stop result is published. Successful
// activity drain calls Sessions.Stop, whose worker join has no context
// deadline. Callbacks control cancellation responsiveness, and a canceled call
// returning success can still complete its local authorization effects before
// leaving activity tracking.
func (s *OpenAIAuthorization) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	// Drain tracked operations before stopping their session dependency. A retained
	// timeout returns here while session eviction remains running for the coordinator to handle.
	if err := s.activity.stop(ctx, "OpenAI authorization"); err != nil {
		return err
	}
	if s.Sessions != nil {
		s.Sessions.Stop()
	}
	return nil
}

// GenerateAuthURL creates a PKCE session and URL under authorization shutdown
// tracking. Supply a nonnil context and initialized service; cancellation and
// ErrProbeStopped are checked at registration, while accepted work receives a
// context canceled by shutdown.
func (s *OpenAIAuthorization) GenerateAuthURL(ctx context.Context, proxyID *int64, redirectURI, platform string) (*OpenAIAuthURLResult, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrProbeStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	return s.generateAuthURL(ctx, proxyID, redirectURI, platform)
}

// ExchangeCode returns token metadata after validating a nonnil input's stored
// session and required state proof. Supply a nonnil context and keep input
// stable; shutdown tracks the accepted exchange and rejects later registration
// with ErrProbeStopped.
func (s *OpenAIAuthorization) ExchangeCode(ctx context.Context, input *OpenAIExchangeCodeInput) (*OpenAITokenInfo, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrProbeStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	return s.exchangeCode(ctx, input)
}

// RefreshTokenWithClientIDAndRouter returns refreshed imported credentials
// under authorization shutdown tracking. Supply a nonnil context; optional
// router IDs and raw client/proxy parameters reach the configured refresh
// callback, and a stopped service rejects registration.
func (s *OpenAIAuthorization) RefreshTokenWithClientIDAndRouter(ctx context.Context, refreshToken string, proxyURL string, clientID string, routerID *int64) (*OpenAITokenInfo, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrProbeStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	return s.refreshTokenWithClientIDAndRouter(ctx, refreshToken, proxyURL, clientID, routerID)
}

// RefreshProviderToken returns metadata according to a nonnil OpenAI OAuth
// record's PAT, refresh-token or access-only mode. Supply a nonnil context and
// stable record; accepted work is tracked through callback completion and
// stopped registration returns ErrProbeStopped.
func (s *OpenAIAuthorization) RefreshProviderToken(ctx context.Context, provider *Record) (*OpenAITokenInfo, error) {
	ctx, finish, err := s.activity.begin(ctx, ErrProbeStopped)
	if err != nil {
		return nil, err
	}
	defer finish()
	return s.refreshProviderToken(ctx, provider)
}

// EnrichTokenInfo mutates nonnil tokenInfo with account and privacy results
// under authorization activity tracking. Supply a nonnil context and exclusive
// access to the result; canceled or stopped registration skips enrichment and
// returns no error value.
func (s *OpenAIAuthorization) EnrichTokenInfo(ctx context.Context, tokenInfo *OpenAITokenInfo, proxyURL string) {
	ctx, finish, err := s.activity.begin(ctx, ErrProbeStopped)
	if err != nil {
		return
	}
	defer finish()
	s.enrichTokenInfo(ctx, tokenInfo, proxyURL)
}

// ValidatePersonalAccessToken returns the configured PAT validation result
// under authorization shutdown tracking. Supply a nonnil context and callable
// ValidatePAT; production bindings validate the at- prefix and whoami
// metadata, while stopped registration returns ErrProbeStopped.
func (s *OpenAIAuthorization) ValidatePersonalAccessToken(ctx context.Context, token, proxyURL string) (*OpenAITokenInfo, error) {
	ctx, done, err := s.activity.begin(ctx, ErrProbeStopped)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.Options.ValidatePAT(ctx, token, proxyURL)
}
