package httpapi

import (
	"context"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Panel password login can produce a temporary TOTP challenge before the final
// access and refresh credentials. The shared session handler applies CAPTCHA,
// account, and backend-mode checks at the HTTP stages that own them, while
// authentication services own account creation and credential lifecycle
// writes.
//
// Second-factor completion can also finish a pending provider binding. It
// reloads the account before binding and challenge deletion, and pending
// binding can persist before a later user reload or token response fails.
// Challenge deletion and logout revocation errors are tolerated at their
// respective cleanup stages. Refresh rotation runs before the HTTP
// backend-mode role check, so a refused refresh response can follow completed
// credential rotation.

// SessionHTTPSettings supplies authentication switches and the frontend URL
// for panel handlers.
type SessionHTTPSettings interface {
	// IsBackendModeEnabled reports whether panel login is restricted to
	// administrator accounts.
	IsBackendModeEnabled(context.Context) bool
	// IsTotpEnabled reports whether password login can request the account
	// authenticator challenge.
	IsTotpEnabled(context.Context) bool
	// IsPromoCodeEnabled reports whether signup promotion validation is enabled
	// for panel clients.
	IsPromoCodeEnabled(context.Context) bool
	// IsInvitationCodeEnabled reports whether registration invitation validation
	// is enabled for panel clients.
	IsInvitationCodeEnabled(context.Context) bool
	// GetFrontendURL returns the configured frontend URL used to build password
	// reset links.
	GetFrontendURL(context.Context) string
}

// InvitationReader loads a submitted registration invitation for HTTP
// availability checks.
type InvitationReader interface {
	// GetByCode returns the submitted invitation record or its lookup error. HTTP
	// validation converts lookup failures into an invalid-code response before
	// inspecting record type and unused status.
	GetByCode(context.Context, string) (*identity.RedeemCode, error)
}

// PromotionPreview reports signup promotion validity, offered account amount,
// and rejection code.
type PromotionPreview struct {
	Valid       bool    // promotion availability result flag
	BonusAmount float64 // USD
	ErrorCode   string  // preview rejection code label
}

// SessionHTTPOptions supplies HTTP audit, backend policy, promotion preview,
// and browser cleanup callbacks.
type SessionHTTPOptions struct {
	// BackendMode, AuditActor, ClearPendingCookies, and LogoutPending are
	// optional callbacks; a nil callback skips its effect. PreviewPromotion is
	// required on the enabled promotion-validation path and supplies that
	// response data.
	BackendMode         func(context.Context) bool                     // optional backend-mode decision callback
	AuditActor          func(*gin.Context, int64, string)              // optional audit actor recorder
	ClearPendingCookies func(*gin.Context)                             // optional successful binding cookie cleanup
	LogoutPending       func(*gin.Context)                             // optional logout pending-state cleanup
	PreviewPromotion    func(context.Context, string) PromotionPreview // signup promotion preview callback
}

// SessionHandler handles account registration, password login, second-factor
// completion, and refresh sessions.
type SessionHandler struct {
	authService *identity.AuthService // account and credential authentication service
	userService *identity.UserService // current account and profile reader
	// settingSvc supplies challenge switches, password-reset URL, and refresh
	// backend checks. Those paths need an initialized reader, while invitation
	// and promotion validation define their separate nil-reader behavior.
	settingSvc    SessionHTTPSettings // operation runtime settings reader
	redeemService InvitationReader    // registration invitation record reader
	// totpService can be absent for password login that creates no authenticator
	// challenge. Login2FA requires the configured service before that completion
	// route is exposed.
	totpService *identity.TotpService // optional authenticator login service
	pending     *identity.PendingFlow // second-factor provider binding flow
	options     SessionHTTPOptions    // retained HTTP operation callbacks
}

// RegisterRequest carries registration credentials, verification proof, and
// optional signup codes.
type RegisterRequest struct {
	Email                 string `json:"email" binding:"required,email"`    // account email address input
	Password              string `json:"password" binding:"required,min=6"` // account plaintext password input
	VerifyCode            string `json:"verify_code"`                       // registration email verification code
	TurnstileToken        string `json:"turnstile_token"`                   // shared enabled CAPTCHA proof
	TencentCaptchaTicket  string `json:"tencent_captcha_ticket"`            // Tencent action ticket text
	TencentCaptchaRandstr string `json:"tencent_captcha_randstr"`           // Tencent ticket random string
	PromoCode             string `json:"promo_code"`                        // optional signup promotion code
	InvitationCode        string `json:"invitation_code"`                   // optional registration invitation code
	AffCode               string `json:"aff_code"`                          // optional signup affiliate code
}

// SendVerifyCodeRequest carries the registration address and enabled CAPTCHA
// provider proof.
type SendVerifyCodeRequest struct {
	Email                 string `json:"email" binding:"required,email"` // account email address input
	TurnstileToken        string `json:"turnstile_token"`                // shared enabled CAPTCHA proof
	TencentCaptchaTicket  string `json:"tencent_captcha_ticket"`         // Tencent action ticket text
	TencentCaptchaRandstr string `json:"tencent_captcha_randstr"`        // Tencent ticket random string
}

// SendVerifyCodeResponse reports accepted verification mail and the client
// retry delay.
type SendVerifyCodeResponse struct {
	Message   string `json:"message"`   // panel operation status text
	Countdown int    `json:"countdown"` // seconds
}

// LoginRequest carries password credentials and enabled CAPTCHA provider proof
// for panel login.
type LoginRequest struct {
	Email                 string `json:"email" binding:"required,email"` // account email address input
	Password              string `json:"password" binding:"required"`    // account plaintext password input
	TurnstileToken        string `json:"turnstile_token"`                // shared enabled CAPTCHA proof
	TencentCaptchaTicket  string `json:"tencent_captcha_ticket"`         // Tencent action ticket text
	TencentCaptchaRandstr string `json:"tencent_captcha_randstr"`        // Tencent ticket random string
}

// TotpLoginResponse asks the browser to complete a temporary authenticator
// login challenge.
type TotpLoginResponse struct {
	Requires2FA     bool   `json:"requires_2fa"`                // temporary authenticator challenge required
	TempToken       string `json:"temp_token,omitempty"`        // temporary authenticator login token
	UserEmailMasked string `json:"user_email_masked,omitempty"` // masked challenge account email
}

// Login2FARequest supplies the temporary login challenge and six-character
// authenticator proof.
type Login2FARequest struct {
	TempToken string `json:"temp_token" binding:"required"`      // temporary authenticator login token
	TotpCode  string `json:"totp_code" binding:"required,len=6"` // six-character authenticator verification code
}

// ValidatePromoCodeRequest supplies the signup promotion code whose
// availability is requested.
type ValidatePromoCodeRequest struct {
	Code string `json:"code" binding:"required"` // submitted signup validation code
}

// ValidatePromoCodeResponse reports signup promotion availability and its
// offered account amount.
type ValidatePromoCodeResponse struct {
	Valid       bool    `json:"valid"`                  // signup code validity flag
	BonusAmount float64 `json:"bonus_amount,omitempty"` // USD
	ErrorCode   string  `json:"error_code,omitempty"`   // signup validation rejection code
	Message     string  `json:"message,omitempty"`      // panel operation status text
}

// ValidateInvitationCodeRequest supplies the registration invitation whose
// availability is requested.
type ValidateInvitationCodeRequest struct {
	Code string `json:"code" binding:"required"` // submitted signup validation code
}

// ValidateInvitationCodeResponse reports submitted invitation validity and a
// possible rejection code.
type ValidateInvitationCodeResponse struct {
	Valid     bool   `json:"valid"`                // signup code validity flag
	ErrorCode string `json:"error_code,omitempty"` // signup validation rejection code
}

// ForgotPasswordRequest supplies the account address and enabled CAPTCHA proof
// for reset mail.
type ForgotPasswordRequest struct {
	Email                 string `json:"email" binding:"required,email"` // account email address input
	TurnstileToken        string `json:"turnstile_token"`                // shared enabled CAPTCHA proof
	TencentCaptchaTicket  string `json:"tencent_captcha_ticket"`         // Tencent action ticket text
	TencentCaptchaRandstr string `json:"tencent_captcha_randstr"`        // Tencent ticket random string
}

// ForgotPasswordResponse reports acceptance of an account password-reset mail
// request.
type ForgotPasswordResponse struct {
	Message string `json:"message"` // panel operation status text
}

// ResetPasswordRequest carries the email reset credential and replacement
// account password.
type ResetPasswordRequest struct {
	Email       string `json:"email" binding:"required,email"`        // account email address input
	Token       string `json:"token" binding:"required"`              // email password-reset credential
	NewPassword string `json:"new_password" binding:"required,min=6"` // replacement account plaintext password
}

// ResetPasswordResponse reports completion of the submitted account
// password-reset operation.
type ResetPasswordResponse struct {
	Message string `json:"message"` // panel operation status text
}

// RefreshTokenRequest supplies the refresh credential submitted for rotating
// panel tokens.
type RefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"` // rotating refresh credential text
}

