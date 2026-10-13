package provider

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	wiregrok "github.com/TokenFlux/TokenRouter/internal/protocol/grok"
)

// Grok browser authorization binds a returned code to the saved state, PKCE
// verifier, redirect, and client identity. Validation finishes before the
// session is claimed, then the claim is consumed and the session is deleted
// even when the upstream exchange fails. A pasted callback can supply missing
// state, while a submitted redirect must agree with the saved callback.
//
// Token responses become OAuth credentials with separate metadata rules.
// ID-token identity claims fill empty fields before access-token claims, and
// the access token supplies subscription tier. Missing lifetime selects six
// hours; refresh responses can retain a previous refresh token and stored tier.
// Public exchange operations stay registered until their actual return so
// shutdown can cancel and wait for callbacks, including callbacks that finish
// after cancellation.

// grokDefaultAccessTokenTTL supplies a response lifetime when the vendor omits
// a positive expires_in; absolute expiry is computed when the response is
// assembled.
const grokDefaultAccessTokenTTL = 6 * time.Hour

// GrokAuthorizationClient exchanges temporary browser, refresh, password, or
// SSO inputs for upstream responses.
type GrokAuthorizationClient interface {
	// ExchangeCode exchanges code, verifier, redirect, proxy URL, and
	// client
	// identity in that order. The authorization caller validates a nonblank
	// returned access token before assembling credentials; ctx carries
	// cancellation.
	ExchangeCode(context.Context, string, string, string, string, string) (*wiregrok.TokenResponse, error)
	// RefreshToken exchanges renewable token, proxy URL, and client
	// identity in
	// that order. The caller validates the returned access token and
	// retains
	// submitted renewal text when the response omits it.
	RefreshToken(context.Context, string, string, string) (*wiregrok.TokenResponse, error)
	// LoginWithPassword exchanges email, password, and proxy URL for
	// temporary
	// login information. The authorization caller requires a nonblank
	// returned SSO
	// token before the second OAuth exchange; ctx carries cancellation.
	LoginWithPassword(context.Context, string, string, string) (*wiregrok.PasswordLoginResult, error)
	// ConvertSSOToBuild exchanges web SSO and proxy URL for a Build OAuth
	// response. The caller validates nonblank access text before credential
	// assembly and keeps its raw SSO input temporary.
	ConvertSSOToBuild(context.Context, string, string) (*wiregrok.TokenResponse, error)
}

// GrokAuthorizationOptions supplies platform parsing, request construction,
// identity generation, and proxy lookup.
type GrokAuthorizationOptions struct {
	// PasswordAuthEnabled is required by capabilities and password
	// authorization.
	// NewGrokAuthorization retains supplied callbacks, so owners configure
	// each
	// callback before invoking its corresponding operation.
	// password authorization policy callback
	PasswordAuthEnabled func() bool
	// LookupProxy is optional when no proxy ID is supplied. Selected IDs
	// need
	// a found result; absent lookup, lookup error, and missing proxy have
	// distinct authorization failures.
	// selected proxy URL lookup
	LookupProxy func(context.Context, int64) (string, bool, error)
	// GenerateState, GenerateNonce, GenerateCodeVerifier, and
	// GenerateSessionID
	// create one browser flow's proof and lookup values. Session IDs need
	// to be
	// fresh for outstanding flows because the store writes by ID.
	// state proof generator, authorization nonce generator, PKCE verifier
	// generator, and stored flow identity generator
	GenerateState, GenerateNonce, GenerateCodeVerifier, GenerateSessionID func() (string, error)
	// EffectiveRedirectURI normalizes the callback before URL construction
	// and
	// storage. GenerateCodeChallenge receives the same verifier later used
	// for
	// exchange, keeping the browser challenge tied to stored proof.
	// effective callback URI builder and PKCE verifier challenge builder
	EffectiveRedirectURI, GenerateCodeChallenge func(string) string
	// browser authorization URL builder
	BuildAuthorizationURL func(string, string, string, string) (string, error)
	// EffectiveClientID and EffectiveScope supply stored browser metadata.
	// EffectiveClientID also fills a blank client identity when response
	// information is assembled; callers provide both reached callbacks.
	// OAuth client identity supplier and requested OAuth scope supplier
	EffectiveClientID, EffectiveScope func() string
	// pasted code and state parser
	ParseAuthorizationInput func(string) wiregrok.AuthorizationInput
	// JWT payload metadata decoder
	DecodeJWTClaims func(string) map[string]any
	// string-valued JWT claim reader
	JWTClaimString func(map[string]any, string) string
	// access-token subscription tier parser
	SubscriptionTierFromJWT func(string) string
	// DefaultClientID is selected for SSO token information before
	// effective-client defaulting. DefaultCLIBaseURL is written by
	// credential
	// construction, with custom endpoint retention handled by later
	// refresh/import
	// callers.
	// SSO OAuth client default and stored CLI endpoint default
	DefaultClientID, DefaultCLIBaseURL string
}

