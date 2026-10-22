package provider

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

// antigravity_authorization.go exchanges and refreshes Antigravity credentials
// and discovers account metadata using stored authorization proof.
// Antigravity authorization binds a Google code to stored state, verifier, and
// proxy before credential exchange. Successful exchange consumes that session
// before best-effort email, project, subscription, and privacy discovery, so
// account metadata failures can accompany usable tokens.
//
// Project loading retains the latest observed subscription even when project
// retries are exhausted, and onboarding requires an upstream default tier.
// Refresh failures stop immediately for permanent OAuth errors or configured
// connection failures; other failures use finite retries with unconditional
// sleeps. Public operations register separately with activity for shutdown
// cancellation and waiting. Session lookup and deletion surround a remote
// call, so callers serialize exchanges for one session and keep callbacks and
// borrowed inputs stable through completion.

// AntigravityAuthorizationClient executes token, project, account, and privacy
// protocol requests.
type AntigravityAuthorizationClient interface {
	// ExchangeCode exchanges the supplied code and verifier for credentials.
	// Return nonnil token data on success because authorization consumes its
	// session and reads expiry before any metadata discovery.
	ExchangeCode(context.Context, string, string) (*google.AntigravityTokenResponse, error)
	// RefreshToken renews the supplied refresh credential through this client.
	// A successful result must be nonnil; the authorization workflow derives
	// its expiry margin before returning the renewed tokens.
	RefreshToken(context.Context, string) (*google.AntigravityTokenResponse, error)
	// GetUserInfo queries email metadata with the supplied access token.
	// Successful data must be nonnil; request errors leave the exchanged token
	// usable and are reported as account discovery diagnostics.
	GetUserInfo(context.Context, string) (*google.AntigravityUserInfo, error)
	// LoadCodeAssist reads project and subscription data with its raw response.
	// The workflow can retain a nonnil typed response despite an error, and raw
	// allowedTiers supplies onboarding when an error-free load lacks a project.
	LoadCodeAssist(context.Context, string) (*google.AntigravityLoadCodeAssistResponse, map[string]any, error)
	// OnboardUser requests a project using an access token and selected default
	// tier.
	OnboardUser(context.Context, string, string) (string, error)
	// SetUserSettings clears account settings for privacy setup using the token.
	// Successful data must be nonnil; SetPrivacy checks key absence before it
	// can proceed to project-associated verification.
	SetUserSettings(context.Context, string) (*google.AntigravitySetUserSettingsResponse, error)
	// FetchUserInfo queries settings for the access token and project identity.
	// SetPrivacy uses its response predicate for verification, which accepts nil
	// data as private when the request itself succeeds.
	FetchUserInfo(context.Context, string, string) (*google.AntigravityFetchUserInfoResponse, error)
}

// AntigravityAuthorizationOptions binds PKCE generation, transport selection,
// client creation, and diagnostics.
type AntigravityAuthorizationOptions struct {
	// NewClient constructs a usable client for each selected proxy URL. It is
	// required by all protocol flows and returns nonnil clients when successful.
	NewClient func(string) (AntigravityAuthorizationClient, error)
	// ResolveProxy is required for selected ProxyID values. False availability
	// retains the saved exchange proxy or leaves a new flow URL empty.
	ResolveProxy func(context.Context, int64) (string, bool)
	// GenerateState, GenerateCodeVerifier, and GenerateSessionID are required for
	// setup. Their outputs tie state and verifier to one stored proof, with
	// errors stopping setup before publication.
	// GenerateState: OAuth state generator; GenerateCodeVerifier: PKCE verifier
	// generator; GenerateSessionID: proof storage identifier generator.
	GenerateState, GenerateCodeVerifier, GenerateSessionID func() (string, error)
	// GenerateCodeChallenge and BuildAuthorizationURL are required after proof
	// generation. The challenge uses the saved verifier, and URL construction
	// uses the paired state so later code exchange can recover the same proof.
	// configured PKCE challenge derivation
	GenerateCodeChallenge func(string) string
	// configured browser URL construction
	BuildAuthorizationURL func(string, string) string
	// IsConnectionError is required after retryable refresh failure. A true
	// result returns the proxy-unavailable wrapper immediately instead of another
	// attempt.
	IsConnectionError func(error) bool
	// Printf is required for reached discovery and renewal diagnostics. Its
	// callback result is unavailable to this workflow, which retains credential
	// outcomes independently.
	Printf func(string, ...any)
	// Warn and Info are required by reached privacy paths. Warn records failed
	// setting or verification work; Info records a setting result accepted by
	// SetPrivacy after the project-associated verification predicate.
	// Warn: privacy failure diagnostics; Info: accepted privacy diagnostics.
	Warn, Info func(string, ...any)
}

