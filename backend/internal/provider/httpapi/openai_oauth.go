package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	wire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/httpapi/dto"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// openai_oauth.go handles OpenAI authorization, provider imports, and quota
// actions for administrator HTTP requests.
// OpenAI authorization returns exchange information, while provider imports
// additionally create or edit administrative records and redact their response
// credentials. Raw refresh input supports both refresh_token and rt, with
// current-field precedence, optional client defaults and a proxy lookup policy
// that can continue with an empty URL after a lookup failure.
//
// Quota reset consumption has separate upstream, state-recovery, snapshot and
// provider-read outcomes. The quota-action use case owns their ordering and
// cancellation budget; this handler exposes flags and warnings so completed
// consumption remains visible when later maintenance is partial. Production
// construction shares those actions with application shutdown, while partial
// test handlers can construct uncached compatibility instances from their
// supplied dependencies.

// OpenAI quota warnings identify post-consumption recovery or cache failures
// while the HTTP response still carries the successful upstream reset result.
const (
	OpenAIQuotaResetWarningCacheRefreshFailed     = providercore.OpenAIQuotaResetWarningCacheRefreshFailed
	OpenAIQuotaResetWarningProviderRecoveryFailed = providercore.OpenAIQuotaResetWarningProviderRecoveryFailed
)

// OpenAIOAuthHandler binds OpenAI authorization, provider imports and quota
// actions to administrator requests.
type OpenAIOAuthHandler struct {
	// QuotaActions is created by the production constructor and registered for
	// application shutdown. A nil field makes partial handlers construct an
	// uncached action instance for that call.
	QuotaActions *providercore.OpenAIQuotaActions // lifecycle-owned quota outcome coordinator
	// Import is created by the production constructor from shared authorization
	// and administration. A nil field makes partial handlers construct an uncached
	// importer from their current dependencies.
	Import        *providercore.OpenAIProviderImport // configured OpenAI provider importer
	Options       OpenAIHTTPOptions                  // HTTP authorization lookup callbacks
	Authorization *providercore.OpenAIAuthorization  // borrowed OpenAI authorization instance
	Admin         OpenAIAdminOperations              // borrowed provider administrative operations
	Quota         OpenAIQuotaService                 // optional upstream quota operations
	// Recovery is optional after confirmed credit consumption. Quota actions
	// report a recovery warning and stop later post-processing when no recovery
	// implementation is configured.
	Recovery OpenAIProviderStateRecoverer // optional post-consumption state recovery
}

// OpenAIQuotaService supplies live usage, reset-credit consumption and quota
// snapshot persistence operations.
type OpenAIQuotaService interface {
	// QueryUsage returns current OpenAI provider quota observations using prepared
	// credentials and transport. The caller handles query errors and may persist a
	// snapshot separately, so a successful read alone does not imply saved cache
	// state.
	QueryUsage(ctx context.Context, providerID int64) (*wire.OpenAIQuotaUsage, error)
	// CacheResetCreditsSnapshot persists the reset-credit observation required for
	// management display. The caller retains live usage if this write fails,
	// marking its cache-persistence flag false rather than discarding the
	// successful query.
	CacheResetCreditsSnapshot(ctx context.Context, providerID int64, credits *wire.OpenAIRateLimitResetCredits) error
	// CachePostResetSnapshot persists usage observed after credit consumption and
	// recovery. A write error leaves completed credit consumption and recovery
	// visible in the action result, with cache refresh reported separately.
	CachePostResetSnapshot(ctx context.Context, providerID int64, usage *wire.OpenAIQuotaUsage) error
	// ResetCredit consumes an available upstream OpenAI reset credit for the
	// provider. The operation uses the supplied cancellation context; callers
	// perform bounded recovery afterward and distinguish consumed results from
	// later maintenance failures.
	ResetCredit(ctx context.Context, providerID int64) (*wire.OpenAIQuotaResetResult, error)
}

// OpenAIProviderStateRecoverer supplies state recovery after a successful
// OpenAI reset-credit consumption.
type OpenAIProviderStateRecoverer interface {
	// RecoverProviderState applies eligible provider runtime recovery under the
	// supplied options and context. The quota-action caller requests token
	// invalidation, retains the administrator scheduling switch and records
	// recovery failure independently of upstream credit consumption.
	RecoverProviderState(ctx context.Context, providerID int64, options providercore.ProviderRecoveryOptions) (*providercore.SuccessfulTestRecovery, error)
}