// RefreshTokenResponse carries the issued replacement access and rotating
// refresh credentials.
type RefreshTokenResponse struct {
	AccessToken  string `json:"access_token"`  // issued signed access credential
	RefreshToken string `json:"refresh_token"` // rotating refresh credential text
	ExpiresIn    int    `json:"expires_in"`    // seconds
	TokenType    string `json:"token_type"`    // Bearer authorization scheme
}

// LogoutRequest optionally supplies a rotating refresh credential for browser
// logout cleanup.
type LogoutRequest struct {
	RefreshToken string `json:"refresh_token,omitempty"` // rotating refresh credential text
}

// LogoutResponse reports completion of the panel logout request and configured
// cleanup.
type LogoutResponse struct {
	Message string `json:"message"` // panel operation status text
}

// RevokeAllSessionsResponse reports completion of account-wide refresh-session
// revocation request handling.
type RevokeAllSessionsResponse struct {
	Message string `json:"message"` // panel operation status text
}

// NewSessionHandler retains account services, runtime readers, pending flow,
// and HTTP callbacks for panel requests. Configure dependencies required by
// exposed operations; optional observers and cleanup callbacks are invoked
// when present.
func NewSessionHandler(auth *identity.AuthService, users *identity.UserService, settings SessionHTTPSettings, redeems InvitationReader, totp *identity.TotpService, pending *identity.PendingFlow, options SessionHTTPOptions) *SessionHandler {
	return &SessionHandler{auth, users, settings, redeems, totp, pending, options}
}

