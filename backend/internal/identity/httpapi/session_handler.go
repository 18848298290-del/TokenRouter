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

// SessionHTTPSettings reads current authentication switches for each operation.
type SessionHTTPSettings interface {
	IsBackendModeEnabled(context.Context) bool
	IsTotpEnabled(context.Context) bool
	IsPromoCodeEnabled(context.Context) bool
	IsInvitationCodeEnabled(context.Context) bool
	GetFrontendURL(context.Context) string
}
type InvitationReader interface {
	GetByCode(context.Context, string) (*identity.RedeemCode, error)
}

// PromotionPreview contains validation results, bonus amount, and error details.
type PromotionPreview struct {
	Valid       bool
	BonusAmount float64
	ErrorCode   string
}

// SessionHTTPOptions supplies HTTP callbacks, cookie operations, and startup parameters.
type SessionHTTPOptions struct {
	BackendMode         func(context.Context) bool
	AuditActor          func(*gin.Context, int64, string)
	ClearPendingCookies func(*gin.Context)
	LogoutPending       func(*gin.Context)
	PreviewPromotion    func(context.Context, string) PromotionPreview
}
type SessionHandler struct {
	authService   *identity.AuthService
	userService   *identity.UserService
	settingSvc    SessionHTTPSettings
	redeemService InvitationReader
	totpService   *identity.TotpService
	pending       *identity.PendingFlow
	options       SessionHTTPOptions
}

// RegisterRequest represents the registration request payload.
type RegisterRequest struct {
	Email                 string `json:"email" binding:"required,email"`
	Password              string `json:"password" binding:"required,min=6"`
	VerifyCode            string `json:"verify_code"`
	TurnstileToken        string `json:"turnstile_token"`
	TencentCaptchaTicket  string `json:"tencent_captcha_ticket"`
	TencentCaptchaRandstr string `json:"tencent_captcha_randstr"`
	PromoCode             string `json:"promo_code"`      // Signup promotion code.
	InvitationCode        string `json:"invitation_code"` // Registration invitation code.
	AffCode               string `json:"aff_code"`        // Affiliate invitation code.
}

// SendVerifyCodeRequest requests a registration email verification code.
type SendVerifyCodeRequest struct {
	Email                 string `json:"email" binding:"required,email"`
	TurnstileToken        string `json:"turnstile_token"`
	TencentCaptchaTicket  string `json:"tencent_captcha_ticket"`
	TencentCaptchaRandstr string `json:"tencent_captcha_randstr"`
}

// SendVerifyCodeResponse reports verification-mail queuing and the retry delay.
type SendVerifyCodeResponse struct {
	Message   string `json:"message"`
	Countdown int    `json:"countdown"` // Seconds.
}

// LoginRequest represents the login request payload.
type LoginRequest struct {
	Email                 string `json:"email" binding:"required,email"`
	Password              string `json:"password" binding:"required"`
	TurnstileToken        string `json:"turnstile_token"`
	TencentCaptchaTicket  string `json:"tencent_captcha_ticket"`
	TencentCaptchaRandstr string `json:"tencent_captcha_randstr"`
}

// TotpLoginResponse represents the response when 2FA is required.
type TotpLoginResponse struct {
	Requires2FA     bool   `json:"requires_2fa"`
	TempToken       string `json:"temp_token,omitempty"`
	UserEmailMasked string `json:"user_email_masked,omitempty"`
}

// Login2FARequest represents the 2FA login request.
type Login2FARequest struct {
	TempToken string `json:"temp_token" binding:"required"`
	TotpCode  string `json:"totp_code" binding:"required,len=6"`
}

// ValidatePromoCodeRequest requests signup-promotion validation.
type ValidatePromoCodeRequest struct {
	Code string `json:"code" binding:"required"`
}

// ValidatePromoCodeResponse reports promotion validity and the bonus amount.
type ValidatePromoCodeResponse struct {
	Valid       bool    `json:"valid"`
	BonusAmount float64 `json:"bonus_amount,omitempty"`
	ErrorCode   string  `json:"error_code,omitempty"`
	Message     string  `json:"message,omitempty"`
}

// ValidateInvitationCodeRequest requests registration-invitation validation.
type ValidateInvitationCodeRequest struct {
	Code string `json:"code" binding:"required"`
}

// ValidateInvitationCodeResponse reports invitation validity.
type ValidateInvitationCodeResponse struct {
	Valid     bool   `json:"valid"`
	ErrorCode string `json:"error_code,omitempty"`
}

// ForgotPasswordRequest requests a password-reset email.
type ForgotPasswordRequest struct {
	Email                 string `json:"email" binding:"required,email"`
	TurnstileToken        string `json:"turnstile_token"`
	TencentCaptchaTicket  string `json:"tencent_captcha_ticket"`
	TencentCaptchaRandstr string `json:"tencent_captcha_randstr"`
}

// ForgotPasswordResponse reports acceptance of a password-reset request.
type ForgotPasswordResponse struct {
	Message string `json:"message"`
}

// ResetPasswordRequest supplies the reset token and replacement password.
type ResetPasswordRequest struct {
	Email       string `json:"email" binding:"required,email"`
	Token       string `json:"token" binding:"required"`
	NewPassword string `json:"new_password" binding:"required,min=6"`
}

// ResetPasswordResponse reports password-reset completion.
type ResetPasswordResponse struct {
	Message string `json:"message"`
}

