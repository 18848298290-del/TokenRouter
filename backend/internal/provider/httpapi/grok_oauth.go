package httpapi

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/httpapi/dto"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// grok_oauth.go handles Grok authorization, provider imports, credential
// reconciliation, and quota HTTP requests.
// Grok authorization distinguishes browser sessions, raw refresh text and SSO
// import material before provider persistence. The HTTP refresh path accepts a
// legacy rt alias but requires a selected proxy to resolve successfully. Import
// responses contain redacted created records and preserve each failed input's
// position separately from successful conversion.
//
// Credential reconciliation requires complementary dry-run and apply flags,
// defaulting omission to planning. The handler limits refresh-window seconds
// before converting the duration and delegates identity checks to the
// reconciler. Quota queries use the shared service, and its unsupported reset
// result remains a domain error; runtime diagnostics comes from a separately
// supplied sanitized callback.

// GrokGenerateAuthURLRequest selects optional proxy and callback hints for a
// Grok session.
type GrokGenerateAuthURLRequest struct {
	// ProxyID selects the proxy retained by the new session. Omission supplies an
	// empty route; lookup failures or missing selected records reject generation.
	ProxyID *int64 `json:"proxy_id"` // selected egress proxy
	// RedirectURI is trimmed before session creation. Blank input selects the
	// configured upstream callback default, and the session retains that effective
	// URI for exchange.
	RedirectURI string `json:"redirect_uri"` // authorization callback URI
}

// GrokExchangeCodeRequest carries the required Grok session and authorization
// input for exchange.
type GrokExchangeCodeRequest struct {
	SessionID string `json:"session_id" binding:"required"` // authorization session identifier
	Code      string `json:"code" binding:"required"`       // authorization exchange code
	// State can be omitted from code-exchange input when authorization derives it
	// from pasted code or callback text. The resolved value still needs to match
	// the stored session.
	State string `json:"state"` // authorization state value
	// RedirectURI is an optional consistency check after trimming. Nonblank input
	// must match the stored session callback; exchange uses the stored URI.
	RedirectURI string `json:"redirect_uri"` // authorization callback URI
	// ProxyID overrides the session route when supplied and successfully resolved.
	// Omission retains that route; lookup failures or missing selected records
	// reject exchange.
	ProxyID *int64 `json:"proxy_id"` // selected egress proxy
}

// GrokRefreshTokenRequest carries current or legacy refresh-token text and
// optional client settings.
type GrokRefreshTokenRequest struct {
	// RefreshToken and RT carry current and legacy token text. Trimmed nonempty
	// RefreshToken takes precedence, while blank selection falls back to trimmed
	// RT.
	RefreshToken string `json:"refresh_token"` // supplied OAuth refresh token
	RT           string `json:"rt"`            // legacy refresh token alias
	// ClientID is forwarded unchanged to authorization. The configured upstream
	// client trims it and selects its effective default for blank text before
	// constructing the refresh form.
	ClientID string `json:"client_id"` // OAuth client identifier
	// ProxyID requires a successful found lookup when supplied. Lookup errors or
	// missing records stop raw refresh; omission sends the empty proxy URL.
	ProxyID *int64 `json:"proxy_id"` // selected egress proxy
}

// GrokSSOTokenRequest supplies browser SSO material and optional proxy
// selection for token conversion.
type GrokSSOTokenRequest struct {
	// SSOToken is trimmed and validated by authorization before conversion. Blank
	// input is rejected, and successful token information excludes this raw cookie
	// value.
	SSOToken string `json:"sso_token"` // browser SSO cookie text
	// ProxyID selects SSO conversion transport. Omission uses the empty route;
	// lookup failures or missing selected records reject authorization before
	// conversion.
	ProxyID *int64 `json:"proxy_id"` // selected egress proxy
}

// GrokPasswordAuthorizeRequest carries account login input for the optional SSO
// password flow.
type GrokPasswordAuthorizeRequest struct {
	// Email is trimmed after password authorization is enabled. Blank text is
	// rejected before login, and the trimmed value reaches the configured client.
	Email string `json:"email"` // account login email
	// Password is checked for blank text after trimming but sent to login as
	// supplied. Successful token information excludes the raw login password.
	Password string `json:"password"` // account login password
	// ProxyID selects transport after password capability validation. Omission uses
	// the empty route; lookup failures or missing selected records reject the
	// enabled authorization flow.
	ProxyID *int64 `json:"proxy_id"` // selected egress proxy
}

