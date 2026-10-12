package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	ip "github.com/TokenFlux/TokenRouter/internal/server/clientip"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

type (
	BackendSettings interface{ IsBackendModeEnabled(context.Context) bool }
	PasskeyHandler  struct {
		passkeys    *identity.PasskeyService
		authService *identity.AuthService
		settingSvc  BackendSettings
	}
)

type PasskeyOptionsResponse struct {
	SessionToken string `json:"session_token"`
	Options      any    `json:"options"`
}

type PasskeyFinishRequest struct {
	SessionToken string          `json:"session_token" binding:"required"`
	Name         string          `json:"name,omitempty"`
	Credential   json.RawMessage `json:"credential" binding:"required"`
}

type PasskeyBeginLoginRequest struct {
	// TurnstileToken carries Aliyun captchaVerifyParam using the shared request field.
	TurnstileToken        string `json:"turnstile_token"`
	TencentCaptchaTicket  string `json:"tencent_captcha_ticket"`
	TencentCaptchaRandstr string `json:"tencent_captcha_randstr"`
}

type PasskeyRenameRequest struct {
	Name string `json:"name" binding:"required"`
}

func NewPasskeyHandler(
	passkeys *identity.PasskeyService,
	authService *identity.AuthService,
	settingService BackendSettings,
) *PasskeyHandler {
	return &PasskeyHandler{
		passkeys:    passkeys,
		authService: authService,
		settingSvc:  settingService,
	}
}

// BeginLogin starts a username-free login ceremony with discoverable credentials.
func (h *PasskeyHandler) BeginLogin(c *gin.Context) {
	var req PasskeyBeginLoginRequest
	_ = c.ShouldBindJSON(&req)
	if err := h.authService.VerifyActionCaptchaIfEnabled(c.Request.Context(), identity.CaptchaProof{
		TurnstileToken: req.TurnstileToken,
		TencentTicket:  req.TencentCaptchaTicket,
		TencentRandstr: req.TencentCaptchaRandstr,
	}, ip.GetClientIP(c)); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	assertion, token, err := h.passkeys.BeginLogin(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, PasskeyOptionsResponse{SessionToken: token, Options: assertion})
}

// FinishLogin verifies a Passkey assertion and creates a TokenRouter session.
// The WebAuthn ceremony requires user verification, so a successful assertion
// completes login without a separate TOTP challenge.
func (h *PasskeyHandler) FinishLogin(c *gin.Context) {
	req, ok := BindPasskeyFinishRequest(c)
	if !ok {
		return
	}
	credentialRequest := bytes.NewReader(req.Credential)
	user, err := h.passkeys.FinishLogin(c.Request.Context(), req.SessionToken, credentialRequest)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if err = h.EnsureBackendModeAllowsUser(c.Request.Context(), user); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.authService.RecordSuccessfulLogin(c.Request.Context(), user.ID)
	RespondWithTokenPair(c, h.authService, user)
}

func (h *PasskeyHandler) BeginRegistration(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	creation, token, err := h.passkeys.BeginRegistration(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, PasskeyOptionsResponse{SessionToken: token, Options: creation})
}

func (h *PasskeyHandler) FinishRegistration(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	req, valid := BindPasskeyFinishRequest(c)
	if !valid {
		return
	}
	credentialRequest := bytes.NewReader(req.Credential)
	credential, err := h.passkeys.FinishRegistration(
		c.Request.Context(),
		subject.UserID,
		req.SessionToken,
		req.Name,
		credentialRequest,
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, credential)
}

func (h *PasskeyHandler) List(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	credentials, err := h.passkeys.List(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, credentials)
}

func (h *PasskeyHandler) Rename(c *gin.Context) {
	subject, credentialID, ok := PasskeyMutationTarget(c)
	if !ok {
		return
	}
	var req PasskeyRenameRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		response.BadRequest(c, "Passkey name is required")
		return
	}
	if err := h.passkeys.Rename(c.Request.Context(), subject.UserID, credentialID, req.Name); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"success": true})
}

func (h *PasskeyHandler) Delete(c *gin.Context) {
	subject, credentialID, ok := PasskeyMutationTarget(c)
	if !ok {
		return
	}
	if err := h.passkeys.Delete(c.Request.Context(), subject.UserID, credentialID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"success": true})
}

func (h *PasskeyHandler) EnsureBackendModeAllowsUser(ctx context.Context, user *identity.User) error {
	if err := EnsureLoginUserActive(user); err != nil {
		return err
	}
	if h.settingSvc == nil || !h.settingSvc.IsBackendModeEnabled(ctx) || user.IsAdmin() {
		return nil
	}
	return infraerrors.Forbidden("BACKEND_MODE_ADMIN_ONLY", "Backend mode is active. Only admin login is allowed.")
}

func BindPasskeyFinishRequest(c *gin.Context) (*PasskeyFinishRequest, bool) {
	var req PasskeyFinishRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Credential) == 0 {
		response.BadRequest(c, "Invalid passkey response")
		return nil, false
	}
	return &req, true
}

func PasskeyMutationTarget(c *gin.Context) (authctx.AuthSubject, int64, bool) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return authctx.AuthSubject{}, 0, false
	}
	credentialID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || credentialID <= 0 {
		response.BadRequest(c, "Invalid passkey ID")
		return authctx.AuthSubject{}, 0, false
	}
	return subject, credentialID, true
}