// RefreshTokenRequest supplies the credential to rotate.
type RefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// RefreshTokenResponse contains the replacement access and refresh tokens.
type RefreshTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"` // Access-token lifetime in seconds.
	TokenType    string `json:"token_type"`
}

// LogoutRequest optionally supplies a refresh token to revoke.
type LogoutRequest struct {
	RefreshToken string `json:"refresh_token,omitempty"` // Optional credential to revoke.
}

// LogoutResponse reports logout completion.
type LogoutResponse struct {
	Message string `json:"message"`
}

// RevokeAllSessionsResponse reports revocation of the user's refresh sessions.
type RevokeAllSessionsResponse struct {
	Message string `json:"message"`
}

func NewSessionHandler(auth *identity.AuthService, users *identity.UserService, settings SessionHTTPSettings, redeems InvitationReader, totp *identity.TotpService, pending *identity.PendingFlow, options SessionHTTPOptions) *SessionHandler {
	return &SessionHandler{auth, users, settings, redeems, totp, pending, options}
}

func (h *SessionHandler) respondWithTokenPair(c *gin.Context, u *identity.User) {
	RespondWithTokenPair(c, h.authService, u)
}

func (h *SessionHandler) auditActor(c *gin.Context, id int64, email string) {
	if h.options.AuditActor != nil {
		h.options.AuditActor(c, id, email)
	}
}

func (h *SessionHandler) isBackendModeEnabled(ctx context.Context) bool {
	if h == nil || h.options.BackendMode == nil {
		return false
	}
	return h.options.BackendMode(ctx)
}

func captchaProof(turnstileToken, tencentTicket, tencentRandstr string) identity.CaptchaProof {
	return identity.CaptchaProof{
		TurnstileToken: turnstileToken,
		TencentTicket:  tencentTicket,
		TencentRandstr: tencentRandstr,
	}
}

func (h *SessionHandler) ensureBackendModeAllowsUser(ctx context.Context, user *identity.User) error {
	if user == nil {
		return infraerrors.Unauthorized("INVALID_USER", "user not found")
	}
	if h == nil || !h.isBackendModeEnabled(ctx) || user.IsAdmin() {
		return nil
	}
	return infraerrors.Forbidden("BACKEND_MODE_ADMIN_ONLY", "Backend mode is active. Only admin login is allowed.")
}

func (h *SessionHandler) ensureBackendModeAllowsNewUserLogin(ctx context.Context) error {
	if h == nil || !h.isBackendModeEnabled(ctx) {
		return nil
	}
	return infraerrors.Forbidden("BACKEND_MODE_ADMIN_ONLY", "Backend mode is active. Only admin login is allowed.")
}

// Register handles user registration
// POST /api/v1/auth/register
func (h *SessionHandler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	h.auditActor(c, 0, strings.TrimSpace(req.Email))

	// Email-code registration reuses the CAPTCHA check performed when requesting the code.
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

// SendVerifyCode queues a registration email verification code.
// POST /api/v1/auth/send-verify-code
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

// Login handles user login
// POST /api/v1/auth/login
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

	// Check if TOTP 2FA is enabled for this user
	if h.totpService != nil && h.settingSvc.IsTotpEnabled(c.Request.Context()) && user.TotpEnabled {
		// Create a temporary login session for 2FA
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

// Login2FA completes the login with 2FA verification
// POST /api/v1/auth/login/2fa
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

	// Load the user before consuming the challenge so backend-mode checks can still reject login.
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

	// Attempt challenge deletion after the login checks pass.
	_ = h.totpService.DeleteLoginSession(c.Request.Context(), req.TempToken)

	if session.PendingOAuthBind == nil {
		h.authService.RecordSuccessfulLogin(c.Request.Context(), user.ID)
	}

	h.respondWithTokenPair(c, user)
}

// GetCurrentUser handles getting current authenticated user
// GET /api/v1/auth/me
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

// ValidatePromoCode checks a signup promotion before registration.
// POST /api/v1/auth/validate-promo-code
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

// ValidateInvitationCode checks an invitation before registration.
// POST /api/v1/auth/validate-invitation-code
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

// ForgotPassword requests a password-reset email.
// POST /api/v1/auth/forgot-password
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

// ResetPassword applies a password-reset token.
// POST /api/v1/auth/reset-password
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

// RefreshToken rotates the supplied refresh token.
// POST /api/v1/auth/refresh
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

	// Backend mode: block non-admin token refresh
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

// Logout attempts refresh-token revocation and clears login cookies.
// POST /api/v1/auth/logout
func (h *SessionHandler) Logout(c *gin.Context) {
	var req LogoutRequest
	// Accept empty request bodies for clients that only need cookie cleanup.
	_ = c.ShouldBindJSON(&req)

	if req.RefreshToken != "" {
		if err := h.authService.RevokeRefreshToken(c.Request.Context(), req.RefreshToken); err != nil {
			slog.Debug("failed to revoke refresh token", "error", err)
			// Revocation errors do not prevent cookie cleanup.
		}
	}
	if h.options.LogoutPending != nil {
		h.options.LogoutPending(c)
	}

	response.Success(c, LogoutResponse{
		Message: "Logged out successfully",
	})
}

// RevokeAllSessions revokes the current user's refresh sessions.
// POST /api/v1/auth/revoke-all-sessions
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
