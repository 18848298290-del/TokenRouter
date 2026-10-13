package provider

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"strings"
	"time"

	geminiwire "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

// Gemini authorization stores PKCE proof with its OAuth variant, proxy,
// project, and selected tier. Code Assist and Google One use the built-in CLI
// client, while AI Studio requires custom client configuration and uses its
// dedicated callback address.
//
// Successful code exchange consumes proof before project and tier discovery,
// so later validation failure still leaves the session consumed. Discovery
// combines variant-specific project requirements with stored or selected tier
// fallback; Google One also retains Drive metadata. Adjusted expiry subtracts
// five minutes and clamps stored lifetime to thirty seconds, which can exceed
// a short upstream lifetime. Public activity registrations support shutdown
// waiting, while fixed retry and onboarding sleeps ignore cancellation until
// return. Callers serialize same-session exchanges and keep callbacks and
// borrowed inputs stable.

// Gemini storage constants use binary byte units and classify Drive limits
// into the local subscription labels consumed by authorization tier inference.
const (
	GB = 1024 * 1024 * 1024
	TB = 1024 * GB

	StorageTierUnlimited = 100 * TB // 100TB
	StorageTierAIPremium = 2 * TB   // 2TB
	StorageTierFree      = 15 * GB  // 15GB
)

// GeminiOAuthClient exchanges authorization codes and renews variant-specific
// Google credentials.
type GeminiOAuthClient interface {
	// ExchangeCode exchanges variant-specific code and saved PKCE proof. Return
	// nonnil token data on success because authorization consumes proof before
	// deriving expiry and account metadata from that result.
	ExchangeCode(context.Context, string, string, string, string, string) (*google.TokenResponse, error)
	// RefreshToken renews the supplied variant credential through its proxy.
	// Successful data must be nonnil because authorization reads lifetime and
	// optional renewal fields before returning the credential result.
	RefreshToken(context.Context, string, string, string) (*google.TokenResponse, error)
}

// GeminiCodeAssistClient reads project eligibility and performs project
// onboarding requests.
type GeminiCodeAssistClient interface {
	// LoadCodeAssist reads project and tier observations with optional metadata.
	// Nil response can trigger onboarding; a nonnil response may retain useful
	// tier information even when the request reports an error.
	LoadCodeAssist(context.Context, string, string, *geminiwire.LoadCodeAssistRequest) (*geminiwire.LoadCodeAssistResponse, error)
	// OnboardUser submits or polls selected-tier onboarding. Return nonnil
	// successful responses; pending results are polled, while completed project
	// identifiers may use string or object representations consumed by project
	// discovery.
	OnboardUser(context.Context, string, string, *geminiwire.OnboardUserRequest) (*geminiwire.OnboardUserResponse, error)
}

// GeminiDriveClient reads storage totals used to estimate personal
// subscription tiers.
type GeminiDriveClient interface {
	// GetStorageQuota reads storage totals with the supplied token and proxy.
	// Return nonnil successful data because tier inference uses Limit bytes and
	// retains other observations as credential metadata.
	GetStorageQuota(context.Context, string, string) (*google.DriveStorageInfo, error)
}

// GeminiAuthorizationOptions binds dynamic OAuth configuration, proof
// generation, discovery, and diagnostics.
type GeminiAuthorizationOptions struct {
	// Config is required during capability checks and variant setup. Keep outputs
	// coherent within a flow; changing client configuration can invalidate
	// compatibility with a stored refresh token.
	Config func() google.OAuthConfig
	// BuiltinClientID, CLIRedirectURI, and AIStudioRedirectURI pair client
	// identity with callback selection. Built-in clients use the CLI address;
	// custom clients use the AI Studio address, replacing the input redirect.
	// BuiltinClientID: built-in CLI client identity; CLIRedirectURI: built-in
	// client callback address; AIStudioRedirectURI: custom client callback
	// address.
	BuiltinClientID, CLIRedirectURI, AIStudioRedirectURI string
	// GenerateState, GenerateCodeVerifier, and GenerateSessionID prepare one
	// paired proof before publication. Their errors abort setup before storing
	// state and verifier under the session identifier.
	// GenerateState: OAuth state generator; GenerateCodeVerifier: PKCE verifier
	// generator; GenerateSessionID: proof lookup identifier generator.
	GenerateState, GenerateCodeVerifier, GenerateSessionID func() (string, error)
	// GenerateCodeChallenge is required after verifier generation and derives the
	// URL challenge from the exact verifier retained for code exchange.
	GenerateCodeChallenge func(string) string
	// EffectiveOAuthConfig is required during setup and AI Studio exchange checks.
	// Errors leave any already stored proof available until expiry or cleanup.
	EffectiveOAuthConfig func(google.OAuthConfig, string) (google.OAuthConfig, error)
	// BuildAuthorizationURL is required after redirect selection. Its failure
	// retains stored proof, including the callback address chosen for that client.
	BuildAuthorizationURL func(google.OAuthConfig, string, string, string, string, string) (string, error)
	// ResolveProxy is required when ProxyID is supplied. False availability
	// retains saved exchange transport or leaves a new flow proxy URL empty.
	ResolveProxy func(context.Context, int64) (string, bool)
	// FetchProject is required when Resource Manager fallback is reached. Its
	// error or blank output may be replaced by the Code Assist discovery
	// diagnostic.
	FetchProject func(context.Context, string, string) (string, error)
	// Logf and Printf are required by reached metadata and failure paths. Logf
	// records discovery decisions and details; Printf reports recoverable
	// discovery or credential-validation diagnostics to the operator.
	// Logf: discovery decision diagnostics; Printf: recoverable failure
	// diagnostics.
	Logf, Printf func(string, ...any)
}

// GeminiAuthorization owns saved proof and tracked variant-specific Google
// authorization operations.
// @project-doc docs/interfaces/gemini_upstream.md#gemini_native_execution
type GeminiAuthorization struct {
	// Store owns temporary proof and cleanup lifecycle. Construction initializes
	// it; successful exchange consumes a session before later project or tier
	// validation.
	Store *GeminiAuthorizationSessions
	// Client, CodeAssist, and Drive borrow shared protocol clients and remain
	// usable until registered operations return. Their consumers select token,
	// project, or storage work according to the saved OAuth variant.
	Client GeminiOAuthClient // shared Google token client
	// CodeAssist supplies project discovery when reached. Nil causes the
	// private discovery operation to return a client-not-configured error.
	CodeAssist GeminiCodeAssistClient
	// Drive is required for Google One storage discovery when reached.
	// Successful responses are nonnil and supply bytes for its local tier
	// estimate.
	Drive GeminiDriveClient
	// Options copies callback values and borrows captured dependencies. Configure
	// the shared service before operations and keep callbacks stable through
	// registered work.
	Options GeminiAuthorizationOptions
	// registered authorization operation lifetime
	activity operationActivity
}