// GrokAuthorization coordinates stored browser sessions and lifecycle-tracked
// OAuth exchanges.
type GrokAuthorization struct {
	// Store is constructed locally by NewGrokAuthorization and can be
	// supplied by
	// assembly before use. Session fields remain stable while browser
	// completion
	// and cleanup share them.
	Store *GrokSessionStore // shared browser PKCE session store
	// Client is retained for registered upstream exchanges. A missing
	// client
	// produces a configuration error when a reached exchange validates it.
	Client GrokAuthorizationClient // upstream OAuth exchange dependency
	// Options keeps caller-supplied callbacks with no normalization here.
	// Configure them before use and keep them stable while registered
	// operations read them.
	Options GrokAuthorizationOptions // retained platform behavior callbacks
	// activity registers public exchange operations until their callbacks
	// return.
	// StopContext cancels those operations after session cleanup stops and
	// retains
	// the first drain result.
	activity operationActivity // exchange admission and completion tracker
}

// GrokOAuthCapabilities reports which administrator authorization methods the
// process enables.
type GrokOAuthCapabilities struct {
	PasswordAuthEnabled bool `json:"password_auth_enabled"` // process password authorization enabled
}

// GrokAuthURLResult pairs the browser URL with its stored session proof.
type GrokAuthURLResult struct {
	// AuthURL, SessionID, and State refer to the same stored browser flow.
	// The
	// caller submits its session ID with returned proof so completion can
	// recover
	// the saved verifier and redirect.
	AuthURL   string `json:"auth_url"`   // browser OAuth authorization address
	SessionID string `json:"session_id"` // stored PKCE flow identity
	State     string `json:"state"`      // returned session state proof
}

// GrokExchangeCodeInput supplies returned authorization proof and optional
// session transport choices.
type GrokExchangeCodeInput struct {
	SessionID string // stored PKCE flow identity
	Code      string // pasted authorization code input
	// State takes precedence over parsed callback state when nonblank. An
	// absent
	// submitted value uses parsed state before comparison with the saved
	// session.
	State string // submitted session state proof
	// RedirectURI may be blank to use the stored callback. A nonblank
	// trimmed
	// value must match that callback before the session can be consumed.
	RedirectURI string // submitted callback URI confirmation
	// ProxyID overrides the saved proxy URL when supplied and resolved.
	// Lookup
	// errors or an absent proxy abort completion before the session is
	// consumed.
	ProxyID *int64 // optional exchange proxy identity
}

// GrokTokenInfo carries exchanged OAuth credentials and accepted account
// metadata for callers.
type GrokTokenInfo struct {
	AccessToken string `json:"access_token"` // exchanged upstream access token
	// RefreshToken can be absent in an exchange response. refreshToken
	// retains
	// its trimmed submitted renewal value on that path, while initial
	// authorization can leave this field blank.
	RefreshToken string `json:"refresh_token,omitempty"` // renewable upstream token value
	IDToken      string `json:"id_token,omitempty"`      // upstream identity metadata JWT
	TokenType    string `json:"token_type,omitempty"`    // reported OAuth token scheme
	// ExpiresIn and ExpiresAt describe lifetime and absolute expiry
	// computed when
	// the response is assembled. A nonpositive upstream lifetime selects
	// six hours
	// before that timestamp is calculated.
	ExpiresIn int64  `json:"expires_in"`          // seconds
	ExpiresAt int64  `json:"expires_at"`          // Unix-seconds
	ClientID  string `json:"client_id,omitempty"` // selected OAuth client identity
	Scope     string `json:"scope,omitempty"`     // exchanged OAuth scope text
	// Email, Subject, and TeamID fill from ID-token claims before
	// access-token
	// claims and then optional existing metadata. A populated earlier value
	// keeps
	// precedence during that assembly.
	Email   string `json:"email,omitempty"`   // accepted account email claim
	Subject string `json:"sub,omitempty"`     // accepted account subject claim
	TeamID  string `json:"team_id,omitempty"` // accepted upstream team claim
	// SubscriptionTier comes from the access-token parser; provider refresh
	// retains the stored tier when that new response supplies no usable
	// tier text.
	SubscriptionTier string `json:"subscription_tier,omitempty"` // accepted access-token tier signal
	// EntitlementStatus is retained from the stored provider during refresh
	// when
	// newly assembled token information leaves this value blank.
	EntitlementStatus string `json:"entitlement_status,omitempty"` // stored account entitlement label
}

