package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// Claude authorization keeps PKCE state, verifier, proxy, and scope together
// until code exchange succeeds. Full OAuth and inference-only Setup Token URLs
// share session storage, while cookie authorization obtains a code through the
// selected organization.
//
// Successful exchange deletes its stored proof before returning token data.
// Lookup and deletion are separate from the remote exchange, so callers
// serialize exchange attempts for one identifier. Organization metadata
// returned with tokens wins over cookie discovery, with discovery filling a
// missing UUID. Public operations register with activity so Stop can cancel
// and wait for them; construction creates idle session cleanup, and Start
// enables periodic expiry removal.

// ClaudeAuthorizationOptions supplies scope, PKCE, URL, proxy, and diagnostic
// callbacks.
type ClaudeAuthorizationOptions struct {
	// ScopeOAuth, ScopeAPI, and ScopeInference choose browser, cookie-full, and
	// Setup Token authorization respectively. Exchange compares the saved scope
	// against ScopeInference before selecting its token variant.
	// ScopeOAuth: browser OAuth scope; ScopeAPI: cookie authorization scope.
	// ScopeInference: inference-only token scope.
	ScopeOAuth, ScopeAPI, ScopeInference string
	// GenerateState, GenerateCodeVerifier, and GenerateSessionID are required for
	// browser setup. Their outputs identify one stored proof; generator errors
	// stop setup before that proof is published.
	// GenerateState: OAuth state generator; GenerateCodeVerifier: PKCE verifier
	// generator.
	// GenerateSessionID: stored proof identifier generator.
	GenerateState, GenerateCodeVerifier, GenerateSessionID func() (string, error)
	// GenerateCodeChallenge is required for setup and cookie authorization. It
	// derives the challenge from the verifier used by subsequent exchange.
	GenerateCodeChallenge func(string) string
	// BuildAuthorizationURL is required after proof setup. It receives state,
	// challenge, and scope paired with the newly stored session.
	BuildAuthorizationURL func(string, string, string) string
	// ResolveProxy is required when a ProxyID is present. A false result leaves
	// the setup proxy URL empty or retains the saved proxy during exchange.
	ResolveProxy func(context.Context, int64) (string, bool)
	// Logf optionally records authorization metadata diagnostics. Its absence
	// discards messages while leaving token and session handling available.
	Logf func(string, ...any)
}

// ClaudeAuthorization owns temporary proof and tracked Claude credential
// exchanges.
type ClaudeAuthorization struct {
	// Store retains browser proof and is initialized by the constructor. Exchange
	// consumes successful sessions, while Start enables periodic expiry removal.
	Store  *ClaudeAuthorizationSessions
	Client ClaudeOAuthClient // configured upstream authorization client
	// Options copies callback references at construction. Configure the shared
	// service before operations and keep those callbacks stable during activity.
	Options  ClaudeAuthorizationOptions
	activity operationActivity // tracked authorization operation lifetime
}

// ClaudeOAuthClient executes organization discovery and credential exchanges
// with prepared proof.
type ClaudeOAuthClient interface {
	// GetOrganizationUUID queries the organization associated with the supplied
	// cookie session key.
	GetOrganizationUUID(ctx context.Context, sessionKey, proxyURL string) (string, error)
	// GetAuthorizationCode issues a code using the supplied organization and PKCE
	// proof. State, challenge, scope, and proxy originate in one prepared flow and
	// remain paired through exchange.
	GetAuthorizationCode(ctx context.Context, sessionKey, orgUUID, scope, codeChallenge, state, proxyURL string) (string, error)
	// ExchangeCodeForToken exchanges code with its verifier, state, proxy, and
	// selected variant. It returns nonnil token data on success, after which the
	// caller consumes saved session proof.
	ExchangeCodeForToken(ctx context.Context, code, codeVerifier, state, proxyURL string, isSetupToken bool) (*wire.OAuthTokenResponse, error)
	// RefreshToken exchanges a stored refresh credential through the selected
	// proxy. Return nonnil token data on success because authorization derives
	// its absolute expiry and optional renewal fields from that response.
	RefreshToken(ctx context.Context, refreshToken, proxyURL string) (*wire.OAuthTokenResponse, error)
}