// AntigravityAuthorization owns session proof and tracked Antigravity account
// authorization operations.
// @project-doc docs/interfaces/antigravity_upstream.md#antigravity_native_execution
type AntigravityAuthorization struct {
	// Store holds borrowed proof pointers and owns cleanup lifecycle. The
	// constructor initializes it, and successful exchanges delete their selected
	// session.
	Store *AntigravityAuthorizationSessions
	// Options copies callback values at construction. Configure the shared
	// service before activity and keep callbacks stable until registered
	// operations return.
	Options  AntigravityAuthorizationOptions
	activity operationActivity // registered operation cancellation state
}

// AntigravityAuthURLResult pairs a browser destination with stored proof
// identifiers.
type AntigravityAuthURLResult struct {
	// generated browser authorization URL
	AuthURL   string `json:"auth_url"`
	SessionID string `json:"session_id"` // stored proof lookup identifier
	State     string `json:"state"`      // returned OAuth state proof
}

// AntigravityExchangeCodeInput supplies saved state proof and the upstream
// authorization code.
type AntigravityExchangeCodeInput struct {
	SessionID string // stored proof lookup identifier
	State     string // supplied OAuth state proof
	Code      string // supplied authorization code text
	// ProxyID overrides saved transport only when its lookup succeeds.
	// An unresolved selection retains the proxy associated with the session proof.
	ProxyID *int64
}

// AntigravityTokenInfo carries refreshed credentials and independently
// observed account metadata.
type AntigravityTokenInfo struct {
	// exchanged OAuth access token
	AccessToken string `json:"access_token"`
	// optional token renewal credential
	RefreshToken string `json:"refresh_token"`
	// upstream token lifetime seconds
	ExpiresIn int64 `json:"expires_in"`
	// absolute Unix-second refresh expiry
	ExpiresAt int64  `json:"expires_at"`
	TokenType string `json:"token_type"` // upstream token type name
	// discovered upstream account email
	Email string `json:"email,omitempty"`
	// discovered project execution identifier
	ProjectID string `json:"project_id,omitempty"`
	// ProjectIDMissing marks incomplete project discovery for caller handling.
	// Exchange and validation set it on lookup failure even when subscription
	// metadata remains usable with the exchanged token.
	ProjectIDMissing bool   `json:"-"`
	PlanType         string `json:"-"` // normalized subscription plan label
	PrivacyMode      string `json:"-"` // privacy setup observation marker
}

// loadCodeAssistResult retains a project and the latest observed subscription
// metadata.
type loadCodeAssistResult struct {
	ProjectID string // discovered project execution identifier
	// Subscription can survive retry exhaustion alongside a project error.
	// Callers inspect that independently useful observation even when ProjectID
	// remains empty.
	Subscription *AntigravitySubscriptionResult
}

// NewAntigravityAuthorization copies option callbacks and creates idle session
// storage. Configure reached client, generator, resolver, and logging
// callbacks before use; Start enables cleanup, and StopContext owns the
// service shutdown.
func NewAntigravityAuthorization(options AntigravityAuthorizationOptions) *AntigravityAuthorization {
	return &AntigravityAuthorization{Store: NewAntigravityAuthorizationSessions(), Options: options}
}

// Start enables the authorization store cleanup worker through its lifecycle
// gate. Repeated calls are harmless, and a store stopped by StopContext cannot
// admit another cleanup worker.
func (s *AntigravityAuthorization) Start() { s.Store.Start() }

// StopContext stops and joins session cleanup before canceling registered
// authorization operations. Pass a nonnil context for the activity wait; its
// first completed result, including a timeout, is retained by later calls.
func (s *AntigravityAuthorization) StopContext(ctx context.Context) error {
	s.Store.Stop()
	return s.activity.stop(ctx, "antigravity authorization")
}