// GrokPasswordLoginResult aliases the temporary upstream password-to-SSO
// exchange response.
type GrokPasswordLoginResult = wiregrok.PasswordLoginResult

// NewGrokAuthorization retains client and platform options and constructs a
// local PKCE session store. Callers bind reached callbacks and start cleanup
// separately; this constructor supplies no default platform parsers.
func NewGrokAuthorization(client GrokAuthorizationClient, options GrokAuthorizationOptions) *GrokAuthorization {
	return &GrokAuthorization{Store: NewGrokSessionStore(nil), Client: client, Options: options}
}

// Start asks the retained session store to launch cleanup once. The store
// serializes repeated starts and prevents restarting after Stop; token
// operations begin on demand through their own lifecycle registrations.
func (s *GrokAuthorization) Start() { s.Store.Start() }

// StopContext stops session cleanup, then cancels and waits for registered
// OAuth work using ctx as the activity-drain budget. The first activity-stop
// result is retained; the preceding session-cleanup wait itself has no caller
// deadline.
func (s *GrokAuthorization) StopContext(ctx context.Context) error {
	s.Store.Stop()
	return s.activity.stop(ctx, "grok authorization")
}

// proxyURL resolves a supplied proxy ID or returns an empty URL for nil.
// Missing lookup, read failure, and absent proxy become distinct public errors
// before an upstream exchange is attempted.
func (s *GrokAuthorization) proxyURL(ctx context.Context, id *int64) (string, error) {
	if id == nil {
		return "", nil
	}
	if s.Options.LookupProxy == nil {
		return "", infraerrors.New(infraerrors.CategoryBadRequest, "GROK_OAUTH_PROXY_NOT_AVAILABLE", "proxy repository is not available")
	}
	value, found, err := s.Options.LookupProxy(ctx, *id)
	if err != nil {
		return "", infraerrors.New(infraerrors.CategoryServiceUnavailable, "GROK_OAUTH_PROXY_LOOKUP_FAILED", "proxy lookup is temporarily unavailable")
	}
	if !found {
		return "", infraerrors.New(infraerrors.CategoryBadRequest, "GROK_OAUTH_PROXY_NOT_FOUND", "configured proxy was not found")
	}
	return value, nil
}

// GetCapabilities reads the configured password authorization flag for
// administrator responses.
func (s *GrokAuthorization) GetCapabilities() GrokOAuthCapabilities {
	return GrokOAuthCapabilities{PasswordAuthEnabled: s.Options.PasswordAuthEnabled()}
}

// generateAuthURL constructs a browser URL and stores its state, verifier,
// callback, proxy, and client inputs under a generated session ID. Generation,
// proxy lookup, or URL construction failures return before storage.
//
// The challenge is derived from the verifier retained for completion, and the
// effective redirect is used for both URL construction and the saved session.
// Store.Set may select local storage after a remote write failure. Owners
// provide fresh IDs and usable callbacks; public registration tracks this
// preparation through its return. Dependencies control cancellation; a
// successful late preparation can still publish its session before returning.
func (s *GrokAuthorization) generateAuthURL(ctx context.Context, proxyID *int64, redirectURI string) (*GrokAuthURLResult, error) {
	state, err := s.Options.GenerateState()
	if err != nil {
		return nil, infraerrors.Newf(infraerrors.CategoryInternalServer, "GROK_OAUTH_STATE_FAILED", "failed to generate state: %v", err)
	}
	nonce, err := s.Options.GenerateNonce()
	if err != nil {
		return nil, infraerrors.Newf(infraerrors.CategoryInternalServer, "GROK_OAUTH_NONCE_FAILED", "failed to generate nonce: %v", err)
	}
	codeVerifier, err := s.Options.GenerateCodeVerifier()
	if err != nil {
		return nil, infraerrors.Newf(infraerrors.CategoryInternalServer, "GROK_OAUTH_VERIFIER_FAILED", "failed to generate code verifier: %v", err)
	}
	sessionID, err := s.Options.GenerateSessionID()
	if err != nil {
		return nil, infraerrors.Newf(infraerrors.CategoryInternalServer, "GROK_OAUTH_SESSION_FAILED", "failed to generate session ID: %v", err)
	}

	proxyURL, err := s.proxyURL(ctx, proxyID)
	if err != nil {
		return nil, err
	}
	redirectURI = s.Options.EffectiveRedirectURI(redirectURI)
	codeChallenge := s.Options.GenerateCodeChallenge(codeVerifier)

	authURL, err := s.Options.BuildAuthorizationURL(state, codeChallenge, redirectURI, nonce)
	if err != nil {
		return nil, infraerrors.Newf(infraerrors.CategoryBadRequest, "GROK_OAUTH_INVALID_AUTHORIZE_URL", "%v", err)
	}

	// Save the verifier and effective callback used to build this URL
	// before
	// returning its lookup identity, so completion recovers this same
	// browser
	// flow rather than reconstructing proof.
	s.Store.Set(sessionID, &GrokOAuthSession{
		State: state,

		CodeVerifier: codeVerifier,

		CodeChallenge: codeChallenge,

		ClientID: s.Options.EffectiveClientID(),

		Scope: s.Options.EffectiveScope(),

		ProxyURL: proxyURL,

		RedirectURI: redirectURI,

		CreatedAt: time.Now(),
	})

	return &GrokAuthURLResult{
		AuthURL:   authURL,
		SessionID: sessionID,
		State:     state,
	}, nil
}

