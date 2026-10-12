package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

type AntigravityOAuthHandler struct {
	antigravityOAuthService AntigravityAuthorizationUseCase
}

type AntigravityGenerateAuthURLRequest struct {
	ProxyID *int64 `json:"proxy_id"`
}

type AntigravityExchangeCodeRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	State     string `json:"state" binding:"required"`
	Code      string `json:"code" binding:"required"`
	ProxyID   *int64 `json:"proxy_id"`
}

// AntigravityRefreshTokenRequest represents the request for validating Antigravity refresh token.
type AntigravityRefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
	ProxyID      *int64 `json:"proxy_id"`
}

// AntigravityAuthorizationUseCase provides authorization URL, code exchange, and refresh-token validation operations.
type AntigravityAuthorizationUseCase interface {
	GenerateAuthURL(context.Context, *int64) (*provider.AntigravityAuthURLResult, error)
	ExchangeCode(context.Context, *provider.AntigravityExchangeCodeInput) (*provider.AntigravityTokenInfo, error)
	ValidateRefreshToken(context.Context, string, *int64) (*provider.AntigravityTokenInfo, error)
}

func NewAntigravityOAuthHandler(antigravityOAuthService AntigravityAuthorizationUseCase) *AntigravityOAuthHandler {
	return &AntigravityOAuthHandler{antigravityOAuthService: antigravityOAuthService}
}

// GenerateAuthURL generates Google OAuth authorization URL
// POST /api/v1/admin/antigravity/oauth/auth-url
func (h *AntigravityOAuthHandler) GenerateAuthURL(c *gin.Context) {
	var req AntigravityGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	result, err := h.antigravityOAuthService.GenerateAuthURL(c.Request.Context(), req.ProxyID)
	if err != nil {
		response.InternalError(c, "生成授权链接失败: "+err.Error())
		return
	}

	response.Success(c, result)
}

// ExchangeCode exchanges an authorization code for tokens.
// POST /api/v1/admin/antigravity/oauth/exchange-code
func (h *AntigravityOAuthHandler) ExchangeCode(c *gin.Context) {
	var req AntigravityExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	tokenInfo, err := h.antigravityOAuthService.ExchangeCode(c.Request.Context(), &provider.AntigravityExchangeCodeInput{
		SessionID: req.SessionID,
		State:     req.State,
		Code:      req.Code,
		ProxyID:   req.ProxyID,
	})
	if err != nil {
		response.BadRequest(c, "Token 交换失败: "+err.Error())
		return
	}

	response.Success(c, tokenInfo)
}

// RefreshToken validates an Antigravity refresh token and returns full token info
// POST /api/v1/admin/antigravity/oauth/refresh-token
func (h *AntigravityOAuthHandler) RefreshToken(c *gin.Context) {
	var req AntigravityRefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	tokenInfo, err := h.antigravityOAuthService.ValidateRefreshToken(c.Request.Context(), req.RefreshToken, req.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, tokenInfo)
}