// generateAuthURL prepares and stores state, verifier, and proxy for one
// returned authorization URL. Required generator failures stop before
// publication, and unresolved selected proxy lookup leaves its URL empty.
//
// The challenge is derived from the exact verifier stored under SessionID,
// while the returned State identifies the same session. Store publication
// precedes URL construction, making the proof available to later ExchangeCode.
// Callers retain stable callbacks and serialize attempts to consume the
// returned session, whose age gate belongs to
// AntigravityAuthorizationSessions.
func (s *AntigravityAuthorization) generateAuthURL(ctx context.Context, proxyID *int64) (*AntigravityAuthURLResult, error) {
	// Prepare proof before storing it so generator failures leave no partial
	// session, and challenge plus returned state identify the same verifier on
	// exchange.
	state, err := s.Options.GenerateState()
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}

	codeVerifier, err := s.Options.GenerateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("failed to generate code_verifier: %w", err)
	}

	sessionID, err := s.Options.GenerateSessionID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate session_id: %w", err)
	}

	// New proof has no saved transport to recover. An unavailable selected
	// proxy leaves its URL empty for the client used by later code exchange.
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

// exchangeCode validates a nonnil input against live saved state and exchanges
// its code. Blank or unequal state fails before client work; resolved input
// proxy overrides the saved proxy, while unresolved lookup retains it.
//
// A successful token response consumes the session before metadata discovery
// and subtracts five minutes from absolute expiry without a minimum lifetime.
// Email and project failures retain tokens; project failure sets
// ProjectIDMissing and may still return subscription metadata. Privacy setup
// then contributes its own status. Callers serialize same-session exchange and
// configure clients to return nonnil successful token and user responses.
func (s *AntigravityAuthorization) exchangeCode(ctx context.Context, input *AntigravityExchangeCodeInput) (*AntigravityTokenInfo, error) {
	session, ok := s.Store.Get(input.SessionID)
	if !ok {
		return nil, fmt.Errorf("session not found or expired")
	}

	if strings.TrimSpace(input.State) == "" || input.State != session.State {
		return nil, fmt.Errorf("invalid state")
	}

	// A resolved request proxy overrides saved transport. Unavailable lookup
	// retains the session proxy so the exchange stays usable through that chosen
	// route.
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

	// Pair the submitted code with the saved verifier after validating state.
	// This prevents an input from selecting a different locally stored proof for
	// exchange.
	tokenResp, err := client.ExchangeCode(ctx, input.Code, session.CodeVerifier)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange token: %w", err)
	}

	// Consume proof before account metadata work so discovery failures cannot
	// leave a successfully exchanged authorization code reusable through this
	// stored session.
	s.Store.Delete(input.SessionID)

	// Subtract five minutes for early refresh; short upstream lifetimes can
	// therefore produce an already elapsed expiry.
	expiresAt := time.Now().Unix() + tokenResp.ExpiresIn - 300

	result := &AntigravityTokenInfo{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresIn:    tokenResp.ExpiresIn,
		ExpiresAt:    expiresAt,
		TokenType:    tokenResp.TokenType,
	}

	// Email discovery failure retains the exchanged token and logs the missing
	// account hint, allowing project and privacy discovery to continue afterward.
	userInfo, err := client.GetUserInfo(ctx, tokenResp.AccessToken)
	if err != nil {
		s.Options.Printf("[AntigravityOAuth] Warning: failed to get user information: %v\n", err)
	} else {
		result.Email = userInfo.Email
	}

	// Project exhaustion can still return subscription data from a load response.
	// Keep that metadata with the token while marking missing project for caller
	// handling.
	loadResult, loadErr := s.loadProjectIDWithRetry(ctx, tokenResp.AccessToken, proxyURL, 3)
	if loadErr != nil {
		s.Options.Printf("[AntigravityOAuth] Warning: failed to get project_id after retries: %v\n", loadErr)
		result.ProjectIDMissing = true
	}
	if loadResult != nil {
		result.ProjectID = loadResult.ProjectID
		if loadResult.Subscription != nil {
			result.PlanType = loadResult.Subscription.PlanType
		}
	}

	// Privacy setup follows project discovery and contributes a separate mode.
	// Failure retains the tokens while callers decide how to store that observation.
	result.PrivacyMode = s.SetPrivacy(ctx, result.AccessToken, result.ProjectID, proxyURL)

	return result, nil
}