// OpenAIQuotaResetResponse pairs upstream credit consumption with distinct
// recovery, cache and provider display outcomes.
type OpenAIQuotaResetResponse struct {
	wire.OpenAIQuotaResetResult // successful upstream credit-consumption result
	// Quota is included only after post-reset query and snapshot persistence both
	// succeed. Cache failure leaves this optional usage field absent in the reset
	// outcome.
	Quota *wire.OpenAIQuotaUsage `json:"quota,omitempty"` // post-reset observed quota usage
	// Provider is mapped from the action's later record read. A missing or failed
	// read keeps it absent even after successful upstream credit consumption.
	Provider *dto.Provider `json:"provider,omitempty"`
	// CacheRefreshed reports the post-reset usage query and snapshot-write outcome
	// independently of the later provider read. False leaves confirmed credit
	// consumption visible in the embedded result.
	CacheRefreshed bool `json:"cache_refreshed"` // post-reset usage snapshot saved
	// ProviderStateRecovered becomes true after the recovery dependency returns no
	// error. Missing or failing recovery leaves it false and stops later quota
	// maintenance in the action use case.
	ProviderStateRecovered bool `json:"provider_state_recovered"`
	// WarningCode describes incomplete recovery, cache refresh or provider reread
	// after consumption. The successful upstream consumption remains in the
	// response when these maintenance steps fail.
	WarningCode string `json:"warning_code,omitempty"` // partial maintenance outcome reason
}

// OpenAIQuotaRefreshResponse returns live usage together with its reset-credit
// snapshot persistence result.
type OpenAIQuotaRefreshResponse struct {
	wire.OpenAIQuotaUsage // live queried quota observations
	// CachePersisted reports whether the reset-credit snapshot write succeeded
	// after the live query. False leaves the returned live usage available for
	// administrator display.
	CachePersisted bool `json:"cache_persisted"` // queried reset-credit snapshot saved
}

// OpenAIGenerateAuthURLRequest selects optional proxy and callback values for
// an OpenAI session.
type OpenAIGenerateAuthURLRequest struct {
	// ProxyID selects the generated session's route when lookup finds a proxy.
	// A lookup error rejects generation; omission or a missing result keeps the
	// empty route.
	ProxyID *int64 `json:"proxy_id"` // selected egress proxy
	// RedirectURI supplies the callback retained in the session. Exactly empty
	// input uses OpenAIAuthorization's configured default before URL generation.
	RedirectURI string `json:"redirect_uri"` // authorization callback URI
}

// OpenAIExchangeCodeRequest carries required session, code and state plus
// optional transport selections.
type OpenAIExchangeCodeRequest struct {
	SessionID string `json:"session_id" binding:"required"` // authorization session identifier
	Code      string `json:"code" binding:"required"`       // authorization exchange code
	State     string `json:"state" binding:"required"`      // authorization state value
	// RedirectURI overrides the stored callback when nonempty. Omission retains
	// the authorization session's callback during this token exchange.
	RedirectURI string `json:"redirect_uri"` // authorization callback URI
	// ProxyID overrides the session route when lookup succeeds and finds a proxy.
	// Lookup errors reject exchange; omitted or missing selections retain the
	// stored route.
	ProxyID *int64 `json:"proxy_id"` // selected egress proxy
	// TLSFingerprintRouterID reaches the exchange callback as supplied, with nil
	// becoming zero. This token-information endpoint leaves provider binding to
	// the separate creation operation.
	TLSFingerprintRouterID *int64 `json:"tls_fingerprint_router_id"` // optional exchange TLS-router selector
}

// OpenAIRefreshTokenRequest accepts current or legacy refresh-token fields and
// optional exchange settings.
type OpenAIRefreshTokenRequest struct {
	// RefreshToken and RT accept refresh-token text from current and legacy
	// clients. After trimming, a nonempty RefreshToken wins, with RT used only for
	// a blank primary value.
	RefreshToken string `json:"refresh_token"` // supplied OAuth refresh token
	RT           string `json:"rt"`            // legacy refresh token alias
	// ClientID is trimmed before raw token exchange. Blank text selects the
	// callback-provided OpenAI default, while a nonblank value is forwarded to
	// authorization.
	ClientID string `json:"client_id"` // supplied OAuth client identifier
	// ProxyID selects raw refresh transport after a successful found lookup.
	// Errors and missing results leave the empty proxy URL passed to authorization.
	ProxyID *int64 `json:"proxy_id"` // selected egress proxy
	// TLSFingerprintRouterID is forwarded to raw refresh, with nil becoming zero.
	// No provider record is supplied, so this call has no stored router selection
	// to recover.
	TLSFingerprintRouterID *int64 `json:"tls_fingerprint_router_id"` // optional exchange TLS-router selector
}

