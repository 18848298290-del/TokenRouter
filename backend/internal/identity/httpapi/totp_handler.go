package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// totp_handler.go exposes TOTP enrollment, removal, status, and session
// step-up verification through authenticated HTTP requests.
// TOTP account management uses the authenticated Gin subject for setup,
// activation, disabling, and status reads. Sensitive-operation verification
// stores a grant under StepUpSessionKey, matching the key used by step-up
// middleware; HTTP responses convert activation times and grant durations to
// seconds for panel clients.

// TotpHandler handles current-account authenticator setup, status, and
// sensitive-operation verification.
type TotpHandler struct {
	totpService *identity.TotpService // authenticator setup and grant service
}

// TotpStatusResponse reports account activation beside the global
// authenticator feature switch.
type TotpStatusResponse struct {
	Enabled        bool   `json:"enabled"`              // account authenticator activation flag
	EnabledAt      *int64 `json:"enabled_at,omitempty"` // Unix seconds
	FeatureEnabled bool   `json:"feature_enabled"`      // global authenticator feature switch
}

// TotpSetupRequest carries optional account proof for starting authenticator
// enrollment.
type TotpSetupRequest struct {
	EmailCode string `json:"email_code"` // account email verification code
	Password  string `json:"password"`   // current account password proof
}

// TotpSetupResponse supplies the authenticator secret, enrollment URL, and
// temporary setup token.
type TotpSetupResponse struct {
	Secret     string `json:"secret"`      // authenticator secret enrollment text
	QRCodeURL  string `json:"qr_code_url"` // authenticator enrollment QR URL
	SetupToken string `json:"setup_token"` // temporary authenticator setup token
	Countdown  int    `json:"countdown"`   // seconds
}

// TotpEnableRequest supplies the temporary setup token and six-character
// authenticator code.
type TotpEnableRequest struct {
	TotpCode   string `json:"totp_code" binding:"required,len=6"` // six-character authenticator code
	SetupToken string `json:"setup_token" binding:"required"`     // temporary authenticator setup token
}

// TotpDisableRequest carries account proof for removing an enabled
// authenticator.
type TotpDisableRequest struct {
	EmailCode string `json:"email_code"` // account email verification code
	Password  string `json:"password"`   // current account password proof
}

// TotpStepUpRequest carries an authenticator code for a temporary
// sensitive-operation grant.
type TotpStepUpRequest struct {
	Code string `json:"code" binding:"required"` // submitted authenticator verification code
}

// TotpStepUpResponse reports successful verification and the remaining session
// grant lifetime.
type TotpStepUpResponse struct {
	Verified  bool  `json:"verified"`   // verification success response flag
	ExpiresIn int64 `json:"expires_in"` // seconds
}

// NewTotpHandler retains the supplied authenticator service for account
// management requests.
func NewTotpHandler(totpService *identity.TotpService) *TotpHandler {
	return &TotpHandler{
		totpService: totpService,
	}
}

// GetStatus writes activation and feature settings for the authenticated
// current account. An enabled-at timestamp is converted to Unix seconds;
// missing identity and service failures produce panel error responses.
func (h *TotpHandler) GetStatus(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	status, err := h.totpService.GetStatus(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	resp := TotpStatusResponse{
		Enabled:        status.Enabled,
		FeatureEnabled: status.FeatureEnabled,
	}

	if status.EnabledAt != nil {
		// Panel status clients consume activation timestamps as Unix seconds.
		// Convert the stored time while retaining omission when the account has no
		// activation timestamp.
		ts := status.EnabledAt.Unix()
		resp.EnabledAt = &ts
	}

	response.Success(c, resp)
}

// InitiateSetup begins authenticator enrollment for the authenticated account
// using optional email or password proof. Any JSON-binding error resets the
// request to empty proof fields before the service applies its verification
// rules.
func (h *TotpHandler) InitiateSetup(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req TotpSetupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Setup proof fields are optional at HTTP decoding. A malformed body is
		// treated like an empty request, leaving account proof requirements to the
		// authenticator service.
		req = TotpSetupRequest{}
	}

	result, err := h.totpService.InitiateSetup(c.Request.Context(), subject.UserID, req.EmailCode, req.Password)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, TotpSetupResponse{
		Secret:     result.Secret,
		QRCodeURL:  result.QRCodeURL,
		SetupToken: result.SetupToken,
		Countdown:  result.Countdown,
	})
}

// Enable completes enrollment with a required setup token and six-character
// authenticator code. The authenticated account owns the setup operation;
// malformed JSON and service errors produce panel error responses.
func (h *TotpHandler) Enable(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req TotpEnableRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if err := h.totpService.CompleteSetup(c.Request.Context(), subject.UserID, req.TotpCode, req.SetupToken); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"success": true})
}

// Disable removes the authenticated account authenticator after the service
// checks supplied email or password proof. Malformed JSON stops before that
// operation, while service failures use the shared panel response mapping.
func (h *TotpHandler) Disable(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req TotpDisableRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if err := h.totpService.Disable(c.Request.Context(), subject.UserID, req.EmailCode, req.Password); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"success": true})
}

// GetVerificationMethod reports the account proof needed for the authenticated
// user authenticator operation. Missing Gin identity writes unauthorized;
// service failures use the shared error response before any method is
// returned.
func (h *TotpHandler) GetVerificationMethod(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	method, err := h.totpService.GetVerificationMethod(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, method)
}

// SendVerifyCode requests authenticator-operation email proof for the
// authenticated account. Accept-Language is forwarded to the service for mail
// selection; missing identity and service errors use panel responses.
func (h *TotpHandler) SendVerifyCode(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	if err := h.totpService.SendVerifyCode(c.Request.Context(), subject.UserID, c.GetHeader("Accept-Language")); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"success": true})
}

// StepUp verifies a required authenticator code for the account and selected
// session key. The returned grant lifetime is converted to integer seconds;
// future sensitive operations query the same StepUpSessionKey.
func (h *TotpHandler) StepUp(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req TotpStepUpRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "TOTP code is required")
		return
	}

	sessionKey := StepUpSessionKey(c, subject.UserID)
	ttl, err := h.totpService.VerifyStepUp(c.Request.Context(), subject.UserID, sessionKey, req.Code)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// Expose whole seconds of the granted duration to panel clients; the stored
	// grant remains scoped to the selected user and session key.
	response.Success(c, TotpStepUpResponse{
		Verified:  true,
		ExpiresIn: int64(ttl.Seconds()),
	})
}