// exchangeCode validates submitted proof against a retrieved PKCE session and
// exchanges the returned code using its saved verifier, callback, and client. A
// parsed callback supplies state only when the submitted State is blank; a
// nonblank submitted redirect must match the saved one.
//
// Proxy override and client availability are resolved before consumption. The
// store then claims the session once, and deferred deletion applies even when
// exchange or response validation fails. A claimed failed exchange therefore
// cannot simply reuse this session. The returned token information comes from a
// validated nonblank access-token response, with metadata parsing handled
// separately.
func (s *GrokAuthorization) exchangeCode(ctx context.Context, input *GrokExchangeCodeInput) (*GrokTokenInfo, error) {
	if input == nil {
		return nil, infraerrors.New(infraerrors.CategoryBadRequest, "GROK_OAUTH_INVALID_INPUT", "input is required")
	}
	session, ok := s.Store.Get(input.SessionID)
	if !ok {
		return nil, infraerrors.New(infraerrors.CategoryBadRequest, "GROK_OAUTH_SESSION_NOT_FOUND", "session not found or expired")
	}

	parsed := s.Options.ParseAuthorizationInput(input.Code)
	code := strings.TrimSpace(parsed.Code)
	if code == "" {
		return nil, infraerrors.New(infraerrors.CategoryBadRequest, "GROK_OAUTH_CODE_REQUIRED", "authorization code is required")
	}
	// Pasted callbacks can carry state alongside the code. Submitted
	// nonblank
	// state wins, then comparison uses the saved proof before one-use
	// consumption is attempted.
	state := strings.TrimSpace(input.State)
	if state == "" {
		state = strings.TrimSpace(parsed.State)
	}
	if state == "" {
		return nil, infraerrors.New(infraerrors.CategoryBadRequest, "GROK_OAUTH_STATE_REQUIRED", "oauth state is required")
	}
	if subtle.ConstantTimeCompare([]byte(state), []byte(session.State)) != 1 {
		return nil, infraerrors.New(infraerrors.CategoryBadRequest, "GROK_OAUTH_INVALID_STATE", "invalid oauth state")
	}
	if redirectURI := strings.TrimSpace(input.RedirectURI); redirectURI != "" &&
		redirectURI != strings.TrimSpace(session.RedirectURI) {
		return nil, infraerrors.New(infraerrors.CategoryBadRequest, "GROK_OAUTH_REDIRECT_URI_MISMATCH", "redirect_uri does not match the OAuth session")
	}

	proxyURL := session.ProxyURL
	if input.ProxyID != nil {
		var err error
		proxyURL, err = s.proxyURL(ctx, input.ProxyID)
		if err != nil {
			return nil, err
		}
	}
	if err := s.requireOAuthClient(); err != nil {
		return nil, err
	}
	// Claim after validating proof and transport, then delete even on
	// exchange
	// failure; another request cannot reuse this accepted browser
	// completion
	// through the same consumption store.
	if !s.Store.TryConsumeSession(input.SessionID) {
		return nil, infraerrors.New(infraerrors.CategoryBadRequest, "GROK_OAUTH_SESSION_ALREADY_USED", "oauth session has already been used")
	}
	defer s.Store.Delete(input.SessionID)
	tokenResp, err := s.Client.ExchangeCode(ctx, code, session.CodeVerifier, session.RedirectURI, proxyURL, session.ClientID)
	if err != nil {
		return nil, err
	}
	if err := validateGrokTokenResponse(tokenResp); err != nil {
		return nil, err
	}
	return s.tokenInfoFromResponse(tokenResp, session.ClientID, nil), nil
}

