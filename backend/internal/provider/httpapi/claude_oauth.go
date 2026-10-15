package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// claude_oauth.go handles Claude OAuth and Setup Token authorization requests,
// including cookie exchanges.
// Claude OAuth and Setup Token creation share session exchange but choose
// different scopes when authorization starts. Cookie authentication accepts the
// frontend's code field as a session key, and URL generation treats every JSON
// binding failure as the default direct-proxy request.

// ClaudeAuthorizationUseCase supplies Claude OAuth and inference-scope session
// authorization operations.
type ClaudeAuthorizationUseCase interface {
	// GenerateAuthURL creates a full-scope authorization URL and stores its
	// session values. The caller retains the returned session ID for exchange; the
	// authorization owner handles session expiration and cleanup.
	GenerateAuthURL(context.Context, *int64) (*provider.ClaudeGenerateAuthURLResult, error)
	// GenerateSetupTokenURL creates an inference-scope URL and stores that scope
	// in the session. ExchangeCode later uses the stored scope to distinguish
	// Setup Token authorization from ordinary OAuth.
	GenerateSetupTokenURL(context.Context, *int64) (*provider.ClaudeGenerateAuthURLResult, error)
	// ExchangeCode resolves the stored session, exchanges its code and consumes it
	// after success. A resolvable requested proxy overrides the session proxy, and
	// the session supplies PKCE verifier, state and scope.
	ExchangeCode(context.Context, *provider.ClaudeExchangeCodeInput) (*provider.ClaudeTokenInfo, error)
	// CookieAuth resolves organization and authorization values from the supplied
	// browser session key. The caller selects full or inference scope in the
	// input; failures are returned before token information can be presented.
	CookieAuth(context.Context, *provider.ClaudeCookieAuthInput) (*provider.ClaudeTokenInfo, error)
}

// ClaudeOAuthHandler binds administrator requests for Claude OAuth and Setup
// Token authorization.
type ClaudeOAuthHandler struct {
	oauthService ClaudeAuthorizationUseCase // borrowed scoped authorization use case
}

// ClaudeGenerateAuthURLRequest optionally selects a proxy for the stored
// authorization session.
type ClaudeGenerateAuthURLRequest struct {
	// ProxyID supplies scoped session generation's proxy lookup. Omitted or
	// unresolved selection leaves an empty URL in the stored session.
	ProxyID *int64 `json:"proxy_id"` // optional session proxy selection
}

// ClaudeExchangeCodeRequest carries the required session and code for
// scope-aware token exchange.
type ClaudeExchangeCodeRequest struct {
	SessionID string `json:"session_id" binding:"required"` // authorization session identifier
	Code      string `json:"code" binding:"required"`       // authorization exchange code
	// ProxyID overrides the stored route when its lookup resolves. Omitted or
	// unresolved selection retains the session proxy for scope-aware exchange.
	ProxyID *int64 `json:"proxy_id"` // optional exchange proxy selection
}

// ClaudeCookieAuthRequest carries browser session-cookie text through the
// frontend's code property.
type ClaudeCookieAuthRequest struct {
	// SessionKey receives browser session-cookie text from the JSON code field
	// used by the frontend. Cookie authorization forwards that value with the
	// requested scope.
	SessionKey string `json:"code" binding:"required"` // frontend code-field cookie text
	// ProxyID supplies cookie authorization's proxy lookup. Omitted or unresolved
	// selection leaves an empty URL for organization and token requests.
	ProxyID *int64 `json:"proxy_id"` // optional cookie-auth proxy selection
}

// NewClaudeOAuthHandler borrows Claude authorization for both full and
// inference-scope routes. The same supplied use case retains the scope-bearing
// sessions, and its owner runs session cleanup and shutdown.
func NewClaudeOAuthHandler(oauthService ClaudeAuthorizationUseCase) *ClaudeOAuthHandler {
	return &ClaudeOAuthHandler{
		oauthService: oauthService,
	}
}