// OpenAICodexPATCreateRequest carries a required Codex PAT and optional
// provider creation configuration.
type OpenAICodexPATCreateRequest struct {
	AccessToken string `json:"access_token" binding:"required"` // required Codex PAT text
	// Name is trimmed before PAT creation. Blank input prefers validated email,
	// account ID and user ID before the fixed PAT label.
	Name  string  `json:"name"`  // optional requested provider name
	Notes *string `json:"notes"` // administrator description text
	// GroupIDs supplies the PAT provider's requested group associations. Empty
	// input creates no automatic default association through administrative creation.
	GroupIDs []int64 `json:"group_ids"` // associated routing groups
	// ProxyID is looked up before PAT validation and forwarded to creation.
	// Lookup errors reject import, while a missing result retains the empty
	// validation route.
	ProxyID *int64 `json:"proxy_id"` // selected egress proxy
	// Concurrency and Priority default to three and fifty respectively when nil.
	// Supplied zero remains a value, and negative values are rejected before PAT
	// validation.
	Concurrency *int `json:"concurrency"` // optional provider request capacity
	Priority    *int `json:"priority"`    // optional scheduler priority value
	// RateMultiplier uses administrative defaults when nil. Supplied negative
	// values are rejected before PAT validation; an explicit zero remains supplied.
	RateMultiplier *float64 `json:"rate_multiplier"` // provider billing multiplier
	// LoadFactor uses administrative creation rules when nil or nonpositive.
	// Values above 10000 are rejected before validating the supplied PAT.
	LoadFactor *int `json:"load_factor"` // scheduler capacity override
	// ExpiresAt and AutoPauseOnExpired describe provider expiration separately
	// from OAuth token lifetime. Creation applies a positive Unix timestamp and
	// defaults omitted automatic pause to true.
	ExpiresAt          *int64 `json:"expires_at"`            // Unix seconds
	AutoPauseOnExpired *bool  `json:"auto_pause_on_expired"` // pause on expiry
	// CredentialExtras is sanitized before merging with validated PAT credentials.
	// Validation-owned token and identity keys win, preventing supplied extras
	// from replacing the authenticated result.
	CredentialExtras map[string]any `json:"credential_extras"` // additional credential configuration values
	// Extra is normalized in place before PAT validation and then combined with
	// import provenance. Provenance wins overlapping keys, and normalization can
	// remain after rejected input.
	Extra map[string]any `json:"extra"` // additional provider configuration values
}

// CreateShadowRequest supplies display, scheduling and group configuration for
// a Spark credential shadow.
type CreateShadowRequest struct {
	// Name is trimmed by shadow creation. Blank input selects the parent name
	// followed by the Spark suffix before the administrator applies its name limit.
	Name string `json:"name"` // provider display name
	// Priority and Concurrency inherit the parent's respective values when
	// nonpositive. Positive supplied values reach administrative shadow validation
	// before the child is stored.
	Priority    int `json:"priority"`    // scheduler priority value
	Concurrency int `json:"concurrency"` // provider request capacity
	// GroupIDs replaces the association selection when nonempty. Empty input
	// inherits loaded parent associations when available, otherwise leaving the
	// shadow ungrouped.
	GroupIDs []int64 `json:"group_ids"` // associated routing groups
}

// OpenAIAdminOperations supplies provider configuration reads and mutations
// needed by OpenAI administration.
type OpenAIAdminOperations interface {
	// GetProvider reads provider configuration for refresh and post-reset
	// presentation. The returned record is passed to redacted mapping, while
	// lookup failures retain their use-case classification for the handler to
	// serialize.
	GetProvider(context.Context, int64) (*providercore.Record, error)
	// CreateProvider validates and stores supplied OpenAI creation settings
	// through administration. Import callers own upstream authorization beforehand
	// and then map the returned record into a redacted response with provider
	// identity.
	CreateProvider(context.Context, *providercore.CreateProviderInput) (*providercore.Record, error)
	// UpdateProvider applies supplied administrative configuration to an existing
	// OpenAI provider. Refresh import uses this operation after upstream exchange;
	// omitted fields and sensitive-value retention follow administration's
	// configuration-write rules.
	UpdateProvider(context.Context, int64, *providercore.UpdateProviderInput) (*providercore.Record, error)
	// CreateShadow validates the parent and creates a Spark provider that resolves
	// credentials through that parent. Supplied group and scheduling choices
	// follow administrative shadow rules, and the handler returns its shallow
	// redacted view.
	CreateShadow(context.Context, int64, providercore.ShadowOptions) (*providercore.Record, error)
}