// refreshToken attempts token renewal at most four times with one-, two-, and
// four-second sleeps after failed attempts. Permanent OAuth errors, configured
// connection failures, and client-construction errors return immediately;
// sleeps ignore cancellation, while cooperative protocol requests receive ctx.
// Successful token data must be nonnil and uses a five-minute expiry margin
// with no lower lifetime bound.
func (s *AntigravityAuthorization) refreshToken(ctx context.Context, refreshToken, proxyURL string) (*AntigravityTokenInfo, error) {
	var lastErr error

	for attempt := range 4 {
		if attempt > 0 {
			backoff := min(time.Duration(1<<uint(attempt-1))*time.Second, 30*time.Second)
			// Retry after a failed attempt with finite spacing. The sleep ignores
			// cancellation, so shutdown joins actual completion after this delay
			// returns.
			time.Sleep(backoff)
		}

		client, err := s.Options.NewClient(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("create antigravity client failed: %w", err)
		}
		tokenResp, err := client.RefreshToken(ctx, refreshToken)
		if err == nil {
			now := time.Now()
			// Reserve five minutes for early renewal, without imposing a minimum
			// lifetime.
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
		// Use the configured connection classifier after permanent OAuth checks.
		// A match ends retries with the proxy diagnostic instead of spending another
		// attempt.
		if s.Options.IsConnectionError(err) {
			return nil, fmt.Errorf("proxy unavailable: %w", err)
		}
		lastErr = err
	}

	return nil, fmt.Errorf("failed to refresh token after retries: %w", lastErr)
}

// validateRefreshToken renews a credential before querying its email, project,
// subscription, and privacy state. Unresolved selected proxy lookup uses an
// empty URL; client-construction and renewal failures stop validation.
//
// Email and project request errors leave the renewed token available. Project
// retry exhaustion sets ProjectIDMissing while preserving returned
// subscription metadata, and privacy setup records its own outcome after
// discovery. Each stage uses the selected transport and renewed token, so
// callers receive independently successful metadata alongside any incomplete
// account discovery.
func (s *AntigravityAuthorization) validateRefreshToken(ctx context.Context, refreshToken string, proxyID *int64) (*AntigravityTokenInfo, error) {
	// Without saved transport, an unavailable selected proxy leaves an empty
	// URL for client construction, so discovery follows the configured client
	// transport.
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

	// Query email with the renewed token. A request failure leaves credentials
	// available and allows subsequent project and privacy discovery to continue.
	client, err := s.Options.NewClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create antigravity client failed: %w", err)
	}
	userInfo, err := client.GetUserInfo(ctx, tokenInfo.AccessToken)
	if err != nil {
		s.Options.Printf("[AntigravityOAuth] Warning: failed to get user information: %v\n", err)
	} else {
		tokenInfo.Email = userInfo.Email
	}

	// Discovery errors retain the renewed token and may retain subscription data.
	// The missing-project marker lets callers distinguish incomplete account
	// discovery from renewal failure.
	loadResult, loadErr := s.loadProjectIDWithRetry(ctx, tokenInfo.AccessToken, proxyURL, 3)
	if loadErr != nil {
		s.Options.Printf("[AntigravityOAuth] Warning: failed to get project_id after retries: %v\n", loadErr)
		tokenInfo.ProjectIDMissing = true
	}
	if loadResult != nil {
		tokenInfo.ProjectID = loadResult.ProjectID
		if loadResult.Subscription != nil {
			tokenInfo.PlanType = loadResult.Subscription.PlanType
		}
	}

	// Privacy setup follows project discovery and contributes a separate mode.
	// Failure retains the tokens while callers decide how to store that observation.
	tokenInfo.PrivacyMode = s.SetPrivacy(ctx, tokenInfo.AccessToken, tokenInfo.ProjectID, proxyURL)

	return tokenInfo, nil
}

// IsNonRetryableAntigravityOAuthError checks four case-sensitive OAuth error
// substrings. Pass a nonnil error; invalid_grant, invalid_client,
// unauthorized_client, and access_denied stop retries, while other text
// remains retryable under the caller policy.
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