// GenerateAuthURL returns a full-scope Claude authorization session using the
// optional proxy. Binding failure discards every decoded request field and
// proceeds with defaults; use-case failures retain their common domain mapping.
// POST /api/v1/admin/providers/generate-auth-url
func (h *ClaudeOAuthHandler) GenerateAuthURL(c *gin.Context) {
	var req ClaudeGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Existing URL clients can omit JSON. Treat every binding failure as that
		// default request and discard partial proxy selection before generating a session.
		req = ClaudeGenerateAuthURLRequest{}
	}

	result, err := h.oauthService.GenerateAuthURL(c.Request.Context(), req.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, result)
}

// GenerateSetupTokenURL returns an inference-scope Claude authorization session
// using the optional proxy. Binding failure discards decoded request fields and
// proceeds with defaults, matching the full-scope URL endpoint's compatibility
// behavior.
// POST /api/v1/admin/providers/generate-setup-token-url
func (h *ClaudeOAuthHandler) GenerateSetupTokenURL(c *gin.Context) {
	var req ClaudeGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Existing URL clients can omit JSON. Treat every binding failure as that
		// default request and discard partial proxy selection before generating a session.
		req = ClaudeGenerateAuthURLRequest{}
	}

	result, err := h.oauthService.GenerateSetupTokenURL(c.Request.Context(), req.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, result)
}

// ExchangeCode binds a required Claude session ID and code and returns
// exchanged token information. Authorization determines scope from the stored
// session and consumes that session after successful token exchange.
// POST /api/v1/admin/providers/exchange-code
func (h *ClaudeOAuthHandler) ExchangeCode(c *gin.Context) {
	var req ClaudeExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	tokenInfo, err := h.oauthService.ExchangeCode(c.Request.Context(), &provider.ClaudeExchangeCodeInput{
		SessionID: req.SessionID,
		Code:      req.Code,
		ProxyID:   req.ProxyID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, tokenInfo)
}

// ExchangeSetupTokenCode binds the same exchange input as the full-scope
// endpoint. The authorization session supplies its scope, so this route name
// alone does not select inference scope for token exchange.
// POST /api/v1/admin/providers/exchange-setup-token-code
func (h *ClaudeOAuthHandler) ExchangeSetupTokenCode(c *gin.Context) {
	var req ClaudeExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	tokenInfo, err := h.oauthService.ExchangeCode(c.Request.Context(), &provider.ClaudeExchangeCodeInput{
		SessionID: req.SessionID,
		Code:      req.Code,
		ProxyID:   req.ProxyID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, tokenInfo)
}

// CookieAuth binds session-key text from the frontend code field and requests
// full-scope authorization. Invalid input returns 400, and the authorization
// use case owns organization lookup and token exchange.
// POST /api/v1/admin/providers/cookie-auth
func (h *ClaudeOAuthHandler) CookieAuth(c *gin.Context) {
	var req ClaudeCookieAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	tokenInfo, err := h.oauthService.CookieAuth(c.Request.Context(), &provider.ClaudeCookieAuthInput{
		SessionKey: req.SessionKey,
		ProxyID:    req.ProxyID,
		Scope:      "full",
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, tokenInfo)
}

// SetupTokenCookieAuth binds session-key text from the frontend code field and
// requests inference-scope authorization. Invalid input returns 400; use-case
// errors retain their common mapping and successful token information is
// returned.
// POST /api/v1/admin/providers/setup-token-cookie-auth
func (h *ClaudeOAuthHandler) SetupTokenCookieAuth(c *gin.Context) {
	var req ClaudeCookieAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	tokenInfo, err := h.oauthService.CookieAuth(c.Request.Context(), &provider.ClaudeCookieAuthInput{
		SessionKey: req.SessionKey,
		ProxyID:    req.ProxyID,
		Scope:      "inference",
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, tokenInfo)
}