// respondWithTokenPair delegates active-account credential response writing to
// the shared login helper.
func (h *SessionHandler) respondWithTokenPair(c *gin.Context, u *identity.User) {
	RespondWithTokenPair(c, h.authService, u)
}

// auditActor records the supplied account and email through the optional HTTP
// observer. Missing observers are skipped; the callback receives the current
// Gin request and supplies its own audit effect.
func (h *SessionHandler) auditActor(c *gin.Context, id int64, email string) {
	if h.options.AuditActor != nil {
		h.options.AuditActor(c, id, email)
	}
}

// isBackendModeEnabled reads the optional HTTP backend decision callback for
// this operation. Nil handlers or missing callbacks return false; this helper
// does not use the separate settingSvc reader.
func (h *SessionHandler) isBackendModeEnabled(ctx context.Context) bool {
	if h == nil || h.options.BackendMode == nil {
		return false
	}
	return h.options.BackendMode(ctx)
}

// captchaProof combines shared CAPTCHA request fields into the identity
// service proof value.
func captchaProof(turnstileToken, tencentTicket, tencentRandstr string) identity.CaptchaProof {
	return identity.CaptchaProof{
		TurnstileToken: turnstileToken,
		TencentTicket:  tencentTicket,
		TencentRandstr: tencentRandstr,
	}
}

// ensureBackendModeAllowsUser rejects a missing account and an ordinary user
// under the HTTP backend policy. Missing handler or callback permits a
// supplied account; current active-state checks remain with the login flow.
func (h *SessionHandler) ensureBackendModeAllowsUser(ctx context.Context, user *identity.User) error {
	if user == nil {
		return infraerrors.Unauthorized("INVALID_USER", "user not found")
	}
	if h == nil || !h.isBackendModeEnabled(ctx) || user.IsAdmin() {
		return nil
	}
	return infraerrors.Forbidden("BACKEND_MODE_ADMIN_ONLY", "Backend mode is active. Only admin login is allowed.")
}

// ensureBackendModeAllowsNewUserLogin rejects account creation when the HTTP
// backend callback enables administrator-only login. Missing handler or
// callback permits the flow, leaving registration and email policies to
// authentication.
func (h *SessionHandler) ensureBackendModeAllowsNewUserLogin(ctx context.Context) error {
	if h == nil || !h.isBackendModeEnabled(ctx) {
		return nil
	}
	return infraerrors.Forbidden("BACKEND_MODE_ADMIN_ONLY", "Backend mode is active. Only admin login is allowed.")
}