// requireOAuthClient reports a configured exchange client or returns the public
// configuration error. Nil receivers and missing Client are rejected, allowing
// reached exchange paths to diagnose unavailable OAuth transport.
func (s *GrokAuthorization) requireOAuthClient() error {
	if s == nil || s.Client == nil {
		return infraerrors.New(infraerrors.CategoryInternalServer, "GROK_OAUTH_CLIENT_NOT_CONFIGURED", "oauth client is not configured")
	}
	return nil
}

// refreshToken exchanges a trimmed nonblank refresh token using the supplied
// proxy and client identity. It validates the returned access token and retains
// the submitted refresh token when the response omits a replacement.
func (s *GrokAuthorization) refreshToken(ctx context.Context, refreshToken, proxyURL, clientID string) (*GrokTokenInfo, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, infraerrors.New(infraerrors.CategoryBadRequest, "GROK_OAUTH_NO_REFRESH_TOKEN", "refresh_token is required")
	}
	if err := s.requireOAuthClient(); err != nil {
		return nil, err
	}
	tokenResp, err := s.Client.RefreshToken(ctx, refreshToken, proxyURL, clientID)
	if err != nil {
		return nil, err
	}
	if err := validateGrokTokenResponse(tokenResp); err != nil {
		return nil, err
	}
	tokenInfo := s.tokenInfoFromResponse(tokenResp, clientID, nil)
	// An exchange can rotate only the access token. Retain the renewable
	// credential so later refresh and persistence still have a token to
	// use.
	if tokenInfo.RefreshToken == "" {
		tokenInfo.RefreshToken = refreshToken
	}
	return tokenInfo, nil
}

// validateSSOToken exchanges a trimmed web SSO value for Build OAuth
// information through the selected proxy. Blank input or unusable client/proxy
// fails before exchange; the returned credentials contain exchanged token
// fields.
func (s *GrokAuthorization) validateSSOToken(ctx context.Context, ssoToken string, proxyID *int64) (*GrokTokenInfo, error) {
	ssoToken = strings.TrimSpace(ssoToken)
	if ssoToken == "" {
		return nil, infraerrors.New(infraerrors.CategoryBadRequest, "GROK_OAUTH_NO_SSO_TOKEN", "sso_token is required")
	}
	if err := s.requireOAuthClient(); err != nil {
		return nil, err
	}
	proxyURL, err := s.proxyURL(ctx, proxyID)
	if err != nil {
		return nil, err
	}
	tokenResp, err := s.Client.ConvertSSOToBuild(ctx, ssoToken, proxyURL)
	if err != nil {
		return nil, err
	}
	if err := validateGrokTokenResponse(tokenResp); err != nil {
		return nil, err
	}
	return s.tokenInfoFromResponse(tokenResp, s.Options.DefaultClientID, nil), nil
}

// convertFromSSO delegates batch-import SSO exchange to validateSSOToken and
// returns its token information. The selected proxy and validation errors are
// shared with direct SSO authorization; provider creation happens afterward.
func (s *GrokAuthorization) convertFromSSO(ctx context.Context, ssoToken string, proxyID *int64) (*GrokTokenInfo, error) {
	return s.validateSSOToken(ctx, ssoToken, proxyID)
}

