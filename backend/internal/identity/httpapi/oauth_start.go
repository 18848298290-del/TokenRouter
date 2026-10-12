package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

type OAuthStartCaptchaRequest struct {
	// TurnstileToken carries Aliyun captchaVerifyParam using the shared request field.
	TurnstileToken        string `json:"turnstile_token"`
	TencentCaptchaTicket  string `json:"tencent_captcha_ticket"`
	TencentCaptchaRandstr string `json:"tencent_captcha_randstr"`
}

type OAuthStartResponse struct {
	AuthorizeURL string `json:"authorize_url"`
}

func (h *SessionHandler) RequireActionCaptchaForOAuthLoginStart(c *gin.Context) bool {
	// Binding routes authenticate the current user separately and do not consume anonymous-login CAPTCHA proofs.
	if strings.HasSuffix(strings.TrimRight(c.Request.URL.Path, "/"), "/bind/start") {
		return true
	}

	var req OAuthStartCaptchaRequest
	if c.Request.Method == http.MethodPost {
		_ = c.ShouldBindJSON(&req)
	}
	if err := h.authService.VerifyActionCaptchaIfEnabled(c.Request.Context(), identity.CaptchaProof{
		TurnstileToken: req.TurnstileToken,
		TencentTicket:  req.TencentCaptchaTicket,
		TencentRandstr: req.TencentCaptchaRandstr,
	}, clientip.GetClientIP(c)); err != nil {
		response.ErrorFrom(c, err)
		return false
	}
	return true
}

func RespondOAuthStart(c *gin.Context, authorizeURL string) {
	if c.Request.Method == http.MethodPost {
		response.Success(c, OAuthStartResponse{AuthorizeURL: authorizeURL})
		return
	}
	c.Redirect(http.StatusFound, authorizeURL)
}
