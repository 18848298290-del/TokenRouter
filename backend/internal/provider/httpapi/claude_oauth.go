package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

type ClaudeAuthorizationUseCase interface {
	GenerateAuthURL(context.Context, *int64) (*provider.ClaudeGenerateAuthURLResult, error)
	GenerateSetupTokenURL(context.Context, *int64) (*provider.ClaudeGenerateAuthURLResult, error)
	ExchangeCode(context.Context, *provider.ClaudeExchangeCodeInput) (*provider.ClaudeTokenInfo, error)
	CookieAuth(context.Context, *provider.ClaudeCookieAuthInput) (*provider.ClaudeTokenInfo, error)
}

// ClaudeOAuthHandler handles OAuth-related operations for providers.
type ClaudeOAuthHandler struct {
	oauthService ClaudeAuthorizationUseCase
}

// ClaudeGenerateAuthURLRequest represents the request for generating auth URL.
type ClaudeGenerateAuthURLRequest struct {
	ProxyID *int64 `json:"proxy_id"`
}

// ClaudeExchangeCodeRequest represents the request for exchanging auth code.
type ClaudeExchangeCodeRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	Code      string `json:"code" binding:"required"`
	ProxyID   *int64 `json:"proxy_id"`
}

// ClaudeCookieAuthRequest represents the request for cookie-based authentication.
type ClaudeCookieAuthRequest struct {
	SessionKey string `json:"code" binding:"required"` // The frontend sends the session key in the "code" field.
	ProxyID    *int64 `json:"proxy_id"`
}

// NewClaudeOAuthHandler creates a new OAuth handler.
func NewClaudeOAuthHandler(oauthService ClaudeAuthorizationUseCase) *ClaudeOAuthHandler {
	return &ClaudeOAuthHandler{
		oauthService: oauthService,
	}
}

// GenerateAuthURL generates OAuth authorization URL with full scope
// POST /api/v1/admin/providers/generate-auth-url
func (h *ClaudeOAuthHandler) GenerateAuthURL(c *gin.Context) {
	var req ClaudeGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Binding failures use the default request, including an empty body.
		req = ClaudeGenerateAuthURLRequest{}
	}

	result, err := h.oauthService.GenerateAuthURL(c.Request.Context(), req.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, result)
}

// GenerateSetupTokenURL generates OAuth authorization URL for setup token (inference only)
// POST /api/v1/admin/providers/generate-setup-token-url
func (h *ClaudeOAuthHandler) GenerateSetupTokenURL(c *gin.Context) {
	var req ClaudeGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Binding failures use the default request, including an empty body.
		req = ClaudeGenerateAuthURLRequest{}
	}

	result, err := h.oauthService.GenerateSetupTokenURL(c.Request.Context(), req.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, result)
}

// ExchangeCode exchanges authorization code for tokens
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

// ExchangeSetupTokenCode exchanges authorization code for setup token
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

// CookieAuth performs OAuth using sessionKey (cookie-based auto-auth)
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

// SetupTokenCookieAuth performs OAuth using sessionKey for setup token (inference only)
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