// refreshProviderToken renews a nonnil Antigravity OAuth record with a
// nonblank refresh token. Its selected proxy is resolved for renewal and
// subsequent project/subscription queries; unresolved lookup uses an empty
// URL.
//
// A stored nonblank email survives token renewal. Failed project discovery
// retains the stored project and marks missing only when that stored value is
// empty. A nonblank returned project overrides it, and any returned
// subscription sets PlanType even with a discovery error. This path returns
// metadata for caller persistence and does not run privacy setup itself.
func (s *AntigravityAuthorization) refreshProviderToken(ctx context.Context, provider *Record) (*AntigravityTokenInfo, error) {
	if provider.Platform != PlatformAntigravity || provider.Type != ProviderTypeOAuth {
		return nil, fmt.Errorf("provider is not an Antigravity OAuth provider")
	}

	refreshToken := provider.GetCredential("refresh_token")
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("no refresh_token available")
	}

	// Without saved transport, an unavailable selected proxy leaves an empty
	// URL for client construction, so discovery follows the configured client
	// transport.
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

	// Renewal supplies token fields without querying email here. Keep the stored
	// nonblank email so credential rebuilding can retain the account hint.
	existingEmail := strings.TrimSpace(provider.GetCredential("email"))
	if existingEmail != "" {
		tokenInfo.Email = existingEmail
	}

	// A failed project query retains the stored project, while nonempty fresh
	// project and subscription observations override their respective returned
	// metadata independently.
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

// loadProjectIDWithRetry queries a project for an initial attempt plus
// maxRetries additional attempts. Callers supply a nonnegative limit and
// usable client factory; retries sleep exponentially up to eight seconds and
// do not wake on cancellation.
//
// Each nonnil load response replaces the retained subscription observation. A
// successful nonempty project returns immediately; an error-free load without
// one tries onboarding using its raw default tier. Exhaustion returns the
// latest subscription alongside an error when available, letting callers
// retain account metadata despite project failure. Client-construction failure
// returns immediately and does not return that retained observation.
func (s *AntigravityAuthorization) loadProjectIDWithRetry(ctx context.Context, accessToken, proxyURL string, maxRetries int) (*loadCodeAssistResult, error) {
	var lastErr error
	var lastSubscription *AntigravitySubscriptionResult

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := min(time.Duration(1<<uint(attempt-1))*time.Second, 8*time.Second)
			// Retry after a failed attempt with finite spacing. The sleep ignores
			// cancellation, so shutdown joins actual completion after this delay
			// returns.
			time.Sleep(backoff)
		}

		client, err := s.Options.NewClient(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("create antigravity client failed: %w", err)
		}
		loadResp, loadRaw, err := client.LoadCodeAssist(ctx, accessToken)

		// Retain each nonnil subscription observation even if this load fails.
		// Later missing responses cannot erase the metadata returned when project
		// retries exhaust.
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

		// An error-free load without a project may supply an onboarding default
		// in its raw response. Try that route before consuming another load attempt.
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
			lastErr = fmt.Errorf("LoadCodeAssist returned an empty response")
		} else {
			lastErr = fmt.Errorf("LoadCodeAssist returned an empty project_id")
		}
	}

	// Exhaustion retains the last nonnil subscription with the project error.
	// Callers can persist that independently useful account observation despite
	// incomplete discovery.
	if lastSubscription != nil {
		return &loadCodeAssistResult{Subscription: lastSubscription}, fmt.Errorf("failed to get project_id after %d retries: %w", maxRetries, lastErr)
	}
	return nil, fmt.Errorf("failed to get project_id after %d retries: %w", maxRetries, lastErr)
}

// tryOnboardProjectID delegates onboarding with the raw response default tier.
// Missing usable default tier fails before calling the client, and protocol
// failure is wrapped with that selected tier; successful project text is
// returned as supplied.
func tryOnboardProjectID(ctx context.Context, client AntigravityAuthorizationClient, accessToken string, loadRaw map[string]any) (string, error) {
	tierID := ResolveAntigravityDefaultTierID(loadRaw)
	if tierID == "" {
		return "", fmt.Errorf("loadCodeAssist returned no usable default tier")
	}

	projectID, err := client.OnboardUser(ctx, accessToken, tierID)
	if err != nil {
		return "", fmt.Errorf("onboardUser failed (tier=%s): %w", tierID, err)
	}
	return projectID, nil
}