// Register validates account JSON and applicable CAPTCHA before registration
// with email proof and signup codes. It records the selected audit actor after
// account creation, then uses the shared active-account credential response
// helper.
func (h *SessionHandler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	h.auditActor(c, 0, strings.TrimSpace(req.Email))

	// Enabled email verification and a nonblank submitted code skip another
	// CAPTCHA call. Code presence supplies that routing condition; the later
	// registration service still owns verification of the submitted email code.
	proof := captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr)
	if err := h.authService.VerifyCaptchaForRegister(c.Request.Context(), proof, clientip.GetClientIP(c), req.VerifyCode); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	_, user, err := h.authService.RegisterWithVerification(c.Request.Context(), req.Email, req.Password, req.VerifyCode, req.PromoCode, req.InvitationCode, req.AffCode)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.auditActor(c, user.ID, user.Email)

	h.respondWithTokenPair(c, user)
}

// SendVerifyCode validates email and enabled CAPTCHA before queuing
// registration verification mail. Accept-Language accompanies the service
// request, and the response countdown comes from its asynchronous mail
// acceptance result.
func (h *SessionHandler) SendVerifyCode(c *gin.Context) {
	var req SendVerifyCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	proof := captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr)
	if err := h.authService.VerifyCaptcha(c.Request.Context(), proof, clientip.GetClientIP(c)); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	result, err := h.authService.SendVerifyCodeAsync(c.Request.Context(), req.Email, c.GetHeader("Accept-Language"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, SendVerifyCodeResponse{
		Message:   "Verification code sent successfully",
		Countdown: result.Countdown,
	})
}

// Login checks CAPTCHA and password credentials before selecting a TOTP
// challenge or the panel credential response. Password authentication already
// signs an access token, but this handler discards that result while applying
// backend admission and the current factor settings.
//
// When both the global switch and account TOTP flag are enabled, a configured
// factor service saves a temporary challenge for Login2FA. Ordinary success
// records login and asks the response helper for a fresh pair; the earlier
// access token is never returned by this handler.
func (h *SessionHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	h.auditActor(c, 0, strings.TrimSpace(req.Email))

	proof := captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr)
	if err := h.authService.VerifyCaptcha(c.Request.Context(), proof, clientip.GetClientIP(c)); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	token, user, err := h.authService.Login(c.Request.Context(), req.Email, req.Password)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.auditActor(c, user.ID, user.Email)
	_ = token // The response helper issues the access/refresh pair after the remaining login checks.

	if err := h.ensureBackendModeAllowsUser(c.Request.Context(), user); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	if h.totpService != nil && h.settingSvc.IsTotpEnabled(c.Request.Context()) && user.TotpEnabled {
		// Both factor switches select a saved challenge rather than the credential
		// response. Login2FA uses that challenge to verify this same account before
		// the shared response helper issues its client credentials.
		tempToken, err := h.totpService.CreateLoginSession(c.Request.Context(), user.ID, user.Email)
		if err != nil {
			response.InternalError(c, "Failed to create 2FA session")
			return
		}

		response.Success(c, TotpLoginResponse{
			Requires2FA:     true,
			TempToken:       tempToken,
			UserEmailMasked: identity.MaskEmail(user.Email),
		})
		return
	}

	h.authService.RecordSuccessfulLogin(c.Request.Context(), user.ID)

	h.respondWithTokenPair(c, user)
}