// GeminiOAuthCapabilities reports custom-client eligibility and required AI
// Studio callback addresses.
type GeminiOAuthCapabilities struct {
	// custom client capability indicator
	AIStudioOAuthEnabled bool `json:"ai_studio_oauth_enabled"`
	// configured custom callback addresses
	RequiredRedirectURIs []string `json:"required_redirect_uris"`
}

// GeminiAuthURLResult pairs the browser destination with saved session state
// identifiers.
type GeminiAuthURLResult struct {
	// generated browser authorization URL
	AuthURL   string `json:"auth_url"`
	SessionID string `json:"session_id"` // stored proof lookup identifier
	State     string `json:"state"`      // returned OAuth state proof
}

// GeminiExchangeCodeInput supplies code proof and optional proxy and tier
// selections.
type GeminiExchangeCodeInput struct {
	SessionID string // stored proof lookup identifier
	State     string // supplied OAuth state proof
	Code      string // supplied authorization code text
	// ProxyID overrides saved transport only when selected lookup succeeds.
	// An unavailable selection retains the proxy associated with this stored
	// session.
	ProxyID *int64
	// OAuthType is accepted metadata, while ExchangeCode uses the stored session
	// variant. An empty saved variant selects the Code Assist exchange path.
	OAuthType string
	// TierID supplies a compatible fallback after automatic discovery. Empty
	// or incompatible input uses the saved session tier before its variant
	// default.
	TierID string
}

// GeminiTokenInfo carries renewed credentials, adjusted expiry, and account
// discovery hints.
type GeminiTokenInfo struct {
	// exchanged OAuth access token
	AccessToken string `json:"access_token"`
	// optional token renewal credential
	RefreshToken string `json:"refresh_token"`
	// upstream token lifetime seconds
	ExpiresIn int64 `json:"expires_in"`
	// adjusted stored Unix expiry
	ExpiresAt int64 `json:"expires_at"`
	// upstream token type name
	TokenType string `json:"token_type"`
	// upstream authorization scope text
	Scope string `json:"scope,omitempty"`
	// selected project execution identifier
	ProjectID string `json:"project_id,omitempty"`
	// selected OAuth variant label
	OAuthType string `json:"oauth_type,omitempty"`
	// selected tier identifier text
	TierID string `json:"tier_id,omitempty"`
	// Extra merges into credentials last and can override token or tier keys.
	// Nested values remain borrowed through that shallow metadata copy.
	Extra map[string]any `json:"extra,omitempty"`
}

// NewGeminiAuthorization retains protocol clients, copies options, and creates
// idle session storage. Configure reached callbacks and clients before use;
// Start enables cleanup, and StopContext owns authorization cancellation and
// completion waiting.
func NewGeminiAuthorization(client GeminiOAuthClient, codeassist GeminiCodeAssistClient, drive GeminiDriveClient, options GeminiAuthorizationOptions) *GeminiAuthorization {
	return &GeminiAuthorization{Store: NewGeminiAuthorizationSessions(), Client: client, CodeAssist: codeassist, Drive: drive, Options: options}
}

// Start enables periodic expiry cleanup through the authorization session
// store. Repeated startup follows its lifecycle gate, and StopContext
// permanently closes cleanup admission before waiting for tracked
// authorization work.
func (s *GeminiAuthorization) Start() { s.Store.Start() }

// StopContext stops and joins session cleanup before canceling authorization
// activity. A nonnil context budgets the activity wait; its first completed
// result, including unfinished-work timeout, remains available to subsequent
// calls.
func (s *GeminiAuthorization) StopContext(ctx context.Context) error {
	s.Store.Stop()
	return s.activity.stop(ctx, "gemini authorization")
}

// GetOAuthConfig reports whether custom client configuration enables AI Studio
// OAuth. Config is required; trimmed ID and secret must both be nonempty, and
// the ID must differ from the configured built-in identifier.
//
// Those fields are obtained from separate Config calls, so changing callback
// output can produce a mixed observation. RequiredRedirectURIs contains a
// newly allocated slice with the configured custom callback address. The
// returned capability describes these reads; later setup and exchange read
// configuration again and perform their own validation.
func (s *GeminiAuthorization) GetOAuthConfig() *GeminiOAuthCapabilities {
	// AI Studio OAuth is only enabled when the operator configures a custom OAuth
	// client.
	clientID := strings.TrimSpace(s.Options.Config().ClientID)
	clientSecret := strings.TrimSpace(s.Options.Config().ClientSecret)
	enabled := clientID != "" && clientSecret != "" && clientID != s.Options.BuiltinClientID

	return &GeminiOAuthCapabilities{
		AIStudioOAuthEnabled: enabled,
		RequiredRedirectURIs: []string{s.Options.AIStudioRedirectURI},
	}
}