// ResolveAntigravityDefaultTierID returns the first nonblank trimmed ID from a
// default entry in the raw allowedTiers array. Missing or differently typed
// containers return empty, and paid/current tier metadata does not substitute
// for that default.
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

// fillProjectID discovers project metadata with the supplied access token and
// resolved record proxy. Pass a nonnil record; it performs four load attempts
// and returns a project with the discovery error when a result is available.
func (s *AntigravityAuthorization) fillProjectID(ctx context.Context, provider *Record, accessToken string) (string, error) {
	// Without saved transport, an unavailable selected proxy leaves an empty
	// URL for client construction, so discovery follows the configured client
	// transport.
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

// BuildProviderCredentials creates stored credentials from nonnil token data.
// Access token and decimal Unix-second expiry are required entries; nonempty
// renewal, token type, email, project, and plan values are included for caller
// persistence.
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

// GenerateAuthURL registers PKCE URL setup for shutdown waiting and releases
// that registration on return. Stopped or canceled activity fails before
// setup; generateAuthURL owns saved proof and proxy fallback.
// Pass a nonnil context for operation registration.
func (s *AntigravityAuthorization) GenerateAuthURL(ctx context.Context, proxyID *int64) (*AntigravityAuthURLResult, error) {
	operation, done, err := s.activity.begin(ctx, fmt.Errorf("antigravity authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	return s.generateAuthURL(operation, proxyID)
}

// ExchangeCode registers a nonnil code input for authorization shutdown
// waiting. Stopped or canceled admission fails before exchange; exchangeCode
// owns state validation, successful session consumption, and independent
// account discovery outcomes.
// Pass a nonnil context for operation registration.
func (s *AntigravityAuthorization) ExchangeCode(ctx context.Context, input *AntigravityExchangeCodeInput) (*AntigravityTokenInfo, error) {
	operation, done, err := s.activity.begin(ctx, fmt.Errorf("antigravity authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	return s.exchangeCode(operation, input)
}

// RefreshToken registers credential renewal and releases its activity on
// return. Shutdown cancellation reaches protocol calls, while the delegated
// finite retry sleeps remain unconditional and can outlast a caller wait
// budget.
// Pass a nonnil context for operation registration.
func (s *AntigravityAuthorization) RefreshToken(ctx context.Context, refreshToken, proxyURL string) (*AntigravityTokenInfo, error) {
	operation, done, err := s.activity.begin(ctx, fmt.Errorf("antigravity authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	return s.refreshToken(operation, refreshToken, proxyURL)
}

// ValidateRefreshToken registers renewal plus account validation for shutdown
// waiting. Its delegated flow returns usable credentials with best-effort
// metadata where permitted, and the registration ends after the entire
// validation operation returns.
// Pass a nonnil context for operation registration.
func (s *AntigravityAuthorization) ValidateRefreshToken(ctx context.Context, refreshToken string, proxyID *int64) (*AntigravityTokenInfo, error) {
	operation, done, err := s.activity.begin(ctx, fmt.Errorf("antigravity authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	return s.validateRefreshToken(operation, refreshToken, proxyID)
}

// RefreshProviderToken registers renewal for a stable nonnil provider record.
// The delegated platform/type checks and stored metadata precedence apply, and
// shutdown waits for actual return of its finite exchange/discovery flow.
// Pass a nonnil context for operation registration.
func (s *AntigravityAuthorization) RefreshProviderToken(ctx context.Context, provider *Record) (*AntigravityTokenInfo, error) {
	operation, done, err := s.activity.begin(ctx, fmt.Errorf("antigravity authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	return s.refreshProviderToken(operation, provider)
}

// FillProjectID registers project discovery using a stable record and supplied
// token. Shutdown can cancel its cooperative requests; unconditional retry
// sleeps finish before the registration is removed on return.
// Pass a nonnil context for operation registration.
func (s *AntigravityAuthorization) FillProjectID(ctx context.Context, provider *Record, accessToken string) (string, error) {
	operation, done, err := s.activity.begin(ctx, fmt.Errorf("antigravity authorization is stopped"))
	if err != nil {
		return "", err
	}
	defer done()
	return s.fillProjectID(operation, provider, accessToken)
}