// Login2FA validates a temporary authenticator challenge and reloads its
// current account before completing login. Active-account and backend checks
// run before challenge cleanup.
//
// A pending provider binding is applied before its cookie cleanup and user
// reload, so later failures can follow completed binding. Challenge deletion
// errors are discarded after these admission stages. The handler records login
// at the appropriate pending or ordinary branch, then the shared response
// helper issues credentials; cleanup failure does not reestablish a
// challenge-consumption guarantee.
func (h *SessionHandler) Login2FA(c *gin.Context) {
	var req Login2FARequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	slog.Debug("login_2fa_request",
		"temp_token_len", len(req.TempToken),
		"totp_code_len", len(req.TotpCode))

	session, err := h.totpService.GetLoginSession(c.Request.Context(), req.TempToken)
	if err != nil || session == nil {
		tokenPrefix := ""
		// Log an eight-byte prefix when the token is long enough.
		if len(req.TempToken) >= 8 {
			tokenPrefix = req.TempToken[:8]
		}
		slog.Debug("login_2fa_session_invalid",
			"temp_token_prefix", tokenPrefix,
			"error", err)
		response.BadRequest(c, "Invalid or expired 2FA session")
		return
	}
	h.auditActor(c, session.UserID, session.Email)

	slog.Debug("login_2fa_session_found",
		"user_id", session.UserID,
		"email", session.Email)

	if err := h.totpService.VerifyCode(c.Request.Context(), session.UserID, req.TotpCode); err != nil {
		slog.Debug("login_2fa_verify_failed",
			"user_id", session.UserID,
			"error", err)
		response.ErrorFrom(c, err)
		return
	}

	// Read the current account before attempting challenge deletion, so a
	// backend-mode rejection returns before temporary challenge cleanup is
	// invoked.
	user, err := h.userService.GetByID(c.Request.Context(), session.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if err := EnsureLoginUserActive(user); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	if err := h.ensureBackendModeAllowsUser(c.Request.Context(), user); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	if session.PendingOAuthBind != nil {
		// Complete the saved provider binding before reloading account data, so the
		// response sees its persisted changes. Cookie cleanup and a failed reload
		// cannot undo that binding operation.
		if err := h.pending.CompleteSecondFactorBinding(c.Request.Context(), session.PendingOAuthBind, user.ID); err != nil {
			response.ErrorFrom(c, err)
			return
		}
		if h.options.ClearPendingCookies != nil {
			h.options.ClearPendingCookies(c)
		}

		h.authService.RecordSuccessfulLogin(c.Request.Context(), user.ID)

		user, err = h.userService.GetByID(c.Request.Context(), session.UserID)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
	}

	// Attempt challenge deletion after account and binding admission. Cleanup
	// errors are discarded, so response issuance proceeds even if the temporary
	// challenge remains in its backing store.
	_ = h.totpService.DeleteLoginSession(c.Request.Context(), req.TempToken)

	if session.PendingOAuthBind == nil {
		h.authService.RecordSuccessfulLogin(c.Request.Context(), user.ID)
	}

	h.respondWithTokenPair(c, user)
}

// GetCurrentUser writes current account data and identity summaries for the
// authenticated Gin subject. Missing identity or either account/summary read
// failure stops the response before the profile DTO is written.
func (h *SessionHandler) GetCurrentUser(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	user, err := h.userService.GetByID(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	identities, err := h.userService.GetProfileIdentitySummaries(c.Request.Context(), subject.UserID, user)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, UserProfileResponseFromService(user, identities))
}

// ValidatePromoCode reports disabled status before decoding when runtime
// settings turn promotion off. Otherwise the required preview callback
// interprets the submitted code, and its validity, amount, and rejection code
// form the response.
func (h *SessionHandler) ValidatePromoCode(c *gin.Context) {
	if h.settingSvc != nil && !h.settingSvc.IsPromoCodeEnabled(c.Request.Context()) {
		response.Success(c, ValidatePromoCodeResponse{
			Valid:     false,
			ErrorCode: "PROMO_CODE_DISABLED",
		})
		return
	}

	var req ValidatePromoCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	preview := h.options.PreviewPromotion(c.Request.Context(), req.Code)
	response.Success(c, ValidatePromoCodeResponse{Valid: preview.Valid, BonusAmount: preview.BonusAmount, ErrorCode: preview.ErrorCode})
}

// ValidateInvitationCode reports disabled status when settings are absent or
// invitations are off. Enabled validation checks the submitted record type and
// unused status; lookup failures become a not-found code in a success
// envelope.
func (h *SessionHandler) ValidateInvitationCode(c *gin.Context) {
	if h.settingSvc == nil || !h.settingSvc.IsInvitationCodeEnabled(c.Request.Context()) {
		response.Success(c, ValidateInvitationCodeResponse{
			Valid:     false,
			ErrorCode: "INVITATION_CODE_DISABLED",
		})
		return
	}

	var req ValidateInvitationCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	redeemCode, err := h.redeemService.GetByCode(c.Request.Context(), req.Code)
	// Missing records and repository failures share the same public availability
	// result, so clients receive no distinction between an absent invitation and
	// an unavailable lookup.
	if err != nil {
		response.Success(c, ValidateInvitationCodeResponse{
			Valid:     false,
			ErrorCode: "INVITATION_CODE_NOT_FOUND",
		})
		return
	}

	if redeemCode.Type != identity.RedeemTypeInvitation {
		response.Success(c, ValidateInvitationCodeResponse{
			Valid:     false,
			ErrorCode: "INVITATION_CODE_INVALID",
		})
		return
	}

	if redeemCode.Status != identity.StatusUnused {
		response.Success(c, ValidateInvitationCodeResponse{
			Valid:     false,
			ErrorCode: "INVITATION_CODE_USED",
		})
		return
	}

	response.Success(c, ValidateInvitationCodeResponse{
		Valid: true,
	})
}

// ForgotPassword verifies enabled CAPTCHA and requires a configured frontend
// URL before queuing reset mail. It writes the same accepted response for
// registered and unknown addresses after service success and forwards
// Accept-Language.
func (h *SessionHandler) ForgotPassword(c *gin.Context) {
	var req ForgotPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	proof := captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr)
	if err := h.authService.VerifyCaptcha(c.Request.Context(), proof, clientip.GetClientIP(c)); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	frontendBaseURL := strings.TrimSpace(h.settingSvc.GetFrontendURL(c.Request.Context()))
	if frontendBaseURL == "" {
		slog.Error("frontend_url not configured in settings or config; cannot build password reset link")
		response.InternalError(c, "Password reset is not configured")
		return
	}

	// Queue the reset email while returning the same success response for unknown addresses.
	if err := h.authService.RequestPasswordResetAsync(c.Request.Context(), req.Email, frontendBaseURL, c.GetHeader("Accept-Language")); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, ForgotPasswordResponse{
		Message: "If your email is registered, you will receive a password reset link shortly.",
	})
}