// GenerateAuthURL registers setup and stores state, verifier, proxy, project,
// tier, and variant for its URL. Required generator failures stop before proof
// publication; unresolved selected proxy lookup leaves transport empty.
//
// Code Assist and Google One clear custom credentials before effective client
// selection. Built-in clients use CLIRedirectURI, custom clients use
// AIStudioRedirectURI, and AI Studio rejects the built-in client. Proof is
// stored before configuration validation, then its redirect is updated before
// URL construction, so later errors can leave a stored session. Callers keep
// callbacks stable and serialize eventual exchange of the returned identifier.
// Pass a nonnil context for independent activity registration.
func (s *GeminiAuthorization) GenerateAuthURL(ctx context.Context, proxyID *int64, redirectURI, projectID, oauthType, tierID string) (*GeminiAuthURLResult, error) {
	operation, done, startErr := s.activity.begin(ctx, errors.New("gemini authorization is stopped"))
	if startErr != nil {
		return nil, startErr
	}
	defer done()
	ctx = operation

	// Prepare state, verifier, challenge, and identifier before publishing proof.
	// Generator failures leave no stored partial session, and later code exchange
	// uses this paired state and verifier.
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

	// This flow has no saved session route to recover. Unavailable selected
	// proxy lookup leaves an empty URL for the configured Google transport
	// callbacks.
	var proxyURL string
	if proxyID != nil {
		if resolved, ok := s.Options.ResolveProxy(ctx, *proxyID); ok {
			proxyURL = resolved
		}
	}

	// Clear custom credentials for Code Assist and Google One before effective
	// client selection. AI Studio retains configured credentials so setup can
	// validate its custom-client requirement.
	oauthCfg := s.Options.Config()
	if oauthType == "code_assist" || oauthType == "google_one" {
		oauthCfg.ClientID = ""
		oauthCfg.ClientSecret = ""
	}

	// Publish initial proof before validating effective client configuration. A
	// later configuration or URL error therefore retains this session until its
	// age gate or cleanup removes it.
	session := &GeminiOAuthSession{
		State:        state,
		CodeVerifier: codeVerifier,
		ProxyURL:     proxyURL,
		RedirectURI:  redirectURI,
		ProjectID:    strings.TrimSpace(projectID),
		TierID:       CanonicalGeminiTierIDForOAuthType(oauthType, tierID),
		OAuthType:    oauthType,
		CreatedAt:    time.Now(),
	}
	s.Store.Set(sessionID, session)

	effectiveCfg, err := s.Options.EffectiveOAuthConfig(oauthCfg, oauthType)
	if err != nil {
		return nil, err
	}

	isBuiltinClient := effectiveCfg.ClientID == s.Options.BuiltinClientID

	// AI Studio OAuth requires a user-provided OAuth client (built-in Gemini CLI
	// client is scope-restricted).
	if oauthType == "ai_studio" && isBuiltinClient {
		return nil, fmt.Errorf("AI Studio OAuth requires a custom OAuth Client (GEMINI_OAUTH_CLIENT_ID / GEMINI_OAUTH_CLIENT_SECRET). If you don't want to configure an OAuth client, please use an AI Studio API Key provider instead")
	}

	// Choose callback from effective client identity, replacing the input
	// redirect. Update the stored session with that address before URL
	// construction so later exchange uses its paired callback.
	if isBuiltinClient {
		redirectURI = s.Options.CLIRedirectURI
	} else {
		redirectURI = s.Options.AIStudioRedirectURI
	}
	session.RedirectURI = redirectURI
	s.Store.Set(sessionID, session)

	authURL, err := s.Options.BuildAuthorizationURL(effectiveCfg, state, codeChallenge, redirectURI, session.ProjectID, oauthType)
	if err != nil {
		return nil, err
	}

	return &GeminiAuthURLResult{
		AuthURL:   authURL,
		SessionID: sessionID,
		State:     state,
	}, nil
}

