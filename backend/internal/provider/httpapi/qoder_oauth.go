package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// qoder_oauth.go handles Qoder device authorization generation, callback
// exchange, and polling requests.
// Qoder device authorization can finish from a copied browser callback or
// through polling. Site validation runs before generating a session, while
// exchange forwards optional state and code so the authorization service can
// fill them from the callback and check consistency.

// QoderAuthorizationUseCase supplies site validation, device sessions,
// completion and polling operations.
type QoderAuthorizationUseCase interface {
	// ValidateSite parses the requested site into a supported Qoder selection.
	// Empty input uses the platform parser's default; the handler rejects
	// unsupported sites before asking authorization to create a session.
	ValidateSite(string) (string, error)
	// GenerateAuthURLForSite creates a device-authorization session for the
	// validated site and optional proxy. The caller keeps session ID and state for
	// completion or polling, while the authorization owner manages expiration.
	GenerateAuthURLForSite(context.Context, string, *int64) (*provider.QoderAuthURLResult, error)
	// ExchangeCode completes a stored device session using callback or submitted
	// values. Callback state fills blank input and mismatches are rejected; the
	// implementation coordinates completion and may reuse a successful stored
	// result. Completion keeps the session's creation-time proxy; submitted ProxyID
	// is accepted for compatibility and ignored.
	ExchangeCode(context.Context, *provider.QoderExchangeCodeInput) (*provider.QoderTokenInfo, error)
	// Poll queries completion for the supplied session and state. Pending is a
	// successful status response; completion retains the creation-time proxy and
	// ignores the compatibility proxy argument. Authorization owns concurrent
	// completion and stored-result reuse.
	Poll(context.Context, string, string, *int64) (*provider.QoderPollResult, error)
}

// QoderOAuthHandler binds administrator device-authorization requests to the
// Qoder authorization use case.
type QoderOAuthHandler struct {
	qoderOAuthService QoderAuthorizationUseCase // borrowed device authorization use case
}

// QoderGenerateAuthURLRequest selects a Qoder site and optional
// device-authorization proxy.
type QoderGenerateAuthURLRequest struct {
	// ProxyID reaches device-session preparation for route resolution. The flow
	// retains that resolved URL, and exchange and polling keep this creation-time
	// selection.
	ProxyID *int64 `json:"proxy_id"` // optional session proxy selection
	// Site is parsed before session creation, with empty input using the upstream
	// parser's default. Only supported site values reach the authorization use
	// case.
	Site string `json:"site"` // Qoder authorization site selector
}

// QoderExchangeCodeRequest carries a required session and optional callback
// completion values.
type QoderExchangeCodeRequest struct {
	SessionID string `json:"session_id" binding:"required"` // authorization session identifier
	State     string `json:"state"`                         // authorization state value
	Code      string `json:"code"`                          // authorization exchange code
	// CallbackURL can supply code and state when their submitted fields are blank.
	// A nonblank callback state must match any independently submitted state
	// during authorization normalization.
	CallbackURL string `json:"callback_url"` // copied browser callback URL
	// ProxyID is accepted as a compatibility input and ignored by production
	// completion. The stored device flow retains its creation-time proxy URL.
	ProxyID *int64 `json:"proxy_id"` // compatibility proxy input
}

// QoderPollRequest identifies a session and required state for
// device-authorization polling.
type QoderPollRequest struct {
	SessionID string `json:"session_id" binding:"required"` // authorization session identifier
	State     string `json:"state" binding:"required"`      // authorization state value
	// ProxyID is accepted as a compatibility input and ignored by production
	// polling. Completion keeps the device flow's creation-time proxy URL.
	ProxyID *int64 `json:"proxy_id"` // compatibility proxy input
}

// NewQoderOAuthHandler borrows Qoder device authorization for site selection,
// callback completion and polling. Its owner starts session cleanup and later
// stops registered authorization work before releasing upstream dependencies.
func NewQoderOAuthHandler(qoderOAuthService QoderAuthorizationUseCase) *QoderOAuthHandler {
	return &QoderOAuthHandler{qoderOAuthService: qoderOAuthService}
}

// GenerateAuthURL validates the requested Qoder site before generating a
// browser authorization session. Invalid bodies and sites return 400; use-case
// generation failures use the endpoint's 500 message.
// POST /api/v1/admin/qoder/oauth/auth-url
func (h *QoderOAuthHandler) GenerateAuthURL(c *gin.Context) {
	var req QoderGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	site, err := h.qoderOAuthService.ValidateSite(req.Site)
	if err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.qoderOAuthService.GenerateAuthURLForSite(c.Request.Context(), site, req.ProxyID)
	if err != nil {
		response.InternalError(c, "Failed to generate authorization URL: "+err.Error())
		return
	}

	response.Success(c, result)
}

// ExchangeCode completes the selected device session using submitted code,
// state or callback URL. Only session_id is required by binding; authorization
// fills callback values and maps completion failures to this endpoint's 400
// response.
// POST /api/v1/admin/qoder/oauth/exchange-code
func (h *QoderOAuthHandler) ExchangeCode(c *gin.Context) {
	var req QoderExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	tokenInfo, err := h.qoderOAuthService.ExchangeCode(c.Request.Context(), &provider.QoderExchangeCodeInput{
		SessionID:   req.SessionID,
		State:       req.State,
		Code:        req.Code,
		CallbackURL: req.CallbackURL,
		ProxyID:     req.ProxyID,
	})
	if err != nil {
		response.BadRequest(c, "Failed to exchange token: "+err.Error())
		return
	}

	response.Success(c, tokenInfo)
}

// Poll checks the required session and state through Qoder authorization and
// returns its completion result. Binding and use-case failures return 400;
// pending and successful results keep the use-case response fields.
// POST /api/v1/admin/qoder/oauth/poll
func (h *QoderOAuthHandler) Poll(c *gin.Context) {
	var req QoderPollRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	result, err := h.qoderOAuthService.Poll(c.Request.Context(), req.SessionID, req.State, req.ProxyID)
	if err != nil {
		response.BadRequest(c, "Failed to check authorization status: "+err.Error())
		return
	}

	response.Success(c, result)
}