// ClaudeGenerateAuthURLResult pairs the authorization destination with its
// saved proof identifier.
type ClaudeGenerateAuthURLResult struct {
	// generated browser authorization URL
	AuthURL string `json:"auth_url"`
	// stored authorization proof identifier
	SessionID string `json:"session_id"`
}

// ClaudeExchangeCodeInput selects saved proof and the code to exchange.
type ClaudeExchangeCodeInput struct {
	SessionID string // stored authorization proof identifier
	Code      string // upstream authorization code text
	// ProxyID selects an exchange override when lookup succeeds. Unresolved
	// selections retain the proxy stored with this session during setup.
	ProxyID *int64
}

// ClaudeTokenInfo carries exchanged credentials, second-based expiry, and
// optional account hints.
type ClaudeTokenInfo struct {
	// exchanged OAuth access token
	AccessToken string `json:"access_token"`
	// upstream authentication token type
	TokenType string `json:"token_type"`
	// seconds until upstream token expiry
	ExpiresIn int64 `json:"expires_in"`
	// absolute Unix-second token expiry
	ExpiresAt int64 `json:"expires_at"`
	// optional credential renewal token
	RefreshToken string `json:"refresh_token,omitempty"`
	// returned OAuth scope text
	Scope string `json:"scope,omitempty"`
	// discovered upstream organization identity
	OrgUUID string `json:"org_uuid,omitempty"`
	// discovered upstream account identity
	AccountUUID string `json:"account_uuid,omitempty"`
	// discovered upstream email address
	EmailAddress string `json:"email_address,omitempty"`
}

// ClaudeCookieAuthInput supplies cookie authentication and its desired
// inference or default scope.
type ClaudeCookieAuthInput struct {
	SessionKey string // Claude browser session cookie
	// ProxyID selects cookie-flow transport when lookup succeeds. An unresolved
	// selection leaves the proxy URL empty for the client transport.
	ProxyID *int64
	Scope   string // "full" or "inference"
}

// NewClaudeAuthorization retains client and option callbacks and creates idle
// session storage. Configure required callbacks before use; construction
// starts no cleanup, and callers later Start and StopContext its shared
// lifecycle.
func NewClaudeAuthorization(client ClaudeOAuthClient, options ClaudeAuthorizationOptions) *ClaudeAuthorization {
	return &ClaudeAuthorization{Store: NewClaudeAuthorizationSessions(), Client: client, Options: options}
}

// Start enables periodic session cleanup for this authorization service.
// Repeated calls follow the store's lifecycle gate, and a stopped store cannot
// admit another cleanup worker.
func (s *ClaudeAuthorization) Start() { s.Store.Start() }

// StopContext stops and joins session cleanup before closing authorization
// activity. Pass a nonnil context to budget cancellation and waiting for
// registered operations, whose first stop result remains available to later calls.
func (s *ClaudeAuthorization) StopContext(ctx context.Context) error {
	s.Store.Stop()
	return s.activity.stop(ctx, "claude authorization")
}