// OpenAIHTTPOptions supplies proxy lookup and platform client defaults for
// administrator authorization.
type OpenAIHTTPOptions struct {
	// ProxyURL returns a selected proxy URL, found flag and lookup error. Raw
	// refresh suppresses absent/error results, while provider import applies its
	// own proxy handling before exchange.
	ProxyURL func(context.Context, int64) (string, bool, error) // selected proxy URL lookup
	// ClientID selects an OAuth client default for blank raw refresh input. The
	// handler uses the returned string even when the callback reports an unknown
	// platform.
	ClientID func(string) (string, bool) // platform OAuth client selection
}

// oauthPlatformFromPath returns the OpenAI platform label for these
// authorization handlers.
func oauthPlatformFromPath(c *gin.Context) string {
	return "openai"
}

// NewOpenAIOAuthHandler borrows authorization, administration, quota and
// recovery and constructs its import and quota-action use cases. Optional quota
// and recovery remain nil; the application owns stopping QuotaActions before
// its shared dependencies are released.
func NewOpenAIOAuthHandler(auth *providercore.OpenAIAuthorization, admin OpenAIAdminOperations, quota OpenAIQuotaService, recovery OpenAIProviderStateRecoverer, options OpenAIHTTPOptions) *OpenAIOAuthHandler {
	return &OpenAIOAuthHandler{
		Authorization: auth,
		Admin:         admin,
		Quota:         quota,
		Recovery:      recovery,
		Options:       options,
		Import:        providercore.NewOpenAIProviderImport(auth, admin, options.ProxyURL),
		QuotaActions:  providercore.NewOpenAIQuotaActions(quota, recovery, admin, slog.Warn),
	}
}

// GenerateAuthURL returns an OpenAI authorization session using optional proxy
// and callback values. Every binding failure discards partial input and uses
// the default request, while authorization errors retain their common domain
// mapping.
// POST /api/v1/admin/openai/generate-auth-url
func (h *OpenAIOAuthHandler) GenerateAuthURL(c *gin.Context) {
	var req OpenAIGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// URL clients can omit JSON. Treat every binding failure as a default
		// request so partially decoded callback and proxy fields cannot select exchange settings.
		req = OpenAIGenerateAuthURLRequest{}
	}

	result, err := h.Authorization.GenerateAuthURL(
		c.Request.Context(),
		req.ProxyID,
		req.RedirectURI,
		oauthPlatformFromPath(c),
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, result)
}

// ExchangeCode binds required session, state and code before OpenAI token
// exchange. Optional callback, proxy and TLS-router selections are forwarded to
// authorization, whose failures retain their shared domain error mapping.
// POST /api/v1/admin/openai/exchange-code
func (h *OpenAIOAuthHandler) ExchangeCode(c *gin.Context) {
	var req OpenAIExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	tokenInfo, err := h.Authorization.ExchangeCode(c.Request.Context(), &providercore.OpenAIExchangeCodeInput{
		SessionID: req.SessionID,

		Code: req.Code,

		State: req.State,

		RedirectURI: req.RedirectURI,

		ProxyID: req.ProxyID,

		TLSFingerprintRouterID: req.TLSFingerprintRouterID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, tokenInfo)
}

// RefreshToken exchanges a supplied current or legacy refresh token and returns
// token information. The trimmed refresh_token field wins over rt; a blank
// result returns 400 before selecting proxy or client defaults. Explicit
// TLS-router selection is passed to authorization.
//
// A selected proxy lookup error or missing proxy is suppressed and leaves the
// empty proxy URL for exchange. Blank client_id uses the OpenAI platform
// default callback. This endpoint returns token information for later use and
// performs no provider-record lookup or credential persistence itself.
// POST /api/v1/admin/openai/refresh-token
func (h *OpenAIOAuthHandler) RefreshToken(c *gin.Context) {
	var req OpenAIRefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	// Both request spellings feed one token exchange. Prefer populated current
	// input so a simultaneously supplied compatibility alias cannot replace that credential.
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
		proxyURLValue, found, err := h.Options.ProxyURL(c.Request.Context(), *req.ProxyID)
		// Raw refresh retains its direct exchange path when proxy lookup fails
		// or finds no proxy; successful lookups alone populate the supplied proxy URL.
		if err == nil && found {
			proxyURL = proxyURLValue
		}
	}

	// Blank client input selects the OpenAI default. The platform helper returns
	// openai for these handlers, independently of which mounted path reached this operation.
	clientID := strings.TrimSpace(req.ClientID)
	if clientID == "" {
		platform := oauthPlatformFromPath(c)
		clientID, _ = h.Options.ClientID(platform)
	}

	tokenInfo, err := h.Authorization.RefreshTokenWithClientIDAndRouter(c.Request.Context(), refreshToken, proxyURL, clientID, req.TLSFingerprintRouterID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, tokenInfo)
}