// authorizePassword performs enabled email/password login and then exchanges
// its returned SSO value for OAuth information. Email is trimmed, password is
// passed as supplied after a nonblank check, and an absent returned SSO fails.
//
// Each phase resolves the supplied proxy ID separately, so completion uses the
// lookup result reached by that phase. The OAuth result supplies the returned
// token identity first; only its blank Email inherits the password login's
// Email. Other login metadata is left outside the assembled OAuth result.
func (s *GrokAuthorization) authorizePassword(ctx context.Context, email, password string, proxyID *int64) (*GrokTokenInfo, error) {
	if !s.Options.PasswordAuthEnabled() {
		return nil, infraerrors.New(infraerrors.CategoryForbidden, "GROK_OAUTH_PASSWORD_AUTH_DISABLED", "Grok password authorization is disabled")
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, infraerrors.New(infraerrors.CategoryBadRequest, "GROK_OAUTH_EMAIL_REQUIRED", "email is required")
	}
	if strings.TrimSpace(password) == "" {
		return nil, infraerrors.New(infraerrors.CategoryBadRequest, "GROK_OAUTH_PASSWORD_REQUIRED", "password is required")
	}
	if err := s.requireOAuthClient(); err != nil {
		return nil, err
	}
	proxyURL, err := s.proxyURL(ctx, proxyID)
	if err != nil {
		return nil, err
	}
	loginResult, err := s.Client.LoginWithPassword(ctx, email, password, proxyURL)
	if err != nil {
		return nil, err
	}
	if loginResult == nil || strings.TrimSpace(loginResult.SSOToken) == "" {
		return nil, infraerrors.New(infraerrors.CategoryBadGateway, "GROK_OAUTH_PASSWORD_LOGIN_FAILED", "grok password login did not return sso_token")
	}
	// Exchange the temporary SSO proof through its own proxy lookup; the
	// login
	// and OAuth calls can therefore observe different lookup results for
	// one ID.
	info, err := s.validateSSOToken(ctx, loginResult.SSOToken, proxyID)
	if err != nil {
		return nil, err
	}
	// Keep the OAuth account email when present; the login response fills
	// only
	// that missing identity hint before this combined result leaves
	// authorization.
	if strings.TrimSpace(info.Email) == "" {
		info.Email = loginResult.Email
	}
	return info, nil
}

// validateGrokTokenResponse rejects nil responses and whitespace-only access
// tokens before token information is assembled. Other response fields may be
// absent, leaving lifetime, scheme, and metadata handling to the assembler.
func validateGrokTokenResponse(tokenResp *wiregrok.TokenResponse) error {
	if tokenResp == nil || strings.TrimSpace(tokenResp.AccessToken) == "" {
		return infraerrors.New(infraerrors.CategoryBadGateway, "GROK_OAUTH_INVALID_TOKEN_RESPONSE", "grok oauth token response missing access_token")
	}
	return nil
}

// refreshProviderToken exchanges credentials for a Grok OAuth record after
// validating its kind, proxy, and renewable token. Validation and exchange
// failures return before stored subscription metadata is combined with the
// response.
//
// A nonblank tier extracted from the refreshed access token wins over the
// stored subscription tier. Blank tier and entitlement results inherit those
// stored values separately; the response assembler itself leaves entitlement
// blank. This merge supplies metadata for later credential construction and
// leaves identity validation and persistence to their respective callers.
func (s *GrokAuthorization) refreshProviderToken(ctx context.Context, provider *Record) (*GrokTokenInfo, error) {
	if provider == nil || provider.Platform != PlatformGrok {
		return nil, infraerrors.New(infraerrors.CategoryBadRequest, "GROK_OAUTH_INVALID_PROVIDER", "provider is not a Grok provider")
	}
	if provider.Type != ProviderTypeOAuth {
		return nil, infraerrors.New(infraerrors.CategoryBadRequest, "GROK_OAUTH_INVALID_PROVIDER_TYPE", "provider is not an OAuth provider")
	}

	proxyURL, err := s.proxyURL(ctx, provider.ProxyID)
	if err != nil {
		return nil, err
	}
	refreshToken := provider.GetCredential("refresh_token")
	if strings.TrimSpace(refreshToken) == "" {
		return nil, infraerrors.New(infraerrors.CategoryBadRequest, "GROK_OAUTH_NO_REFRESH_TOKEN", "no refresh token available")
	}

	clientID := provider.GetCredential("client_id")
	tokenInfo, err := s.refreshToken(ctx, refreshToken, proxyURL, clientID)
	if err != nil {
		return nil, err
	}
	// The refreshed access token's tier takes precedence over stored
	// metadata.
	// Retain the stored tier when the token has no tier claim or is opaque.
	if strings.TrimSpace(tokenInfo.SubscriptionTier) == "" {
		tokenInfo.SubscriptionTier = provider.GetCredential("subscription_tier")
	}
	if strings.TrimSpace(tokenInfo.EntitlementStatus) == "" {
		tokenInfo.EntitlementStatus = provider.GetCredential("entitlement_status")
	}
	return tokenInfo, nil
}

