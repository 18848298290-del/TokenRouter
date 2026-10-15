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

// passkey_handler.go exposes Passkey login, registration, and credential
// management through HTTP handlers.
// Passkey HTTP requests keep short-lived ceremony tokens separate from
// persisted credentials. Anonymous login begin verifies enabled action CAPTCHA
// before asking the Passkey service for discovery options, while registration
// and credential management identify the current account through authenticated
// Gin values.
//
// Finish requests pass the credential JSON bytes to the service with the
// matching session token. Login then applies active-account and backend-mode
// checks before recording success and issuing panel credentials. The service
// owns WebAuthn validation and ceremony consumption; these handlers preserve
// its errors, and current-account mutation routes also pass the owning user ID
// to credential operations.

type (
	// BackendSettings supplies the runtime mode checked after Passkey login
	// validation.
	BackendSettings interface {
		// IsBackendModeEnabled reports whether login is restricted to administrator accounts.
		IsBackendModeEnabled(context.Context) bool
	}
	// PasskeyHandler connects ceremony operations to panel authentication and
	// account checks.
	PasskeyHandler struct {
		passkeys    *identity.PasskeyService // Passkey ceremony and credential service
		authService *identity.AuthService    // action proof and panel credentials
		settingSvc  BackendSettings          // post-login backend-mode settings
	}
)

// PasskeyOptionsResponse pairs browser ceremony options with the matching
// temporary session token.
type PasskeyOptionsResponse struct {
	SessionToken string `json:"session_token"` // temporary ceremony session token
	Options      any    `json:"options"`       // browser WebAuthn ceremony options
}

// PasskeyFinishRequest supplies a ceremony token, credential JSON, and
// optional registration name.
type PasskeyFinishRequest struct {
	SessionToken string `json:"session_token" binding:"required"` // temporary ceremony session token
	// Name supplies stored credential naming during FinishRegistration.
	// FinishLogin uses the session token and credential response from this same
	// request shape for assertion validation.
	Name       string          `json:"name,omitempty"`                // credential display name
	Credential json.RawMessage `json:"credential" binding:"required"` // WebAuthn credential response JSON
}

// PasskeyBeginLoginRequest carries enabled action-CAPTCHA proof before
// anonymous ceremony creation.
type PasskeyBeginLoginRequest struct {
	TurnstileToken        string `json:"turnstile_token"`         // Aliyun captchaVerifyParam proof text
	TencentCaptchaTicket  string `json:"tencent_captcha_ticket"`  // Tencent action ticket
	TencentCaptchaRandstr string `json:"tencent_captcha_randstr"` // Tencent ticket random string
}

// PasskeyRenameRequest supplies the display name assigned to one owned
// credential.
type PasskeyRenameRequest struct {
	Name string `json:"name" binding:"required"` // credential display name
}

// NewPasskeyHandler retains ceremony and authentication services plus optional
// backend-mode settings. Initialize both services before exposing the routes;
// nil settings permit active users after successful Passkey login.
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

// BeginLogin verifies enabled action CAPTCHA before returning discovery
// ceremony options and a session token. JSON-binding errors are ignored so
// missing or partially decoded proof reaches the verifier; service errors
// write panel errors.
func (h *PasskeyHandler) BeginLogin(c *gin.Context) {
	var req PasskeyBeginLoginRequest
	// An empty or malformed body can still leave decoded proof fields. The
	// enabled action verifier decides admission before any Passkey ceremony is
	// created.
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

// FinishLogin validates the ceremony token and credential JSON through the
// Passkey service, then checks active-account and backend-mode admission. The
// configured WebAuthn verifier requires user verification, so this route
// completes successful ceremony login without a separate TOTP challenge.
// Success records login and issues panel credentials; the service consumes the
// ceremony before later admission failures.
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

// BeginRegistration starts credential enrollment for the account resolved by
// authenticated Gin context. Missing identity writes an unauthorized response;
// successful service results return browser options with their temporary
// session token.
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

// FinishRegistration passes the authenticated owner, ceremony token, optional
// name, and credential JSON to enrollment. Invalid requests stop before the
// service; a successful result writes the stored credential record.
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

// List returns the credentials belonging to the authenticated Gin account.
// Missing identity writes an unauthorized response, and credential service
// failures use the shared panel error mapping.
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

// Rename updates one credential belonging to the authenticated account using a
// positive route ID. Invalid IDs, malformed JSON, or whitespace-only names are
// rejected before the service receives the supplied name.
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

// Delete removes one credential belonging to the authenticated account using a
// positive route ID. Missing identity or invalid IDs stop before service
// execution; service errors use the panel response mapping.
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

// EnsureBackendModeAllowsUser rejects missing or inactive accounts before
// applying the backend-mode role rule. Nil settings permit an active account;
// enabled mode requires the supplied current user to be an administrator.
func (h *PasskeyHandler) EnsureBackendModeAllowsUser(ctx context.Context, user *identity.User) error {
	if err := EnsureLoginUserActive(user); err != nil {
		return err
	}
	if h.settingSvc == nil || !h.settingSvc.IsBackendModeEnabled(ctx) || user.IsAdmin() {
		return nil
	}
	return infraerrors.Forbidden("BACKEND_MODE_ADMIN_ONLY", "Backend mode is active. Only admin login is allowed.")
}

// BindPasskeyFinishRequest decodes the required ceremony token and credential
// JSON from the request. False means a bad-request response was written;
// callers stop before validating the ceremony when decoding fails.
func BindPasskeyFinishRequest(c *gin.Context) (*PasskeyFinishRequest, bool) {
	var req PasskeyFinishRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Credential) == 0 {
		response.BadRequest(c, "Invalid passkey response")
		return nil, false
	}
	return &req, true
}

// PasskeyMutationTarget returns the authenticated owner and positive
// credential route ID for mutations. False means an unauthorized or
// bad-request response was written; callers stop before touching stored
// credentials.
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