// ResetPassword passes the submitted email reset credential and replacement
// password to authentication. Malformed JSON stops before service work;
// successful credential application returns the panel completion message.
func (h *SessionHandler) ResetPassword(c *gin.Context) {
	var req ResetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if err := h.authService.ResetPassword(c.Request.Context(), req.Email, req.Token, req.NewPassword); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, ResetPasswordResponse{
		Message: "Your password has been reset successfully. You can now log in with your new password.",
	})
}

// RefreshToken rotates the submitted refresh credential before checking its
// returned account role against backend mode. Supply initialized
// authentication and runtime readers before exposing this route.
//
// A successful service rotation replaces the submitted credential before this
// HTTP role decision. A refused role receives forbidden and no replacement
// pair in the response; the handler performs no rotation compensation on that
// path. Accepted requests return the issued access and refresh text with the
// access lifetime. Service or decoding failures retain their corresponding
// panel errors.
func (h *SessionHandler) RefreshToken(c *gin.Context) {
	var req RefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	result, err := h.authService.RefreshTokenPair(c.Request.Context(), req.RefreshToken)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// The service has already rotated this refresh credential. Backend-mode
	// rejection withholds the replacement response after that work rather than
	// undoing the issued credential state.
	if h.settingSvc.IsBackendModeEnabled(c.Request.Context()) && result.UserRole != "admin" {
		response.Forbidden(c, "Backend mode is active. Only admin login is allowed.")
		return
	}

	response.Success(c, RefreshTokenResponse{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresIn:    result.ExpiresIn,
		TokenType:    "Bearer",
	})
}

// Logout accepts optional refresh JSON and attempts revocation before
// configured pending browser cleanup. Malformed JSON and revocation errors are
// tolerated, and the handler returns success after invoking its optional
// logout callback.
func (h *SessionHandler) Logout(c *gin.Context) {
	var req LogoutRequest
	// Cookie-only logout clients can omit JSON. Discard decoding errors and
	// continue optional credential revocation and browser cleanup with whatever
	// refresh value was decoded.
	_ = c.ShouldBindJSON(&req)

	if req.RefreshToken != "" {
		if err := h.authService.RevokeRefreshToken(c.Request.Context(), req.RefreshToken); err != nil {
			slog.Debug("failed to revoke refresh token", "error", err)
			// A revocation failure is diagnosed while the browser logout callback still
			// runs, allowing its pending-session consumption and cookie expiration to
			// complete.
		}
	}
	if h.options.LogoutPending != nil {
		h.options.LogoutPending(c)
	}

	response.Success(c, LogoutResponse{
		Message: "Logged out successfully",
	})
}

// RevokeAllSessions requests refresh-session revocation for the authenticated
// Gin account. Service failures produce an internal response; successful
// return uses the completion message while access-token validity follows its
// separate checks.
func (h *SessionHandler) RevokeAllSessions(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	if err := h.authService.RevokeAllUserTokens(c.Request.Context(), subject.UserID); err != nil {
		slog.Error("failed to revoke all sessions", "user_id", subject.UserID, "error", err)
		response.InternalError(c, "Failed to revoke sessions")
		return
	}

	response.Success(c, RevokeAllSessionsResponse{
		Message: "All sessions have been revoked. Please log in again.",
	})
}
