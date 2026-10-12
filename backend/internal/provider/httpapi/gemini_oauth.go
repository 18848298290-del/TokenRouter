package httpapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

type GeminiAuthorizationUseCase interface {
	GetOAuthConfig() *provider.GeminiOAuthCapabilities
	GenerateAuthURL(context.Context, *int64, string, string, string, string) (*provider.GeminiAuthURLResult, error)
	ExchangeCode(context.Context, *provider.GeminiExchangeCodeInput) (*provider.GeminiTokenInfo, error)
}
type GeminiOAuthHandler struct {
	geminiOAuthService GeminiAuthorizationUseCase
}

type GeminiGenerateAuthURLRequest struct {
	ProxyID   *int64 `json:"proxy_id"`
	ProjectID string `json:"project_id"`
	// OAuthType accepts "code_assist", "google_one", or "ai_studio".
	// An omitted value defaults to "code_assist" for older clients.
	OAuthType string `json:"oauth_type"`
	// TierID is a user-selected tier to be used when auto detection is unavailable or fails.
	TierID string `json:"tier_id"`
}

type GeminiExchangeCodeRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	State     string `json:"state" binding:"required"`
	Code      string `json:"code" binding:"required"`
	ProxyID   *int64 `json:"proxy_id"`
	// OAuthType must match the type used to generate the authorization URL.
	OAuthType string `json:"oauth_type"`
	// TierID is a user-selected tier to be used when auto detection is unavailable or fails.
	// This field is optional; when omitted, the server uses the tier stored in the OAuth session.
	TierID string `json:"tier_id"`
}

func NewGeminiOAuthHandler(geminiOAuthService GeminiAuthorizationUseCase) *GeminiOAuthHandler {
	return &GeminiOAuthHandler{geminiOAuthService: geminiOAuthService}
}

// GetCapabilities returns the Gemini OAuth configuration capabilities.
// GET /api/v1/admin/gemini/oauth/capabilities
func (h *GeminiOAuthHandler) GetCapabilities(c *gin.Context) {
	cfg := h.geminiOAuthService.GetOAuthConfig()
	response.Success(c, cfg)
}

// GenerateAuthURL generates Google OAuth authorization URL for Gemini.
// POST /api/v1/admin/gemini/oauth/auth-url
func (h *GeminiOAuthHandler) GenerateAuthURL(c *gin.Context) {
	var req GeminiGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	// Default to code_assist for clients that omit oauth_type.
	oauthType := strings.TrimSpace(req.OAuthType)
	if oauthType == "" {
		oauthType = "code_assist"
	}
	if oauthType != "code_assist" && oauthType != "google_one" && oauthType != "ai_studio" {
		response.BadRequest(c, "Invalid oauth_type: must be 'code_assist', 'google_one', or 'ai_studio'")
		return
	}

	// Always pass the "hosted" callback URI; the OAuth service may override it depending on
	// oauth_type and whether the built-in Gemini CLI OAuth client is used.
	redirectURI := deriveGeminiRedirectURI(c)
	result, err := h.geminiOAuthService.GenerateAuthURL(c.Request.Context(), req.ProxyID, redirectURI, req.ProjectID, oauthType, req.TierID)
	if err != nil {
		msg := err.Error()
		// Treat missing/invalid OAuth client configuration as a user/config error.
		if strings.Contains(msg, "OAuth client not configured") ||
			strings.Contains(msg, "requires your own OAuth Client") ||
			strings.Contains(msg, "requires a custom OAuth Client") ||
			strings.Contains(msg, "GEMINI_CLI_OAUTH_CLIENT_SECRET_MISSING") ||
			strings.Contains(msg, "built-in Gemini CLI OAuth client_secret is not configured") {
			response.BadRequest(c, "Failed to generate auth URL: "+msg)
			return
		}
		response.InternalError(c, "Failed to generate auth URL: "+msg)
		return
	}

	response.Success(c, result)
}

// ExchangeCode exchanges authorization code for tokens.
// POST /api/v1/admin/gemini/oauth/exchange-code
func (h *GeminiOAuthHandler) ExchangeCode(c *gin.Context) {
	var req GeminiExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	// Default to code_assist for clients that omit oauth_type.
	oauthType := strings.TrimSpace(req.OAuthType)
	if oauthType == "" {
		oauthType = "code_assist"
	}
	if oauthType != "code_assist" && oauthType != "google_one" && oauthType != "ai_studio" {
		response.BadRequest(c, "Invalid oauth_type: must be 'code_assist', 'google_one', or 'ai_studio'")
		return
	}

	tokenInfo, err := h.geminiOAuthService.ExchangeCode(c.Request.Context(), &provider.GeminiExchangeCodeInput{
		SessionID: req.SessionID,
		State:     req.State,
		Code:      req.Code,
		ProxyID:   req.ProxyID,
		OAuthType: oauthType,
		TierID:    req.TierID,
	})
	if err != nil {
		response.BadRequest(c, "Failed to exchange code: "+err.Error())
		return
	}

	response.Success(c, tokenInfo)
}

func deriveGeminiRedirectURI(c *gin.Context) string {
	origin := strings.TrimSpace(c.GetHeader("Origin"))
	if origin != "" {
		return strings.TrimRight(origin, "/") + "/auth/callback"
	}

	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if xfProto := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")); xfProto != "" {
		scheme = strings.TrimSpace(strings.Split(xfProto, ",")[0])
	}

	host := strings.TrimSpace(c.Request.Host)
	if xfHost := strings.TrimSpace(c.GetHeader("X-Forwarded-Host")); xfHost != "" {
		host = strings.TrimSpace(strings.Split(xfHost, ",")[0])
	}

	return fmt.Sprintf("%s://%s/auth/callback", scheme, host)
}