// RefreshProviderToken delegates refresh for the decimal path-selected OpenAI
// provider and returns a redacted DTO. Import input errors map to 400, while
// other use-case errors retain their shared mapping.
// POST /api/v1/admin/openai/providers/:id/refresh
func (h *OpenAIOAuthHandler) RefreshProviderToken(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	value, err := h.providerImport().RefreshProvider(c.Request.Context(), providerID, oauthPlatformFromPath(c))
	if err != nil {
		writeOpenAIProviderImportError(c, err)
		return
	}
	response.Success(c, dto.ProviderFromRecord(value))
}

// CreateProviderFromOAuth strictly binds a required session, code and state and
// delegates OpenAI provider creation. The import use case owns session
// consumption and administrative persistence; success returns a redacted full
// provider DTO.
// POST /api/v1/admin/openai/create-from-oauth
func (h *OpenAIOAuthHandler) CreateProviderFromOAuth(c *gin.Context) {
	var req OpenAIOAuthProviderCreateRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	value, err := h.providerImport().CreateOAuthProvider(c.Request.Context(), providercore.OpenAIOAuthProviderCreateInput{
		SessionID: req.SessionID,

		Code: req.Code,

		State: req.State,

		RedirectURI: req.RedirectURI,

		ProxyID: req.ProxyID,

		TLSFingerprintRouterID: req.TLSFingerprintRouterID,

		Name: req.Name,

		Concurrency: req.Concurrency,

		Priority: req.Priority,

		GroupIDs: req.GroupIDs,
	}, oauthPlatformFromPath(c))
	if err != nil {
		writeOpenAIProviderImportError(c, err)
		return
	}
	response.Success(c, dto.ProviderFromRecord(value))
}

// CreateProviderFromCodexPAT strictly binds required access-token text and
// optional provider configuration. The importer validates the PAT and applies
// creation defaults before storage; input errors become 400 and successful
// records are redacted for response.
// POST /api/v1/admin/openai/create-from-codex-pat
func (h *OpenAIOAuthHandler) CreateProviderFromCodexPAT(c *gin.Context) {
	var req OpenAICodexPATCreateRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	value, err := h.providerImport().CreatePATProvider(c.Request.Context(), providercore.OpenAICodexPATCreateInput(req))
	if err != nil {
		writeOpenAIProviderImportError(c, err)
		return
	}
	response.Success(c, dto.ProviderFromRecord(value))
}

// QueryQuota returns live upstream usage for a decimal OpenAI provider
// identifier. A nil quota dependency returns 400 before querying; domain query
// failures retain their common mapping and success returns usage directly.
// GET /api/v1/admin/openai/providers/:id/quota
func (h *OpenAIOAuthHandler) QueryQuota(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}
	if h.Quota == nil {
		response.BadRequest(c, "openai quota service is not enabled")
		return
	}
	usage, err := h.Quota.QueryUsage(c.Request.Context(), providerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, usage)
}

// RefreshQuota requests live usage and reset-credit snapshot maintenance
// through quota actions. Persistence failure can still return usage with
// cache_persisted=false; an empty-result outcome error maps to 500 after the
// nil-quota guard.
// POST /api/v1/admin/openai/providers/:id/quota/refresh
func (h *OpenAIOAuthHandler) RefreshQuota(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}
	if h.Quota == nil {
		response.BadRequest(c, "openai quota service is not enabled")
		return
	}

	value, err := h.quotaActions().Refresh(c.Request.Context(), providerID)
	if err != nil {
		if missing, ok := errors.AsType[*providercore.OpenAIQuotaOutcomeError](err); ok {
			response.Error(c, http.StatusInternalServerError, missing.Message)
		} else {
			response.ErrorFrom(c, err)
		}
		return
	}
	output := OpenAIQuotaRefreshResponse{OpenAIQuotaUsage: value.OpenAIQuotaUsage, CachePersisted: value.CachePersisted}
	response.Success(c, output)
}