// ValidateTierID accepts empty or up to sixty-four ASCII letters, digits,
// underscores, hyphens, and slashes. Validation is structural: a permitted
// string need not be a recognized canonical tier.
func ValidateTierID(tierID string) error {
	if tierID == "" {
		return nil
	}
	// Bound stored tier labels before accepting credential metadata.
	if len(tierID) > 64 {
		return fmt.Errorf("tier_id exceeds maximum length of 64 characters")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_/-]+$`).MatchString(tierID) {
		return fmt.Errorf("tier_id contains invalid characters")
	}
	return nil
}

// ExtractTierIDFromAllowedTiers selects a trimmed nonempty default tier, then
// the first nonempty tier when its sentinel remains. Missing and blank IDs
// cannot displace a usable candidate; empty input returns LEGACY.
//
// LEGACY is also a possible upstream ID, so selecting that exact default still
// enters the second pass. An earlier nondefault tier can then replace it.
// Array order determines the fallback, and fetchProjectID uses the result for
// onboarding only when current or paid tier metadata is unavailable.
func ExtractTierIDFromAllowedTiers(allowedTiers []geminiwire.AllowedTier) string {
	// Use LEGACY as the unresolved marker before default selection. Its
	// literal identity also triggers the later first-nonempty pass, so
	// explicit LEGACY defaults need that same handling.
	tierID := "LEGACY"
	for _, tier := range allowedTiers {
		if tier.IsDefault && strings.TrimSpace(tier.ID) != "" {
			tierID = strings.TrimSpace(tier.ID)
			break
		}
	}
	// LEGACY doubles as the unresolved sentinel, so an explicit default LEGACY
	// also enters this pass and can select an earlier nonempty array entry.
	if tierID == "LEGACY" {
		for _, tier := range allowedTiers {
			if strings.TrimSpace(tier.ID) != "" {
				tierID = strings.TrimSpace(tier.ID)
				break
			}
		}
	}
	return tierID
}

// InferGoogleOneTier estimates a label from Drive bytes and requires logf.
// Above one hundred binary TB selects Ultra; two through one hundred selects
// Pro, fifteen GB through below two TB selects Free, and other values select
// Unknown.
func InferGoogleOneTier(storageBytes int64, logf func(string, ...any)) string {
	// Rounded binary-unit diagnostics leave integer-byte tier comparisons
	// unchanged.
	logf("[GeminiOAuth] inferGoogleOneTier - input: %d bytes (%.2f TB)", storageBytes, float64(storageBytes)/float64(TB))

	// Storage capacity produces a local estimate, so unresolved or below-free
	// observations use Unknown. Check larger cutoffs first because each also
	// satisfies the lower cutoffs; exact maximum capacity remains in Pro.
	if storageBytes <= 0 {
		logf("[GeminiOAuth] inferGoogleOneTier - storageBytes <= 0, returning UNKNOWN")
		return GeminiTierGoogleOneUnknown
	}

	if storageBytes > StorageTierUnlimited {
		logf("[GeminiOAuth] inferGoogleOneTier - > %d bytes (100TB), returning UNLIMITED", StorageTierUnlimited)
		return GeminiTierGoogleAIUltra
	}
	if storageBytes >= StorageTierAIPremium {
		logf("[GeminiOAuth] inferGoogleOneTier - >= %d bytes (2TB), returning google_ai_pro", StorageTierAIPremium)
		return GeminiTierGoogleAIPro
	}
	if storageBytes >= StorageTierFree {
		logf("[GeminiOAuth] inferGoogleOneTier - >= %d bytes (15GB), returning FREE", StorageTierFree)
		return GeminiTierGoogleOneFree
	}

	logf("[GeminiOAuth] inferGoogleOneTier - < %d bytes (15GB), returning UNKNOWN", StorageTierFree)
	return GeminiTierGoogleOneUnknown
}

// fetchGoogleOneTier estimates a personal tier from Drive quota using the
// selected token and proxy. Drive and Logf are required, successful storage
// data is nonnil, and request failure returns Unknown with the original error.
func (s *GeminiAuthorization) fetchGoogleOneTier(ctx context.Context, accessToken, proxyURL string) (string, *google.DriveStorageInfo, error) {
	s.Options.Logf("[GeminiOAuth] Starting FetchGoogleOneTier (Google One personal provider)")

	// Use Drive API to infer tier from storage quota (requires drive.readonly
	// scope)
	s.Options.Logf("[GeminiOAuth] Calling Drive API for storage quota...")

	storageInfo, err := s.Drive.GetStorageQuota(ctx, accessToken, proxyURL)
	if err != nil {
		// A status 403 diagnostic uses scope-specific logging before returning the
		// original error and Unknown tier, keeping the failed observation unresolved.
		if strings.Contains(err.Error(), "status 403") {
			s.Options.Logf("[GeminiOAuth] Drive API scope not available (403): %v", err)
			return GeminiTierGoogleOneUnknown, nil, err
		}
		s.Options.Logf("[GeminiOAuth] Failed to fetch Drive storage: %v", err)
		return GeminiTierGoogleOneUnknown, nil, err
	}

	s.Options.Logf("[GeminiOAuth] Drive API response - Limit: %d bytes (%.2f TB), Usage: %d bytes (%.2f GB)",
		storageInfo.Limit, float64(storageInfo.Limit)/float64(TB),
		storageInfo.Usage, float64(storageInfo.Usage)/float64(GB))

	tierID := InferGoogleOneTier(storageInfo.Limit, s.Options.Logf)
	s.Options.Logf("[GeminiOAuth] Inferred tier from storage: %s", tierID)

	return tierID, storageInfo, nil
}

// ExchangeCode registers a nonnil input, validates saved state, and exchanges
// its code. Session OAuthType selects the variant, defaulting empty to
// code_assist; input OAuthType is unused, and a resolved input proxy overrides
// saved transport.
//
// Success deletes proof before project validation. Code Assist rejects an
// empty resulting project; Google One aborts project discovery errors and
// queries Drive for tier, while AI Studio uses selected tier without
// discovery. Compatible detected tiers precede input/session fallback and
// variant defaults. Expiry is margin-adjusted with a thirty-second floor.
// Callers serialize same-session exchange and provide nonnil successful token
// responses.
// Pass a nonnil context for independent activity registration.
func (s *GeminiAuthorization) ExchangeCode(ctx context.Context, input *GeminiExchangeCodeInput) (*GeminiTokenInfo, error) {
	operation, done, startErr := s.activity.begin(ctx, errors.New("gemini authorization is stopped"))
	if startErr != nil {
		return nil, startErr
	}
	defer done()
	ctx = operation

	s.Options.Logf("[GeminiOAuth] ========== ExchangeCode START ==========")
	s.Options.Logf("[GeminiOAuth] SessionID: %s", input.SessionID)

	session, ok := s.Store.Get(input.SessionID)
	if !ok {
		s.Options.Logf("[GeminiOAuth] ERROR: Session not found or expired")
		return nil, fmt.Errorf("session not found or expired")
	}
	if strings.TrimSpace(input.State) == "" || input.State != session.State {
		s.Options.Logf("[GeminiOAuth] ERROR: Invalid state")
		return nil, fmt.Errorf("invalid state")
	}

	// Unavailable selected proxy lookup retains the saved transport route so
	// code exchange continues with the proxy associated with this authorization
	// proof.
	proxyURL := session.ProxyURL
	if input.ProxyID != nil {
		if resolved, ok := s.Options.ResolveProxy(ctx, *input.ProxyID); ok {
			proxyURL = resolved
		}
	}
	s.Options.Logf("[GeminiOAuth] ProxyURL: %s", proxyURL)

	redirectURI := session.RedirectURI

	// The saved session selects OAuth variant, so input OAuthType cannot replace
	// it. Empty stored variants use Code Assist and its fixed built-in callback
	// address.
	oauthType := session.OAuthType
	if oauthType == "" {
		oauthType = "code_assist"
	}
	s.Options.Logf("[GeminiOAuth] OAuth Type: %s", oauthType)
	s.Options.Logf("[GeminiOAuth] Project ID from session: %s", session.ProjectID)

	// If the session was created for AI Studio OAuth, ensure a custom OAuth
	// client is configured.
	if oauthType == "ai_studio" {
		effectiveCfg, err := s.Options.EffectiveOAuthConfig(s.Options.Config(), "ai_studio")
		if err != nil {
			return nil, err
		}
		isBuiltinClient := effectiveCfg.ClientID == s.Options.BuiltinClientID
		if isBuiltinClient {
			return nil, fmt.Errorf("AI Studio OAuth requires a custom OAuth Client. Please use an AI Studio API Key provider, or configure GEMINI_OAUTH_CLIENT_ID / GEMINI_OAUTH_CLIENT_SECRET and re-authorize")
		}
	}

	// Code Assist and Google One exchange through the fixed CLI callback, matching
	// the built-in client selected when their authorization URL stored this PKCE
	// proof.
	if oauthType == "code_assist" || oauthType == "google_one" {
		redirectURI = s.Options.CLIRedirectURI
	}

	tokenResp, err := s.Client.ExchangeCode(ctx, oauthType, input.Code, session.CodeVerifier, redirectURI, proxyURL)
	if err != nil {
		s.Options.Logf("[GeminiOAuth] ERROR: Failed to exchange code: %v", err)
		return nil, fmt.Errorf("failed to exchange code: %w", err)
	}
	s.Options.Logf("[GeminiOAuth] Token exchange successful")
	s.Options.Logf("[GeminiOAuth] Token scope: %s", tokenResp.Scope)
	s.Options.Logf("[GeminiOAuth] Token expires_in: %d seconds", tokenResp.ExpiresIn)

	// Consume proof after token success and before project validation. A later
	// missing-project error therefore requires new setup for another code
	// exchange.
	sessionProjectID := strings.TrimSpace(session.ProjectID)
	s.Store.Delete(input.SessionID)

	// Subtract a refresh margin but floor stored lifetime at thirty seconds. This
	// avoids immediately elapsed credential timestamps and may exceed the lifetime
	// actually issued by upstream.
	const safetyWindow = 300 // 5 minutes
	const minTTL = 30        // minimum 30 seconds
	expiresAt := time.Now().Unix() + tokenResp.ExpiresIn - safetyWindow
	minExpiresAt := time.Now().Unix() + minTTL
	if expiresAt < minExpiresAt {
		expiresAt = minExpiresAt
	}

	projectID := sessionProjectID
	var tierID string
	// Select a compatible input tier before the stored session choice. This
	// fallback is consulted when automatic discovery yields an unresolved
	// compatible tier.
	fallbackTierID := CanonicalGeminiTierIDForOAuthType(oauthType, input.TierID)
	if fallbackTierID == "" {
		fallbackTierID = CanonicalGeminiTierIDForOAuthType(oauthType, session.TierID)
	}

	s.Options.Logf("[GeminiOAuth] ========== Provider Type Detection START ==========")
	s.Options.Logf("[GeminiOAuth] OAuth Type: %s", oauthType)

	// Code Assist rejects a still-empty project after discovery; Google One
	// aborts discovery errors but does not recheck a successful blank result.
	// Their tier sources differ, while AI Studio uses the compatible selected
	// tier without project discovery.
	switch oauthType {
	case "code_assist":
		s.Options.Logf("[GeminiOAuth] Processing code_assist OAuth type")
		if projectID == "" {
			s.Options.Logf("[GeminiOAuth] No project_id provided, attempting to fetch from LoadCodeAssist API...")
			var err error
			projectID, tierID, err = s.fetchProjectID(ctx, tokenResp.AccessToken, proxyURL)
			if err != nil {
				// Log discovery errors here; the
				// missing-project check below rejects an
				// unresolved project.
				s.Options.Printf("[GeminiOAuth] Warning: Failed to fetch project_id during token exchange: %v\n", err)
				s.Options.Logf("[GeminiOAuth] WARNING: Failed to fetch project_id: %v", err)
			} else {
				s.Options.Logf("[GeminiOAuth] Successfully fetched project_id: %s, tier_id: %s", projectID, tierID)
			}
		} else {
			s.Options.Logf("[GeminiOAuth] User provided project_id: %s, fetching tier_id...", projectID)
			// A manually supplied project still needs tier discovery.
			_, fetchedTierID, err := s.fetchProjectID(ctx, tokenResp.AccessToken, proxyURL)
			if err != nil {
				s.Options.Printf("[GeminiOAuth] Warning: Failed to fetch tierID: %v\n", err)
				s.Options.Logf("[GeminiOAuth] WARNING: Failed to fetch tier_id: %v", err)
			} else {
				tierID = fetchedTierID
				s.Options.Logf("[GeminiOAuth] Successfully fetched tier_id: %s", tierID)
			}
		}
		if strings.TrimSpace(projectID) == "" {
			s.Options.Logf("[GeminiOAuth] ERROR: Missing project_id for Code Assist OAuth")
			return nil, fmt.Errorf("missing project_id for Code Assist OAuth: please fill Project ID (optional field) and regenerate the auth URL, or ensure your Google account has an ACTIVE GCP project")
		}
		// Compatible detected tier takes precedence. If unresolved, compatible
		// input/session fallback wins before the standard Code Assist default used
		// for this account variant.
		tierID = CanonicalGeminiTierIDForOAuthType(oauthType, tierID)
		if tierID == "" {
			if fallbackTierID != "" {
				tierID = fallbackTierID
				s.Options.Logf("[GeminiOAuth] Using fallback tier_id from user/session: %s", tierID)
			} else {
				tierID = GeminiTierGCPStandard
				s.Options.Logf("[GeminiOAuth] Using default tier_id: %s", tierID)
			}
		}
		s.Options.Logf("[GeminiOAuth] Final code_assist result - project_id: %s, tier_id: %s", projectID, tierID)

	case "google_one":
		s.Options.Logf("[GeminiOAuth] Processing google_one OAuth type")

		// Google One providers use cloudaicompanion API, which requires a project_id.
		// Try LoadCodeAssist and Resource Manager when a project was not supplied.
		if projectID == "" {
			s.Options.Logf("[GeminiOAuth] No project_id provided, attempting to fetch from LoadCodeAssist API...")
			var err error
			projectID, _, err = s.fetchProjectID(ctx, tokenResp.AccessToken, proxyURL)
			if err != nil {
				s.Options.Logf("[GeminiOAuth] ERROR: Failed to fetch project_id: %v", err)
				return nil, fmt.Errorf("google One providers require a project_id, failed to auto-detect: %w", err)
			}
			s.Options.Logf("[GeminiOAuth] Successfully fetched project_id: %s", projectID)
		}

		s.Options.Logf("[GeminiOAuth] Attempting to fetch Google One tier from Drive API...")
		var storageInfo *google.DriveStorageInfo
		var err error
		tierID, storageInfo, err = s.fetchGoogleOneTier(ctx, tokenResp.AccessToken, proxyURL)
		if err != nil {
			// Drive failure retains tokens and project metadata. Clear the inferred
			// tier so compatible input/session fallback can supply its display label.
			s.Options.Printf("[GeminiOAuth] Warning: Failed to fetch Drive tier: %v\n", err)
			s.Options.Logf("[GeminiOAuth] WARNING: Failed to fetch Drive tier: %v", err)
			tierID = ""
		} else {
			s.Options.Logf("[GeminiOAuth] Successfully fetched Drive tier: %s", tierID)
			if storageInfo != nil {
				s.Options.Logf("[GeminiOAuth] Drive storage - Limit: %d bytes (%.2f TB), Usage: %d bytes (%.2f GB)",
					storageInfo.Limit, float64(storageInfo.Limit)/float64(TB),
					storageInfo.Usage, float64(storageInfo.Usage)/float64(GB))
			}
		}
		tierID = CanonicalGeminiTierIDForOAuthType(oauthType, tierID)
		if tierID == "" || tierID == GeminiTierGoogleOneUnknown {
			if fallbackTierID != "" {
				tierID = fallbackTierID
				s.Options.Logf("[GeminiOAuth] Using fallback tier_id from user/session: %s", tierID)
			} else {
				tierID = GeminiTierGoogleOneFree
				s.Options.Logf("[GeminiOAuth] Using default tier_id: %s", tierID)
			}
		}
		s.Options.Printf("[GeminiOAuth] Google One tierID after normalization: %s\n", tierID)

		// Retain Drive totals with their observation time for later credential
		// construction, which merges these metadata keys after the selected token
		// fields.
		if storageInfo != nil {
			tokenInfo := &GeminiTokenInfo{
				AccessToken:  tokenResp.AccessToken,
				RefreshToken: tokenResp.RefreshToken,
				TokenType:    tokenResp.TokenType,
				ExpiresIn:    tokenResp.ExpiresIn,
				ExpiresAt:    expiresAt,
				Scope:        tokenResp.Scope,
				ProjectID:    projectID,
				TierID:       tierID,
				OAuthType:    oauthType,
				Extra: map[string]any{
					"drive_storage_limit":   storageInfo.Limit,
					"drive_storage_usage":   storageInfo.Usage,
					"drive_tier_updated_at": time.Now().Format(time.RFC3339),
				},
			}
			s.Options.Logf("[GeminiOAuth] ========== ExchangeCode END (google_one with storage info) ==========")
			return tokenInfo, nil
		}

	case "ai_studio":
		// No automatic tier detection for AI Studio OAuth; rely on user selection.
		if fallbackTierID != "" {
			tierID = fallbackTierID
		} else {
			tierID = GeminiTierAIStudioFree
		}

	default:
		s.Options.Logf("[GeminiOAuth] Processing %s OAuth type (no tier detection)", oauthType)
	}

	s.Options.Logf("[GeminiOAuth] ========== Provider Type Detection END ==========")

	result := &GeminiTokenInfo{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		TokenType:    tokenResp.TokenType,
		ExpiresIn:    tokenResp.ExpiresIn,
		ExpiresAt:    expiresAt,
		Scope:        tokenResp.Scope,
		ProjectID:    projectID,
		TierID:       tierID,
		OAuthType:    oauthType,
	}
	s.Options.Logf("[GeminiOAuth] Final result - OAuth Type: %s, Project ID: %s, Tier ID: %s", result.OAuthType, result.ProjectID, result.TierID)
	s.Options.Logf("[GeminiOAuth] ========== ExchangeCode END ==========")
	return result, nil
}

// refreshToken renews the supplied variant credential with at most four calls
// and nonnil successful token data. Permanent OAuth substrings stop
// immediately; other errors use one-, two-, and four-second unconditional
// sleeps, while ctx reaches cooperative requests.
//
// Expiry subtracts a five-minute margin and then floors the stored timestamp
// at thirty seconds from the clock. ExpiresIn retains the upstream lifetime,
// so the adjusted timestamp can extend beyond a short-lived token. Retry
// sleeps can outlast shutdown waiting even after the protocol context is
// canceled.
func (s *GeminiAuthorization) refreshToken(ctx context.Context, oauthType, refreshToken, proxyURL string) (*GeminiTokenInfo, error) {
	var lastErr error

	for attempt := range 4 {
		if attempt > 0 {
			backoff := min(time.Duration(1<<uint(attempt-1))*time.Second, 30*time.Second)
			// Space transient exchange attempts after failure. Sleep ignores
			// cancellation, so registered work completes only after this fixed delay
			// returns.
			time.Sleep(backoff)
		}

		tokenResp, err := s.Client.RefreshToken(ctx, oauthType, refreshToken, proxyURL)
		if err == nil {
			// Subtract a refresh margin but floor stored lifetime at thirty seconds.
			// The adjusted timestamp can exceed short upstream lifetimes even though
			// ExpiresIn still reports the issued lifetime.
			const safetyWindow = 300 // 5 minutes
			const minTTL = 30        // minimum 30 seconds
			expiresAt := time.Now().Unix() + tokenResp.ExpiresIn - safetyWindow
			minExpiresAt := time.Now().Unix() + minTTL
			if expiresAt < minExpiresAt {
				expiresAt = minExpiresAt
			}
			return &GeminiTokenInfo{
				AccessToken:  tokenResp.AccessToken,
				RefreshToken: tokenResp.RefreshToken,
				TokenType:    tokenResp.TokenType,
				ExpiresIn:    tokenResp.ExpiresIn,
				ExpiresAt:    expiresAt,
				Scope:        tokenResp.Scope,
			}, nil
		}

		if IsNonRetryableGeminiOAuthError(err) {
			return nil, err
		}
		lastErr = err
	}

	return nil, fmt.Errorf("token refresh failed after retries: %w", lastErr)
}

// IsNonRetryableGeminiOAuthError checks four case-sensitive OAuth substrings
// in a nonnil error. invalid_grant, invalid_client, unauthorized_client, and
// access_denied stop retries; other diagnostic text remains subject to the
// caller retry schedule.
func IsNonRetryableGeminiOAuthError(err error) bool {
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

// RefreshProviderToken registers renewal for a stable nonnil Gemini OAuth
// record with a nonblank refresh token. Empty stored type defaults to Code
// Assist; unresolved selected proxy lookup uses an empty URL.
//
// Client mismatch may retry through another variant, but returned OAuthType
// remains the stored variant. Existing project survives renewal. Code Assist
// canonicalizes stored tier, defaults it, and discovers a missing project.
// Google One refreshes Drive metadata after its twenty-four-hour timestamp
// gate and retains existing tier when discovery is unusable. Discovery
// failures have variant-specific consequences, and metadata is returned for
// caller persistence.
// Pass a nonnil context for independent activity registration.
func (s *GeminiAuthorization) RefreshProviderToken(ctx context.Context, provider *Record) (*GeminiTokenInfo, error) {
	operation, done, startErr := s.activity.begin(ctx, errors.New("gemini authorization is stopped"))
	if startErr != nil {
		return nil, startErr
	}
	defer done()
	ctx = operation

	if provider.Platform != PlatformGemini || provider.Type != ProviderTypeOAuth {
		return nil, fmt.Errorf("provider is not a Gemini OAuth provider")
	}

	refreshToken := provider.GetCredential("refresh_token")
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("no refresh token available")
	}

	// Empty stored variant text selects Code Assist renewal. The returned
	// metadata keeps that selected variant even when client-mismatch recovery
	// uses a different exchange path.
	oauthType := strings.TrimSpace(provider.GetCredential("oauth_type"))
	if oauthType == "" {
		oauthType = "code_assist"
	}

	// This flow has no saved session route to recover. Unavailable selected
	// proxy lookup leaves an empty URL for the configured Google transport
	// callbacks.
	var proxyURL string
	if provider.ProxyID != nil {
		if resolved, ok := s.Options.ResolveProxy(ctx, *provider.ProxyID); ok {
			proxyURL = resolved
		}
	}

	// Attempt the stored variant before client-mismatch alternatives. Alternate
	// failure retains the initial error, while success still reports the stored
	// OAuth variant in returned credential metadata.
	tokenInfo, err := s.refreshToken(ctx, oauthType, refreshToken, proxyURL)
	// A Code Assist unauthorized_client error can retry through AI Studio
	// when custom credentials are configured. Preserve the initial error if that
	// alternate fails.
	if err != nil && oauthType == "code_assist" && strings.Contains(err.Error(), "unauthorized_client") && s.GetOAuthConfig().AIStudioOAuthEnabled {
		if alt, altErr := s.refreshToken(ctx, "ai_studio", refreshToken, proxyURL); altErr == nil {
			tokenInfo = alt
			err = nil
		}
	}
	// Google One unauthorized_client selects a Code Assist retry when custom
	// configuration is available. The standard OAuthClient forces built-in
	// identity for both variants.
	if err != nil && oauthType == "google_one" && strings.Contains(err.Error(), "unauthorized_client") && s.GetOAuthConfig().AIStudioOAuthEnabled {
		if alt, altErr := s.refreshToken(ctx, "code_assist", refreshToken, proxyURL); altErr == nil {
			tokenInfo = alt
			err = nil
		}
	}
	if err != nil {
		// Provide a more actionable error for common OAuth client mismatch issues.
		if strings.Contains(err.Error(), "unauthorized_client") {
			return nil, fmt.Errorf("%w (OAuth client mismatch: the refresh_token is bound to the OAuth client used during authorization; please re-authorize this provider or restore the original GEMINI_OAUTH_CLIENT_ID/SECRET)", err)
		}
		return nil, err
	}

	tokenInfo.OAuthType = oauthType

	// Renewal itself returns no project metadata. Keep the nonblank stored
	// project so the selected account can remain usable without another discovery
	// request.
	existingProjectID := strings.TrimSpace(provider.GetCredential("project_id"))
	if existingProjectID != "" {
		tokenInfo.ProjectID = existingProjectID
	}

	existingTierID := strings.TrimSpace(provider.GetCredential("tier_id"))

	// For Code Assist, project_id is required. Auto-detect if missing.
	// For AI Studio OAuth, project_id is optional and should not block refresh.
	switch oauthType {
	case "code_assist":
		// Use the stored canonical tier, or the standard Code Assist tier when it is
		// missing.
		if existingTierID != "" {
			tokenInfo.TierID = CanonicalGeminiTierIDForOAuthType(oauthType, existingTierID)
		}
		if tokenInfo.TierID == "" {
			tokenInfo.TierID = GeminiTierGCPStandard
		}

		// Missing project metadata triggers discovery after assigning a provisional
		// tier. A compatible discovered tier can replace that provisional choice
		// before the final project requirement is checked.
		needDetect := strings.TrimSpace(tokenInfo.ProjectID) == "" || tokenInfo.TierID == ""
		if needDetect {
			projectID, tierID, err := s.fetchProjectID(ctx, tokenInfo.AccessToken, proxyURL)
			if err != nil {
				s.Options.Printf("[GeminiOAuth] Warning: failed to auto-detect project/tier: %v\n", err)
			} else {
				if strings.TrimSpace(tokenInfo.ProjectID) == "" && projectID != "" {
					tokenInfo.ProjectID = projectID
				}
				if tierID != "" {
					if canonical := CanonicalGeminiTierIDForOAuthType(oauthType, tierID); canonical != "" {
						tokenInfo.TierID = canonical
					}
				}
			}
		}

		if strings.TrimSpace(tokenInfo.ProjectID) == "" {
			return nil, fmt.Errorf("failed to auto-detect project_id: empty result")
		}
	case "google_one":
		canonicalExistingTier := CanonicalGeminiTierIDForOAuthType(oauthType, existingTierID)
		// Reuse a parseable Drive tier timestamp no older than twenty-four hours.
		// Future timestamps satisfy this elapsed-time gate too, postponing another
		// discovery attempt.
		needsRefresh := true
		if provider.Extra != nil {
			if updatedAtStr, ok := provider.Extra["drive_tier_updated_at"].(string); ok {
				if updatedAt, err := time.Parse(time.RFC3339, updatedAtStr); err == nil {
					if time.Since(updatedAt) <= 24*time.Hour {
						needsRefresh = false
						tokenInfo.TierID = canonicalExistingTier
					}
				}
			}
		}

		if tokenInfo.TierID == "" {
			tokenInfo.TierID = canonicalExistingTier
		}

		// Stale or missing Drive age triggers discovery. Errors or unusable tier
		// data retain existing canonical metadata and let the final default rule
		// supply a tier; successful storage data contributes a fresh Extra map.
		if needsRefresh {
			tierID, storageInfo, err := s.fetchGoogleOneTier(ctx, tokenInfo.AccessToken, proxyURL)
			if err == nil {
				if canonical := CanonicalGeminiTierIDForOAuthType(oauthType, tierID); canonical != "" && canonical != GeminiTierGoogleOneUnknown {
					tokenInfo.TierID = canonical
				}
				if storageInfo != nil {
					tokenInfo.Extra = map[string]any{
						"drive_storage_limit":   storageInfo.Limit,
						"drive_storage_usage":   storageInfo.Usage,
						"drive_tier_updated_at": time.Now().Format(time.RFC3339),
					}
				}
			}
		}

		// An unresolved tier keeps a compatible stored value where available.
		// Otherwise use the free Google One label so renewal returns displayable
		// tier metadata even when Drive discovery did not identify the plan.
		if tokenInfo.TierID == "" || tokenInfo.TierID == GeminiTierGoogleOneUnknown {
			if canonicalExistingTier != "" {
				tokenInfo.TierID = canonicalExistingTier
			} else {
				tokenInfo.TierID = GeminiTierGoogleOneFree
			}
		}
	}

	return tokenInfo, nil
}

// BuildProviderCredentials constructs credentials from nonnil token
// information. Access token and decimal Unix expiry are included; nonempty
// optional fields are copied, and structurally invalid tier is diagnosed and
// omitted.
//
// Extra is copied into credentials last. Its keys can override token,
// identity, or validated tier entries, and nested references remain borrowed
// because maps.Copy is shallow. Normal Google One results supply Drive
// metadata keys, while other callers choose Extra keys and synchronize mutable
// values before persistence.
func (s *GeminiAuthorization) BuildProviderCredentials(tokenInfo *GeminiTokenInfo) map[string]any {
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
	if tokenInfo.Scope != "" {
		creds["scope"] = tokenInfo.Scope
	}
	if tokenInfo.ProjectID != "" {
		creds["project_id"] = tokenInfo.ProjectID
	}
	if tokenInfo.TierID != "" {
		// Validate structure before storing the selected tier. Failure omits that
		// entry while credential construction keeps the other exchanged values
		// available.
		if err := ValidateTierID(tokenInfo.TierID); err == nil {
			creds["tier_id"] = tokenInfo.TierID
			s.Options.Printf("[GeminiOAuth] Storing tier_id: %s\n", tokenInfo.TierID)
		} else {
			s.Options.Printf("[GeminiOAuth] Invalid tier_id %s: %v\n", tokenInfo.TierID, err)
		}
	}
	if tokenInfo.OAuthType != "" {
		creds["oauth_type"] = tokenInfo.OAuthType
	}
	// Merge metadata after token fields. A colliding Extra key wins, and nested
	// references remain shared with tokenInfo because this map copy is shallow.
	if len(tokenInfo.Extra) > 0 {
		maps.Copy(creds, tokenInfo.Extra)
	}
	return creds
}

// fetchProjectID obtains a project and tier through Code Assist, onboarding,
// or Resource Manager. Missing CodeAssist fails; callers provide reached
// FetchProject and Logf callbacks and nonnil successful onboarding responses.
//
// A load project returns directly. Registered tier without project skips
// onboarding and tries Resource Manager; otherwise a default or LEGACY tier is
// polled up to five times. Each pending result sleeps two seconds, including
// the final attempt. Completed string or object IDs return even when blank.
// Other outcomes try Resource Manager and fold its failure into the selected
// diagnostic; tier can accompany project failure.
func (s *GeminiAuthorization) fetchProjectID(ctx context.Context, accessToken, proxyURL string) (string, string, error) {
	if s.CodeAssist == nil {
		return "", "", errors.New("code assist client not configured")
	}

	loadResp, loadErr := s.CodeAssist.LoadCodeAssist(ctx, accessToken, proxyURL, nil)

	// Retain tier data even while project discovery is unresolved.
	// Resource Manager and onboarding results can then return that useful
	// account observation alongside a missing-project diagnostic.
	tierID := "LEGACY"
	if loadResp != nil {
		if tier := loadResp.GetTier(); tier != "" {
			tierID = tier
		} else {
			tierID = ExtractTierIDFromAllowedTiers(loadResp.AllowedTiers)
		}
	}

	if loadErr == nil && loadResp != nil && strings.TrimSpace(loadResp.CloudAICompanionProject) != "" {
		return strings.TrimSpace(loadResp.CloudAICompanionProject), tierID, nil
	}

	// Registered tier metadata selects Resource Manager before onboarding. A
	// failed or blank project lookup retains the tier observation with a request
	// for manually supplied project identity.
	if loadResp != nil {
		registeredTierID := strings.TrimSpace(loadResp.GetTier())
		if registeredTierID != "" {
			s.Options.Logf("[GeminiOAuth] User has tier (%s) but no cloudaicompanionProject, trying Cloud Resource Manager...", registeredTierID)

			// A registered tier without project can still use
			// Resource Manager. Failed or blank fallback becomes
			// the missing-project diagnostic returned with the
			// observed tier.
			fallback, fbErr := s.Options.FetchProject(ctx, accessToken, proxyURL)
			if fbErr == nil && strings.TrimSpace(fallback) != "" {
				s.Options.Logf("[GeminiOAuth] Found project from Cloud Resource Manager: %s", fallback)
				return strings.TrimSpace(fallback), tierID, nil
			}

			s.Options.Logf("[GeminiOAuth] No project found from Cloud Resource Manager, user must provide project_id manually")
			return "", tierID, fmt.Errorf("user is registered (tier: %s) but no project_id available. Please provide Project ID manually in the authorization form, or create a project at https://console.cloud.google.com", registeredTierID)
		}
	}

	// Without currentTier or paidTier, try onboarding as a new user.
	s.Options.Logf("[GeminiOAuth] No currentTier/paidTier found, proceeding with onboardUser (tierID: %s)", tierID)

	req := &geminiwire.OnboardUserRequest{
		TierID: tierID,
		Metadata: geminiwire.LoadCodeAssistMetadata{
			IDEType:    "ANTIGRAVITY",
			Platform:   "PLATFORM_UNSPECIFIED",
			PluginType: "GEMINI",
		},
	}

	// Bound polling of pending onboarding responses before Resource Manager
	// fallback. Reusing the selected request keeps tier and account metadata
	// consistent across those finite attempts.
	maxAttempts := 5
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := s.CodeAssist.OnboardUser(ctx, accessToken, proxyURL, req)
		if err != nil {
			// Onboarding failure may still leave a Resource
			// Manager project available. Failed or blank fallback
			// retains the original onboarding error with the
			// selected tier.
			fallback, fbErr := s.Options.FetchProject(ctx, accessToken, proxyURL)
			if fbErr == nil && strings.TrimSpace(fallback) != "" {
				return strings.TrimSpace(fallback), tierID, nil
			}
			return "", tierID, err
		}
		// Completed onboarding may encode the project as string or object ID.
		// Extract that representation before fallback, retaining even an explicitly
		// blank returned identifier.
		if resp.Done {
			if resp.Response != nil && resp.Response.CloudAICompanionProject != nil {
				switch v := resp.Response.CloudAICompanionProject.(type) {
				case string:
					return strings.TrimSpace(v), tierID, nil
				case map[string]any:
					if id, ok := v["id"].(string); ok {
						return strings.TrimSpace(id), tierID, nil
					}
				}
			}

			// Completed onboarding without a recognized project
			// tries Resource Manager. Failed or blank fallback
			// returns the completed-without-project diagnostic and
			// retains the selected tier.
			fallback, fbErr := s.Options.FetchProject(ctx, accessToken, proxyURL)
			if fbErr == nil && strings.TrimSpace(fallback) != "" {
				return strings.TrimSpace(fallback), tierID, nil
			}
			return "", tierID, errors.New("onboardUser completed but no project_id returned")
		}
		// Pending onboarding is polled after fixed spacing. The last pending
		// attempt also sleeps before fallback, and cancellation does not wake this
		// delay.
		time.Sleep(2 * time.Second)
	}

	// Pending onboarding exhausted its finite polls. Try Resource Manager
	// before choosing the timeout diagnostic, retaining a prior load
	// failure when it supplies useful context.
	fallback, fbErr := s.Options.FetchProject(ctx, accessToken, proxyURL)
	if fbErr == nil && strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback), tierID, nil
	}
	if loadErr != nil {
		return "", tierID, fmt.Errorf("loadCodeAssist failed (%v) and onboardUser timeout after %d attempts", loadErr, maxAttempts)
	}
	return "", tierID, fmt.Errorf("onboardUser timeout after %d attempts", maxAttempts)
}

// RefreshToken registers renewal separately with authorization activity and
// releases it after refreshToken returns. Nested public calls also register
// themselves; shutdown waits for actual completion, including unconditional
// finite retry sleeps.
// Pass a nonnil context for independent activity registration.
func (s *GeminiAuthorization) RefreshToken(ctx context.Context, kind, token, proxy string) (*GeminiTokenInfo, error) {
	operation, done, err := s.activity.begin(ctx, errors.New("gemini authorization is stopped"))
	if err != nil {
		return nil, err
	}
	defer done()
	return s.refreshToken(operation, kind, token, proxy)
}

// FetchGoogleOneTier registers Drive discovery for shutdown waiting. The
// private query defines required clients and nonnil successful data;
// cancellation reaches its cooperative request, and registration ends on
// return.
// Pass a nonnil context for independent activity registration.
func (s *GeminiAuthorization) FetchGoogleOneTier(ctx context.Context, token, proxy string) (string, *google.DriveStorageInfo, error) {
	operation, done, err := s.activity.begin(ctx, errors.New("gemini authorization is stopped"))
	if err != nil {
		return "", nil, err
	}
	defer done()
	return s.fetchGoogleOneTier(operation, token, proxy)
}

// FetchProject registers project discovery for shutdown waiting using the
// supplied token and proxy. Delegated fallback and onboarding rules apply,
// including unconditional sleeps that can outlast a caller stop budget.
// Pass a nonnil context for independent activity registration.
func (s *GeminiAuthorization) FetchProject(ctx context.Context, token, proxy string) (string, string, error) {
	operation, done, err := s.activity.begin(ctx, errors.New("gemini authorization is stopped"))
	if err != nil {
		return "", "", err
	}
	defer done()
	return s.fetchProjectID(operation, token, proxy)
}
