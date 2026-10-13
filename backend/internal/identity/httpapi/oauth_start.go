package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Anonymous OAuth start requests share CAPTCHA fields with password and
// Passkey actions. POST responses return an authorize URL for browser code to
// navigate, while accepted GET starts redirect; current-user bind starts use
// their authenticated binding flow and skip the anonymous CAPTCHA proof.

// OAuthStartCaptchaRequest carries the shared action-CAPTCHA proof for
// anonymous OAuth starts.
type OAuthStartCaptchaRequest struct {
	// TurnstileToken carries Aliyun captchaVerifyParam in the shared JSON field.
	// OAuth start action checks use Tencent or Aliyun when those providers are
	// enabled.
	TurnstileToken        string `json:"turnstile_token"`
	TencentCaptchaTicket  string `json:"tencent_captcha_ticket"`  // Tencent action ticket
	TencentCaptchaRandstr string `json:"tencent_captcha_randstr"` // Tencent ticket random string
}

// OAuthStartResponse supplies the external authorize URL after a successful
// POST start.
type OAuthStartResponse struct {
	AuthorizeURL string `json:"authorize_url"` // provider authorize URL
}

// RequireActionCaptchaForOAuthLoginStart validates enabled action CAPTCHA for
// an anonymous OAuth start and reports success. Bind-start paths bypass the
// check; POST bodies supply proof fields, and CAPTCHA errors write the panel
// error response.
func (h *SessionHandler) RequireActionCaptchaForOAuthLoginStart(c *gin.Context) bool {
	// A current-user bind start obtains identity from its binding flow. Skipping
	// anonymous action CAPTCHA here keeps that separate flow independent of a
	// login proof.
	if strings.HasSuffix(strings.TrimRight(c.Request.URL.Path, "/"), "/bind/start") {
		return true
	}

	var req OAuthStartCaptchaRequest
	if c.Request.Method == http.MethodPost {
		// Malformed or absent POST JSON leaves any decoded proof fields for the
		// CAPTCHA verifier, whose enabled-provider check decides whether the request
		// can start.
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

// RespondOAuthStart writes the authorize URL in a panel success response for
// POST requests. Other request methods receive an HTTP 302 redirect to the
// supplied URL, matching the browser navigation start flow.
func RespondOAuthStart(c *gin.Context, authorizeURL string) {
	if c.Request.Method == http.MethodPost {
		response.Success(c, OAuthStartResponse{AuthorizeURL: authorizeURL})
		return
	}
	c.Redirect(http.StatusFound, authorizeURL)
}