// CreateShadow strictly binds scheduling and group options for the
// path-selected parent provider. Administration validates parent eligibility
// and creates the credential shadow; success returns a shallow redacted view
// with its parent relationship.
// POST /api/v1/admin/providers/:id/shadow
func (h *OpenAIOAuthHandler) CreateShadow(c *gin.Context) {
	parentID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	var req CreateShadowRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	shadow, err := h.Admin.CreateShadow(c.Request.Context(), parentID, providercore.ShadowOptions{
		Name:        req.Name,
		Priority:    req.Priority,
		Concurrency: req.Concurrency,
		GroupIDs:    req.GroupIDs,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.ProviderFromRecordShallow(shadow))
}

// ResetQuota consumes an OpenAI reset credit and returns the quota-action
// outcome. A nil quota dependency returns 400, and an empty upstream result
// maps to 500. Successful consumption can be followed by partial recovery or
// cache work, described by separate flags and warning_code.
//
// The core owns a bounded post-consumption context detached from HTTP
// cancellation so confirmed credit consumption can finish maintenance. The
// response includes a redacted provider when its later read succeeds. Callers
// inspect the outcome flags rather than treating a maintenance warning as an
// unconsumed credit that can safely be retried.
// POST /api/v1/admin/openai/providers/:id/reset-quota
func (h *OpenAIOAuthHandler) ResetQuota(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}
	if h.Quota == nil {
		response.BadRequest(c, "openai quota service is not enabled")
		return
	}
	value, err := h.quotaActions().Reset(c.Request.Context(), providerID)
	if err != nil {
		if missing, ok := errors.AsType[*providercore.OpenAIQuotaOutcomeError](err); ok {
			response.Error(c, http.StatusInternalServerError, missing.Message)
		} else {
			response.ErrorFrom(c, err)
		}
		return
	}
	output := OpenAIQuotaResetResponse{
		OpenAIQuotaResetResult: value.OpenAIQuotaResetResult,
		Quota:                  value.Quota,
		Provider:               dto.ProviderFromRecord(value.Provider),
		CacheRefreshed:         value.CacheRefreshed,
		ProviderStateRecovered: value.ProviderStateRecovered,
		WarningCode:            value.WarningCode,
	}
	response.Success(c, output)
}

// providerImport returns the configured importer or constructs an uncached
// compatibility instance from current handler dependencies. Production
// construction supplies Import; partially initialized callers need
// authorization, administration and proxy callbacks for operations that use
// them.
func (h *OpenAIOAuthHandler) providerImport() *providercore.OpenAIProviderImport {
	if h.Import != nil {
		return h.Import
	}
	// Partial handler construction can leave Import empty. Build from its supplied
	// dependencies for this call, while application construction supplies the shared importer.
	return providercore.NewOpenAIProviderImport(h.Authorization, h.Admin, h.Options.ProxyURL)
}

// writeOpenAIProviderImportError maps importer input errors to their 400 safe
// message. Other errors use the shared domain mapping, retaining the importer's
// classification for administrator response handling.
func writeOpenAIProviderImportError(c *gin.Context, err error) {
	if inputError, ok := errors.AsType[*providercore.OpenAIProviderInputError](err); ok {
		response.BadRequest(c, inputError.Message)
		return
	}
	response.ErrorFrom(c, err)
}

// quotaActions returns configured quota actions or constructs an uncached
// compatibility instance from current handler dependencies. Production
// construction supplies the lifecycle-owned instance; partial handlers use the
// current quota, recovery and administration fields.
func (h *OpenAIOAuthHandler) quotaActions() *providercore.OpenAIQuotaActions {
	if h.QuotaActions != nil {
		return h.QuotaActions
	}
	// Partial handler construction can leave QuotaActions empty. Build from its
	// current dependencies for this call; application construction registers its shared instance for shutdown.
	return providercore.NewOpenAIQuotaActions(h.Quota, h.Recovery, h.Admin, slog.Warn)
}