// GrokOAuthReconcileRequest selects reconciliation mode, cursor, population
// limit and token refresh window.
type GrokOAuthReconcileRequest struct {
	// DryRun and Apply select complementary modes after DryRun defaults to true.
	// Mutation requires Apply=true and DryRun=false; equal flags are rejected
	// before reconciliation.
	DryRun *bool `json:"dry_run"` // optional planning-only mode selector
	Apply  bool  `json:"apply"`   // explicit mutation mode selector
	// AfterID is forwarded as the exclusive population cursor. The production
	// reconciler rejects negative values and starts from its initial cursor when
	// zero is supplied.
	AfterID int64 `json:"after_id"` // exclusive scan cursor identifier
	// Limit zero selects the production reconciler's fifty-record default.
	// Supplied limits must be between one and five hundred before population
	// scanning begins.
	Limit int `json:"limit"` // requested provider scan capacity
	// RefreshWindowSeconds is admitted from zero through one day before duration
	// conversion. The production reconciler uses its configured Skew for zero
	// input and raises any smaller admitted lookahead to that same minimum.
	RefreshWindowSeconds int64 `json:"refresh_window_seconds"` // seconds
}

// GrokSSOToOAuthRequest reuses the core request for per-item SSO conversion and
// provider creation.
type GrokSSOToOAuthRequest = providercore.GrokSSOToOAuthRequest

// GrokSSOToOAuthItemResult presents one source item with a redacted provider or
// failure text.
type GrokSSOToOAuthItemResult struct {
	Index    int           `json:"index"`              // original SSO input position
	Name     string        `json:"name,omitempty"`     // provider display name
	Email    string        `json:"email,omitempty"`    // account login email
	Provider *dto.Provider `json:"provider,omitempty"` // optional redacted created provider
	Error    string        `json:"error,omitempty"`    // source item failure message
}

// GrokSSOToOAuthResponse separates successfully created providers from failed
// SSO import items.
type GrokSSOToOAuthResponse struct {
	Created []GrokSSOToOAuthItemResult `json:"created"` // successful SSO import items
	Failed  []GrokSSOToOAuthItemResult `json:"failed"`  // failed SSO import items
}

// GrokOAuthReconciler supplies dry-run and apply credential reconciliation for
// administrator selections.
type GrokOAuthReconciler interface {
	// ReconcileGrokOAuth scans the requested population and returns planned or
	// applied credential outcomes. The core owns identity checks and bounded
	// refresh work; callers select complementary DryRun and Apply modes before
	// invocation.
	ReconcileGrokOAuth(context.Context, providercore.GrokOAuthReconcileInput) (*providercore.GrokOAuthReconcileResult, error)
}

// GrokOAuthHTTPOptions supplies proxy lookup, sanitized runtime diagnostics and
// optional reconciliation.
type GrokOAuthHTTPOptions struct {
	// ProxyURL resolves a selected raw-refresh proxy into URL, found flag and
	// error. Lookup errors or a false found flag reject the request before Grok
	// token exchange.
	ProxyURL func(context.Context, int64) (string, bool, error) // selected proxy URL lookup
	// RuntimeSanity is required when the diagnostics route is mounted. Its
	// callback selects sanitized output before the handler sends the returned value.
	RuntimeSanity func() any // sanitized runtime diagnostics callback
	// Reconciler is optional during construction. A reconciliation request
	// returns the unavailable response when this implementation was not supplied.
	Reconciler GrokOAuthReconciler // optional credential reconciliation service
}

// GrokOAuthHandler binds Grok authorization, imports, quota observations and
// reconciliation to administration.
type GrokOAuthHandler struct {
	grokOAuthService *providercore.GrokAuthorization  // borrowed Grok authorization instance
	imports          *providercore.GrokProviderImport // borrowed Grok provider importer
	quotaService     *providercore.GrokQuotaService   // optional Grok quota maintenance
	reconciler       GrokOAuthReconciler              // optional credential reconciliation service
	options          GrokOAuthHTTPOptions             // HTTP lookup and diagnostics callbacks
}

// NewGrokOAuthHandler borrows authorization, import, quota and optional
// reconciliation for administrator routes. Mounted endpoints require their
// reached dependencies; the owner manages session cleanup and query shutdown
// for the supplied instances.
func NewGrokOAuthHandler(auth *providercore.GrokAuthorization, imports *providercore.GrokProviderImport, quota *providercore.GrokQuotaService, options GrokOAuthHTTPOptions) *GrokOAuthHandler {
	return &GrokOAuthHandler{grokOAuthService: auth, imports: imports, quotaService: quota, reconciler: options.Reconciler, options: options}
}

// GetCapabilities returns the authorization service's current Grok login
// capability display. The service controls whether password authorization is
// enabled, and the HTTP wrapper encodes its result through the common success
// envelope.
func (h *GrokOAuthHandler) GetCapabilities(c *gin.Context) {
	response.Success(c, h.grokOAuthService.GetCapabilities())
}