// GenerateAuthURL registers one operation and saves full-OAuth PKCE proof for
// its returned URL. A resolved selected proxy is retained in the session;
// unresolved lookup leaves its proxy empty.
// Pass a nonnil context for the registered exchange workflow.
func (s *ClaudeAuthorization) GenerateAuthURL(ctx context.Context, proxyID *int64) (*ClaudeGenerateAuthURLResult, error) {
	operation, done, err := s.activity.begin(ctx, errors.New("claude authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	ctx = operation

	return s.generateAuthURLWithScope(ctx, s.Options.ScopeOAuth, proxyID)
}

// GenerateSetupTokenURL registers one operation and saves inference-scope PKCE
// proof for Setup Token authorization. It uses the same session lifetime and
// proxy resolution path as full OAuth.
// Pass a nonnil context for the registered exchange workflow.
func (s *ClaudeAuthorization) GenerateSetupTokenURL(ctx context.Context, proxyID *int64) (*ClaudeGenerateAuthURLResult, error) {
	operation, done, err := s.activity.begin(ctx, errors.New("claude authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	ctx = operation

	scope := s.Options.ScopeInference
	return s.generateAuthURLWithScope(ctx, scope, proxyID)
}

// generateAuthURLWithScope generates and stores one PKCE proof before
// returning its authorization URL. Generator errors stop setup before
// publication, and required options provide its scope, challenge, and
// destination.
//
// The saved state and verifier correspond to the state and challenge used by
// the returned URL. A resolved proxy is stored for later exchange, while
// unresolved lookup leaves it empty. The caller uses SessionID to retrieve
// this exact setup until expiry or successful exchange consumes its proof.
func (s *ClaudeAuthorization) generateAuthURLWithScope(ctx context.Context, scope string, proxyID *int64) (*ClaudeGenerateAuthURLResult, error) {
	// Generate one proof before storing it. The same state and verifier connect
	// URL construction to later exchange under the returned session identifier.
	state, err := s.Options.GenerateState()
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}

	codeVerifier, err := s.Options.GenerateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("failed to generate code verifier: %w", err)
	}

	codeChallenge := s.Options.GenerateCodeChallenge(codeVerifier)

	sessionID, err := s.Options.GenerateSessionID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate session ID: %w", err)
	}

	// A selected proxy is used only after successful lookup. An unavailable
	// selection leaves the proxy URL empty for the configured client transport.
	var proxyURL string
	if proxyID != nil {
		if resolved, ok := s.Options.ResolveProxy(ctx, *proxyID); ok {
			proxyURL = resolved
		}
	}

	// Publish state, verifier, scope, and proxy together before returning the URL,
	// so later exchange can use the exact proof created by this setup.
	session := &ClaudeAuthorizationSession{
		State:        state,
		CodeVerifier: codeVerifier,
		Scope:        scope,
		ProxyURL:     proxyURL,
		CreatedAt:    time.Now(),
	}
	s.Store.Set(sessionID, session)

	authURL := s.Options.BuildAuthorizationURL(state, codeChallenge, scope)

	return &ClaudeGenerateAuthURLResult{
		AuthURL:   authURL,
		SessionID: sessionID,
	}, nil
}

// ExchangeCode exchanges a nonnil input using saved proof and returns token
// information. Missing or expired sessions fail, and an upstream error keeps
// the session available for retry until expiry.
//
// A resolved input proxy overrides the saved proxy; failed resolution keeps
// its saved choice. Saved scope equality with ScopeInference selects Setup
// Token exchange. Lookup and remote exchange do not atomically claim proof, so
// callers serialize attempts for one identifier. Success deletes the session
// after token metadata has been assembled.
// Pass a nonnil context for the registered exchange workflow.
func (s *ClaudeAuthorization) ExchangeCode(ctx context.Context, input *ClaudeExchangeCodeInput) (*ClaudeTokenInfo, error) {
	operation, done, err := s.activity.begin(ctx, errors.New("claude authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	ctx = operation

	session, ok := s.Store.Get(input.SessionID)
	if !ok {
		return nil, fmt.Errorf("session not found or expired")
	}

	// A resolved input proxy wins over the saved choice. When lookup fails, retain
	// the session proxy that was selected during this authorization setup.
	proxyURL := session.ProxyURL
	if input.ProxyID != nil {
		if resolved, ok := s.Options.ResolveProxy(ctx, *input.ProxyID); ok {
			proxyURL = resolved
		}
	}

	// Match the saved scope against the configured inference scope, so exchange
	// uses the variant selected during URL setup rather than a caller-supplied
	// mode.
	isSetupToken := session.Scope == s.Options.ScopeInference

	tokenInfo, err := s.exchangeCodeForToken(ctx, input.Code, session.CodeVerifier, session.State, proxyURL, isSetupToken)
	if err != nil {
		return nil, err
	}

	// Consume proof after success, retaining it after failure for possible retry.
	// This deletion is separate from lookup, so callers serialize exchanges for
	// one session.
	s.Store.Delete(input.SessionID)

	return tokenInfo, nil
}

// CookieAuth authenticates a nonnil cookie input through organization
// discovery, PKCE code issuance, and token exchange. The exact inference input
// selects Setup Token mode; other inputs select ScopeAPI.
//
// Each stage uses the same selected proxy and generated proof, with errors
// stopping later calls. The token response's organization UUID has precedence
// over cookie discovery. When that UUID is absent, the discovered organization
// fills it so returned metadata remains associated with the organization used
// to obtain the code.
// Pass a nonnil context for the registered exchange workflow.
func (s *ClaudeAuthorization) CookieAuth(ctx context.Context, input *ClaudeCookieAuthInput) (*ClaudeTokenInfo, error) {
	operation, done, err := s.activity.begin(ctx, errors.New("claude authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	ctx = operation

	// Cookie authorization has no stored proxy to recover. An unavailable lookup
	// therefore leaves transport selection empty for the subsequent organization
	// and token calls.
	var proxyURL string
	if input.ProxyID != nil {
		if resolved, ok := s.Options.ResolveProxy(ctx, *input.ProxyID); ok {
			proxyURL = resolved
		}
	}

	// The exact inference input switches both issuance scope and token variant.
	// Other inputs use ScopeAPI throughout this cookie flow and its paired
	// exchange.
	scope := s.Options.ScopeAPI
	isSetupToken := false
	if input.Scope == "inference" {
		scope = s.Options.ScopeInference
		isSetupToken = true
	}

	orgUUID, err := s.getOrganizationUUID(ctx, input.SessionKey, proxyURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get organization info: %w", err)
	}

	// Generate verifier, challenge, and state as one cookie-flow proof. Code
	// issuance and exchange use these paired values with the discovered
	// organization.
	codeVerifier, err := s.Options.GenerateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("failed to generate code verifier: %w", err)
	}
	codeChallenge := s.Options.GenerateCodeChallenge(codeVerifier)

	state, err := s.Options.GenerateState()
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}

	authCode, err := s.getAuthorizationCode(ctx, input.SessionKey, orgUUID, scope, codeChallenge, state, proxyURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get authorization code: %w", err)
	}

	tokenInfo, err := s.exchangeCodeForToken(ctx, authCode, codeVerifier, state, proxyURL, isSetupToken)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code: %w", err)
	}

	// Token response organization metadata wins. When absent, keep the cookie
	// discovery UUID so the token remains associated with its code-issuing
	// organization.
	if tokenInfo.OrgUUID == "" && orgUUID != "" {
		tokenInfo.OrgUUID = orgUUID
		s.logf("[OAuth] Set org_uuid from cookie auth")
	}

	return tokenInfo, nil
}

// getOrganizationUUID delegates cookie organization lookup through the
// configured Claude client.
func (s *ClaudeAuthorization) getOrganizationUUID(ctx context.Context, sessionKey, proxyURL string) (string, error) {
	return s.Client.GetOrganizationUUID(ctx, sessionKey, proxyURL)
}

// getAuthorizationCode delegates issuance with the supplied organization,
// scope, and PKCE proof.
func (s *ClaudeAuthorization) getAuthorizationCode(ctx context.Context, sessionKey, orgUUID, scope, codeChallenge, state, proxyURL string) (string, error) {
	return s.Client.GetAuthorizationCode(ctx, sessionKey, orgUUID, scope, codeChallenge, state, proxyURL)
}

// exchangeCodeForToken exchanges supplied proof and assembles a new token
// result. Configure Client to return nonnil token data on success; optional
// response metadata contributes organization, account, and email hints.
//
// State and verifier remain paired with the supplied code, while isSetupToken
// chooses its exchange variant. ExpiresAt adds response lifetime seconds to
// current Unix seconds with no margin subtraction. Only supplied organization
// and provider hints are copied, giving later provider construction the
// identity metadata returned by this token exchange.
func (s *ClaudeAuthorization) exchangeCodeForToken(ctx context.Context, code, codeVerifier, state, proxyURL string, isSetupToken bool) (*ClaudeTokenInfo, error) {
	tokenResp, err := s.Client.ExchangeCodeForToken(ctx, code, codeVerifier, state, proxyURL, isSetupToken)
	if err != nil {
		return nil, err
	}

	tokenInfo := &ClaudeTokenInfo{
		AccessToken: tokenResp.AccessToken,
		TokenType:   tokenResp.TokenType,
		ExpiresIn:   tokenResp.ExpiresIn,
		// Response lifetime seconds become an absolute Unix-second credential expiry.
		ExpiresAt:    time.Now().Unix() + tokenResp.ExpiresIn,
		RefreshToken: tokenResp.RefreshToken,
		Scope:        tokenResp.Scope,
	}

	if tokenResp.Organization != nil && tokenResp.Organization.UUID != "" {
		tokenInfo.OrgUUID = tokenResp.Organization.UUID
		s.logf("[OAuth] Got org_uuid")
	}
	if tokenResp.Provider != nil {
		if tokenResp.Provider.UUID != "" {
			tokenInfo.AccountUUID = tokenResp.Provider.UUID
			s.logf("[OAuth] Got account_uuid")
		}
		if tokenResp.Provider.EmailAddress != "" {
			tokenInfo.EmailAddress = tokenResp.Provider.EmailAddress
			s.logf("[OAuth] Got email_address")
		}
	}

	return tokenInfo, nil
}

// RefreshToken registers an exchange using the supplied refresh token and
// proxy. Configure Client to return nonnil token data on success; response
// lifetime becomes Unix-second expiry, optional renewal/scope fields are
// returned, and provider persistence remains with the caller.
// Pass a nonnil context for the registered exchange workflow.
func (s *ClaudeAuthorization) RefreshToken(ctx context.Context, refreshToken string, proxyURL string) (*ClaudeTokenInfo, error) {
	operation, done, err := s.activity.begin(ctx, errors.New("claude authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	ctx = operation

	tokenResp, err := s.Client.RefreshToken(ctx, refreshToken, proxyURL)
	if err != nil {
		return nil, err
	}

	return &ClaudeTokenInfo{
		AccessToken: tokenResp.AccessToken,
		TokenType:   tokenResp.TokenType,
		ExpiresIn:   tokenResp.ExpiresIn,
		// Response lifetime seconds become an absolute Unix-second credential expiry.
		ExpiresAt:    time.Now().Unix() + tokenResp.ExpiresIn,
		RefreshToken: tokenResp.RefreshToken,
		Scope:        tokenResp.Scope,
	}, nil
}

// RefreshProviderToken reads a nonnil record's refresh token and resolved
// proxy. An exactly empty token fails; unresolved proxy lookup leaves the proxy
// URL empty, and RefreshToken owns the tracked exchange.
// Pass a nonnil context for the registered exchange workflow.
func (s *ClaudeAuthorization) RefreshProviderToken(ctx context.Context, provider *Record) (*ClaudeTokenInfo, error) {
	refreshToken := provider.GetCredential("refresh_token")
	if refreshToken == "" {
		return nil, fmt.Errorf("no refresh token available")
	}

	// Without saved authorization proof, unresolved provider proxy lookup leaves
	// an empty URL, which RefreshToken passes through to the configured transport.
	var proxyURL string
	if provider.ProxyID != nil {
		if resolved, ok := s.Options.ResolveProxy(ctx, *provider.ProxyID); ok {
			proxyURL = resolved
		}
	}

	return s.RefreshToken(ctx, refreshToken, proxyURL)
}

// logf forwards diagnostics to the optional configured authorization logger.
// Missing Logf discards the message while credential result and session
// handling continue through their existing paths.
func (s *ClaudeAuthorization) logf(format string, args ...any) {
	if s.Options.Logf != nil {
		s.Options.Logf(format, args...)
	}
}