// BuildProviderCredentials creates a scalar credential map or returns nil for
// nil information. It always formats ExpiresAt as UTC RFC3339, including zero,
// and writes DefaultCLIBaseURL; nonempty optional metadata is added as
// supplied.
func (s *GrokAuthorization) BuildProviderCredentials(tokenInfo *GrokTokenInfo) map[string]any {
	if tokenInfo == nil {
		return nil
	}
	// Encode absolute token expiry in the format shared credential readers
	// accept.
	expiresAt := time.Unix(tokenInfo.ExpiresAt, 0).UTC().Format(time.RFC3339)
	creds := map[string]any{
		"access_token": tokenInfo.AccessToken,
		"expires_at":   expiresAt,
	}
	if tokenInfo.RefreshToken != "" {
		creds["refresh_token"] = tokenInfo.RefreshToken
	}
	if tokenInfo.TokenType != "" {
		creds["token_type"] = tokenInfo.TokenType
	}
	if tokenInfo.IDToken != "" {
		creds["id_token"] = tokenInfo.IDToken
	}
	if tokenInfo.ClientID != "" {
		creds["client_id"] = tokenInfo.ClientID
	}
	if tokenInfo.Scope != "" {
		creds["scope"] = tokenInfo.Scope
	}
	if tokenInfo.Email != "" {
		creds["email"] = tokenInfo.Email
	}
	if tokenInfo.Subject != "" {
		creds["sub"] = tokenInfo.Subject
	}
	if tokenInfo.TeamID != "" {
		creds["team_id"] = tokenInfo.TeamID
	}
	if tokenInfo.SubscriptionTier != "" {
		creds["subscription_tier"] = tokenInfo.SubscriptionTier
	}
	if tokenInfo.EntitlementStatus != "" {
		creds["entitlement_status"] = tokenInfo.EntitlementStatus
	}
	creds["base_url"] = s.Options.DefaultCLIBaseURL
	return creds
}

// tokenInfoFromResponse assembles nonnil response fields, a computed expiry,
// and accepted account claims. Nonpositive lifetime selects six hours; blank
// client identity and token type use the effective client and Bearer
// respectively.
//
// ID-token claims fill identity before access-token claims, while tier comes
// from the access token. Optional existing metadata fills remaining email,
// subject, and team gaps. This parsing supplies hints after exchange; it does
// not independently authenticate the JWT. Callers keep the response and any
// existing map stable during assembly.
func (s *GrokAuthorization) tokenInfoFromResponse(tokenResp *wiregrok.TokenResponse, clientID string, existing map[string]any) *GrokTokenInfo {
	now := time.Now()
	expiresIn := tokenResp.ExpiresIn
	// Missing positive lifetime selects the configured six-hour response
	// default.
	if expiresIn <= 0 {
		expiresIn = int64(grokDefaultAccessTokenTTL.Seconds())
	}
	info := &GrokTokenInfo{
		AccessToken: tokenResp.AccessToken,

		RefreshToken: tokenResp.RefreshToken,

		IDToken: tokenResp.IDToken,

		TokenType: tokenResp.TokenType,

		ExpiresIn: expiresIn,

		ExpiresAt: now.Add(time.Duration(expiresIn) * time.Second).Unix(),

		ClientID: strings.TrimSpace(clientID),

		Scope: tokenResp.Scope,
	}
	if info.ClientID == "" {
		info.ClientID = s.Options.EffectiveClientID()
	}
	if info.TokenType == "" {
		info.TokenType = "Bearer"
	}
	s.applyGrokTokenClaims(info, tokenResp.IDToken, false)
	s.applyGrokTokenClaims(info, tokenResp.AccessToken, true)
	if existing != nil {
		if info.Email == "" {
			if email, _ := existing["email"].(string); email != "" {
				info.Email = email
			}
		}
		if info.Subject == "" {
			if subject, _ := existing["sub"].(string); subject != "" {
				info.Subject = subject
			}
		}
		if info.TeamID == "" {
			if teamID, _ := existing["team_id"].(string); teamID != "" {
				info.TeamID = teamID
			}
		}
	}
	return info
}