// GenerateAuthURL returns a Grok authorization session using optional proxy and
// callback hints. Any JSON binding error discards the decoded request and
// selects defaults; authorization errors retain their shared domain mapping.
func (h *GrokOAuthHandler) GenerateAuthURL(c *gin.Context) {
	var req GrokGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// URL generation accepts omitted JSON. Discard partially decoded values
		// after any binding error so default proxy and callback selection remain together.
		req = GrokGenerateAuthURLRequest{}
	}
	result, err := h.grokOAuthService.GenerateAuthURL(c.Request.Context(), req.ProxyID, req.RedirectURI)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// ExchangeCode binds the required Grok session and authorization input before
// exchange. State and callback checks belong to authorization, which can derive
// state from pasted input and returns domain failures through the shared
// mapping.
func (h *GrokOAuthHandler) ExchangeCode(c *gin.Context) {
	var req GrokExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	tokenInfo, err := h.grokOAuthService.ExchangeCode(c.Request.Context(), &providercore.GrokExchangeCodeInput{
		SessionID: req.SessionID,

		Code: req.Code,

		State: req.State,

		RedirectURI: req.RedirectURI,

		ProxyID: req.ProxyID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}

// RefreshToken exchanges trimmed refresh-token text, preferring refresh_token
// over its rt alias. Empty token selection returns 400. A requested proxy needs
// a successful found lookup, so errors or missing records stop this endpoint
// before exchange.
//
// The raw client_id value is passed to authorization. The configured upstream
// client trims that value and selects its effective default when blank.
// Successful token information is returned for later provider use. This handler owns request binding and proxy error mapping,
// while authorization owns token exchange and optional session-independent
// metadata handling.
func (h *GrokOAuthHandler) RefreshToken(c *gin.Context) {
	var req GrokRefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	// Current and compatibility spellings feed one exchange. Prefer populated
	// current input so the rt alias cannot replace that same request's selected credential.
	refreshToken := strings.TrimSpace(req.RefreshToken)
	if refreshToken == "" {
		refreshToken = strings.TrimSpace(req.RT)
	}
	if refreshToken == "" {
		response.BadRequest(c, "refresh_token is required")
		return
	}

	var proxyURL string
	if req.ProxyID != nil {
		value, found, err := h.options.ProxyURL(c.Request.Context(), *req.ProxyID)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		if !found {
			response.BadRequest(c, "GROK_OAUTH_PROXY_NOT_FOUND: proxy not found")
			return
		}
		proxyURL = value
	}
	tokenInfo, err := h.grokOAuthService.RefreshToken(c.Request.Context(), refreshToken, proxyURL, req.ClientID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}

// ValidateSSOToken exchanges supplied Web SSO material for Build OAuth token
// information. Authorization validates the input and selected proxy, and the
// response returns its token metadata while raw cookie handling remains in the
// use case.
func (h *GrokOAuthHandler) ValidateSSOToken(c *gin.Context) {
	var req GrokSSOTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	tokenInfo, err := h.grokOAuthService.ValidateSSOToken(c.Request.Context(), req.SSOToken, req.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}

// AuthorizePassword forwards email, password and optional proxy to the enabled
// SSO authorization flow. The returned token information omits login secrets,
// while capability checks and credential failures retain their domain response
// mapping.
func (h *GrokOAuthHandler) AuthorizePassword(c *gin.Context) {
	var req GrokPasswordAuthorizeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	tokenInfo, err := h.grokOAuthService.AuthorizePassword(c.Request.Context(), req.Email, req.Password, req.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}

// RefreshProviderToken requests import maintenance for the decimal
// path-selected Grok provider and returns a redacted DTO. GrokImportInputError
// maps to 400, while other failures retain their shared domain mapping.
func (h *GrokOAuthHandler) RefreshProviderToken(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}
	value, err := h.imports.RefreshProvider(c.Request.Context(), id)
	if err != nil {
		if input, ok := errors.AsType[*providercore.GrokImportInputError](err); ok {
			response.BadRequest(c, input.Message)
		} else {
			response.ErrorFrom(c, err)
		}
		return
	}
	response.Success(c, dto.ProviderFromRecord(value))
}

// ReconcileOAuthProviders binds an administrator reconciliation selection with
// dry_run=true by default. Apply and the effective dry-run value must differ,
// so mutation requires apply=true with dry_run=false. Refresh-window seconds
// are admitted from zero through one day before conversion.
//
// A missing reconciler returns 500 after input validation. The core receives
// cursor, limit and duration and owns its population rules and identity-checked
// maintenance. Dry-run output exposes planned outcomes, while apply output
// describes performed work; handler validation prevents omitted or conflicting
// flags from accidentally choosing mutation.
func (h *GrokOAuthHandler) ReconcileOAuthProviders(c *gin.Context) {
	var req GrokOAuthReconcileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request")
		return
	}
	dryRun := true
	if req.DryRun != nil {
		dryRun = *req.DryRun
	}
	if req.Apply == dryRun {
		response.ErrorFrom(c, providercore.ErrGrokOAuthReconcileMode)
		return
	}
	// Reconciliation accepts at most one day of refresh lookahead. Bound the
	// HTTP seconds value before duration multiplication and the core's skew normalization.
	if req.RefreshWindowSeconds < 0 || req.RefreshWindowSeconds > int64((24*time.Hour)/time.Second) {
		response.ErrorFrom(c, providercore.ErrGrokOAuthReconcileWindow)
		return
	}
	if h.reconciler == nil {
		response.InternalError(c, "Grok OAuth reconciliation service is unavailable")
		return
	}
	result, err := h.reconciler.ReconcileGrokOAuth(c.Request.Context(), providercore.GrokOAuthReconcileInput{
		DryRun: dryRun,

		Apply: req.Apply,

		AfterID: req.AfterID,

		Limit: req.Limit,

		// The HTTP value uses seconds, while the reconciler consumes a duration.
		RefreshWindow: time.Duration(req.RefreshWindowSeconds) * time.Second,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// CreateProviderFromOAuth strictly binds the required session and code and
// delegates Grok provider creation. The importer owns authorization session
// exchange, configuration persistence and follow-up dispatch, and the HTTP
// result redacts provider credentials.
func (h *GrokOAuthHandler) CreateProviderFromOAuth(c *gin.Context) {
	var req GrokOAuthProviderCreateRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	value, err := h.imports.CreateFromOAuth(c.Request.Context(), providercore.GrokOAuthProviderCreateInput{
		SessionID:   req.SessionID,
		Code:        req.Code,
		State:       req.State,
		RedirectURI: req.RedirectURI,
		ProxyID:     req.ProxyID,
		Name:        req.Name,
		Concurrency: req.Concurrency,
		Priority:    req.Priority,
		GroupIDs:    req.GroupIDs,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.ProviderFromRecord(value))
}

// CreateProvidersFromSSO strictly binds a core SSO import request and returns
// created and failed item arrays. Core input errors map to 400; each successful
// provider is redacted before the separate outcome collections are encoded.
func (h *GrokOAuthHandler) CreateProvidersFromSSO(c *gin.Context) {
	var req GrokSSOToOAuthRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.imports.CreateFromSSO(c.Request.Context(), req)
	if err != nil {
		if errors.Is(err, providercore.ErrGrokSSOInput) {
			response.BadRequest(c, err.Error())
		} else {
			response.ErrorFrom(c, err)
		}
		return
	}
	out := GrokSSOToOAuthResponse{Created: make([]GrokSSOToOAuthItemResult, 0, len(result.Created)), Failed: make([]GrokSSOToOAuthItemResult, 0, len(result.Failed))}
	convert := func(item providercore.GrokSSOToOAuthItemResult) GrokSSOToOAuthItemResult {
		return GrokSSOToOAuthItemResult{Index: item.Index, Name: item.Name, Email: item.Email, Error: item.Error, Provider: dto.ProviderFromRecord(item.Provider)}
	}
	for _, v := range result.Created {
		out.Created = append(out.Created, convert(v))
	}
	for _, v := range result.Failed {
		out.Failed = append(out.Failed, convert(v))
	}
	response.Success(c, out)
}

// QueryQuota returns the quota service's live Grok observation for a decimal
// provider identifier. A nil service returns 400, while query eligibility and
// upstream maintenance belong to the shared quota use case.
func (h *GrokOAuthHandler) QueryQuota(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}
	if h.quotaService == nil {
		response.BadRequest(c, "grok quota service is not enabled")
		return
	}
	result, err := h.quotaService.QueryQuota(c.Request.Context(), providerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// ResetQuota delegates to the Grok quota-reset operation after decimal ID
// validation and service availability. The current service reports
// GROK_QUOTA_RESET_UNSUPPORTED through domain error mapping because its
// configured OAuth reset operation is unavailable.
func (h *GrokOAuthHandler) ResetQuota(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}
	if h.quotaService == nil {
		response.BadRequest(c, "grok quota service is not enabled")
		return
	}
	// The configured quota service reports GROK_QUOTA_RESET_UNSUPPORTED for this
	// operation. Keep the domain response mapping so administrators receive that typed reason.
	//nolint:staticcheck // SA4023
	result, err := h.quotaService.ResetQuota(c.Request.Context(), providerID)
	if err != nil { //nolint:staticcheck // SA4023
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// RuntimeSanity returns the supplied diagnostics callback's sanitized Grok
// configuration display. The callback is required when this route is mounted
// and owns field selection before the HTTP wrapper encodes the result.
func (h *GrokOAuthHandler) RuntimeSanity(c *gin.Context) {
	response.Success(c, h.options.RuntimeSanity())
}