// applyGrokTokenClaims fills blank identity fields from decoded token metadata
// and optionally replaces subscription tier. Nil information, blank token, or
// undecodable claims skips that source. Already populated email, subject, and
// team fields keep their earlier values, allowing the assembler's
// ID-token-first order to determine precedence. Callers provide reached decoder
// and claim callbacks.
//
// includeTier selects access-token tier handling separately. A nonblank tier
// replaces an earlier tier even when identity fields already contain values,
// keeping that precedence independent of missing identity hints.
func (s *GrokAuthorization) applyGrokTokenClaims(info *GrokTokenInfo, token string, includeTier bool) {
	if info == nil || strings.TrimSpace(token) == "" {
		return
	}
	// Opaque or undecodable token text contributes no metadata. Keep
	// earlier
	// identity values so later accepted sources can fill any remaining
	// gaps.
	claims := s.Options.DecodeJWTClaims(token)
	if claims == nil {
		return
	}
	if info.Email == "" {
		info.Email = s.Options.JWTClaimString(claims, "email")
	}
	if info.Subject == "" {
		info.Subject = s.Options.JWTClaimString(claims, "sub")
	}
	if info.TeamID == "" {
		info.TeamID = s.Options.JWTClaimString(claims, "team_id")
	}
	if includeTier {
		if tier := s.Options.SubscriptionTierFromJWT(token); tier != "" {
			info.SubscriptionTier = tier
		}
	}
}

// GenerateAuthURL registers browser preparation with caller and shutdown
// cancellation and returns its URL, session ID, and state. A stopped service
// rejects admission; preparation uses the retained platform callbacks and stays
// registered until it returns.
func (s *GrokAuthorization) GenerateAuthURL(ctx context.Context, proxyID *int64, redirectURI string) (*GrokAuthURLResult, error) {
	operation, done, err := s.activity.begin(ctx, errors.New("grok authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	return s.generateAuthURL(operation, proxyID, redirectURI)
}

// ExchangeCode registers browser completion and returns validated exchanged
// token information. It enforces saved proof and one-use consumption through
// exchangeCode; stopped services reject new completion while admitted callbacks
// remain tracked through their return.
func (s *GrokAuthorization) ExchangeCode(ctx context.Context, input *GrokExchangeCodeInput) (*GrokTokenInfo, error) {
	operation, done, err := s.activity.begin(ctx, errors.New("grok authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	return s.exchangeCode(operation, input)
}

// RefreshToken registers a renewable-token exchange with caller and shutdown
// cancellation. It returns validated token information, retaining the submitted
// refresh token when omitted upstream; stopped services reject new work.
func (s *GrokAuthorization) RefreshToken(ctx context.Context, refreshToken, proxyURL, clientID string) (*GrokTokenInfo, error) {
	operation, done, err := s.activity.begin(ctx, errors.New("grok authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	return s.refreshToken(operation, refreshToken, proxyURL, clientID)
}

// ValidateSSOToken registers direct web-SSO validation and returns exchanged
// OAuth information. It resolves the selected proxy and rejects blank input
// before transport; shutdown cancellation tracks the dependency until it
// actually returns.
func (s *GrokAuthorization) ValidateSSOToken(ctx context.Context, ssoToken string, proxyID *int64) (*GrokTokenInfo, error) {
	operation, done, err := s.activity.begin(ctx, errors.New("grok authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	return s.validateSSOToken(operation, ssoToken, proxyID)
}

// ConvertFromSSO registers the SSO exchange used by batch provider imports. It
// returns OAuth information before provider creation, with caller/shutdown
// cancellation tracked through the exchange; stopped services reject admission.
func (s *GrokAuthorization) ConvertFromSSO(ctx context.Context, ssoToken string, proxyID *int64) (*GrokTokenInfo, error) {
	operation, done, err := s.activity.begin(ctx, errors.New("grok authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	return s.convertFromSSO(operation, ssoToken, proxyID)
}

// AuthorizePassword registers the optional password-to-SSO-to-OAuth operation
// and returns its exchanged credentials. Process policy is checked before
// login, and shutdown cancels registered work while waiting for the dependency
// calls to return.
func (s *GrokAuthorization) AuthorizePassword(ctx context.Context, email, password string, proxyID *int64) (*GrokTokenInfo, error) {
	operation, done, err := s.activity.begin(ctx, errors.New("grok authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	return s.authorizePassword(operation, email, password, proxyID)
}

// RefreshProviderToken registers renewal for a supplied Grok OAuth record and
// returns token information. Platform/type, renewable token, and proxy checks
// precede exchange; stopped services reject admission and successful callers
// persist credentials separately.
func (s *GrokAuthorization) RefreshProviderToken(ctx context.Context, provider *Record) (*GrokTokenInfo, error) {
	operation, done, err := s.activity.begin(ctx, errors.New("grok authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	return s.refreshProviderToken(operation, provider)
}
