package httpapi

import (
	"github.com/gin-gonic/gin"
)

// Authentication routes share one assembled set of session, pending, and
// provider handlers. Dispatch passes the request to the selected child
// instance, so validation, cookie changes, and response writing stay with the
// handler that implements that login or continuation flow.

// AuthenticationHandler retains the assembled login handlers used by identity
// route dispatch.
type AuthenticationHandler struct {
	// Each handler is retained across requests. Application assembly initializes
	// the child for every registered route before serving; methods directly
	// dispatch through these pointers and do not supply replacements.
	Session  *SessionHandler      // password and session HTTP operations
	Pending  *PendingHandler      // browser pending identity operations
	Bind     *OAuthBindHandler    // temporary current-user binding cookies
	LinuxDo  *LinuxDoHandler      // LinuxDo OAuth HTTP operations
	OIDC     *OIDCHandler         // OIDC OAuth HTTP operations
	Email    *EmailOAuthHandler   // GitHub and Google email OAuth
	Google   *GoogleOneTapHandler // Google One Tap HTTP operations
	WeChat   *WeChatHandler       // WeChat OAuth HTTP operations
	DingTalk *DingTalkHandler     // DingTalk OAuth HTTP operations
}

// BindDingTalkOAuthLogin delegates to the child handler that binds a browser
// pending DingTalk identity after existing-account credential proof.
func (h *AuthenticationHandler) BindDingTalkOAuthLogin(c *gin.Context) {
	h.DingTalk.BindDingTalkOAuthLogin(c)
}

// BindLinuxDoOAuthLogin delegates to the child handler that binds a browser
// pending LinuxDo identity after existing-account credential proof.
func (h *AuthenticationHandler) BindLinuxDoOAuthLogin(c *gin.Context) {
	h.Pending.BindLinuxDoOAuthLogin(c)
}

// BindOIDCOAuthLogin delegates to the child handler that binds a browser
// pending OIDC identity after existing-account credential proof.
func (h *AuthenticationHandler) BindOIDCOAuthLogin(c *gin.Context) { h.Pending.BindOIDCOAuthLogin(c) }

// BindPendingOAuthLogin delegates to the child handler that binds the browser
// pending provider identity after existing-account credential proof.
func (h *AuthenticationHandler) BindPendingOAuthLogin(c *gin.Context) {
	h.Pending.BindPendingOAuthLogin(c)
}

// BindWeChatOAuthLogin delegates to the child handler that binds a browser
// pending WeChat identity after existing-account credential proof.
func (h *AuthenticationHandler) BindWeChatOAuthLogin(c *gin.Context) {
	h.Pending.BindWeChatOAuthLogin(c)
}

// CompleteDingTalkOAuthRegistration delegates to the child handler that
// completes invitation-based signup using a browser pending DingTalk session.
func (h *AuthenticationHandler) CompleteDingTalkOAuthRegistration(c *gin.Context) {
	h.DingTalk.CompleteDingTalkOAuthRegistration(c)
}

// CompleteGitHubOAuthRegistration delegates to the child handler that
// completes password and invitation setup for a verified GitHub-email pending
// session.
func (h *AuthenticationHandler) CompleteGitHubOAuthRegistration(c *gin.Context) {
	h.Email.CompleteGitHubOAuthRegistration(c)
}

// CompleteGoogleOAuthRegistration delegates to the child handler that
// completes password and invitation setup for a verified Google-email pending
// session.
func (h *AuthenticationHandler) CompleteGoogleOAuthRegistration(c *gin.Context) {
	h.Email.CompleteGoogleOAuthRegistration(c)
}

// CompleteLinuxDoOAuthRegistration delegates to the child handler that
// completes invitation-based signup using a browser pending LinuxDo session.
func (h *AuthenticationHandler) CompleteLinuxDoOAuthRegistration(c *gin.Context) {
	h.LinuxDo.CompleteLinuxDoOAuthRegistration(c)
}

// CompleteOIDCOAuthRegistration delegates to the child handler that completes
// invitation-based signup using a browser pending OIDC session.
func (h *AuthenticationHandler) CompleteOIDCOAuthRegistration(c *gin.Context) {
	h.OIDC.CompleteOIDCOAuthRegistration(c)
}

// CompleteWeChatOAuthRegistration delegates to the child handler that
// completes invitation-based signup using a browser pending WeChat session.
func (h *AuthenticationHandler) CompleteWeChatOAuthRegistration(c *gin.Context) {
	h.WeChat.CompleteWeChatOAuthRegistration(c)
}

// CreateDingTalkOAuthAccount delegates to the child handler that creates an
// email account from the browser pending DingTalk registration context.
func (h *AuthenticationHandler) CreateDingTalkOAuthAccount(c *gin.Context) {
	h.DingTalk.CreateDingTalkOAuthAccount(c)
}

// CreateLinuxDoOAuthAccount delegates to the child handler that creates an
// email account from the browser pending LinuxDo registration context.
func (h *AuthenticationHandler) CreateLinuxDoOAuthAccount(c *gin.Context) {
	h.Pending.CreateLinuxDoOAuthAccount(c)
}

// CreateOIDCOAuthAccount delegates to the child handler that creates an email
// account from the browser pending OIDC registration context.
func (h *AuthenticationHandler) CreateOIDCOAuthAccount(c *gin.Context) {
	h.Pending.CreateOIDCOAuthAccount(c)
}

// CreatePendingOAuthAccount delegates to the child handler that creates an
// email account from the browser pending provider registration context.
func (h *AuthenticationHandler) CreatePendingOAuthAccount(c *gin.Context) {
	h.Pending.CreatePendingOAuthAccount(c)
}

// CreateWeChatOAuthAccount delegates to the child handler that creates an
// email account from the browser pending WeChat registration context.
func (h *AuthenticationHandler) CreateWeChatOAuthAccount(c *gin.Context) {
	h.Pending.CreateWeChatOAuthAccount(c)
}

// DingTalkOAuthCallback delegates to the child handler that exchanges DingTalk
// authorization and prepares browser pending login or binding state.
func (h *AuthenticationHandler) DingTalkOAuthCallback(c *gin.Context) {
	h.DingTalk.DingTalkOAuthCallback(c)
}

// DingTalkOAuthStart delegates to the child handler that starts DingTalk
// authorization after the applicable anonymous action-CAPTCHA admission check.
func (h *AuthenticationHandler) DingTalkOAuthStart(c *gin.Context) { h.DingTalk.DingTalkOAuthStart(c) }

// ExchangePendingOAuthCompletion delegates to the child handler that resolves
// browser pending state into account-choice or completed authentication
// responses.
func (h *AuthenticationHandler) ExchangePendingOAuthCompletion(c *gin.Context) {
	h.Pending.ExchangePendingOAuthCompletion(c)
}

// ForgotPassword delegates to the child handler that requests password-reset
// mail after the enabled CAPTCHA check succeeds.
func (h *AuthenticationHandler) ForgotPassword(c *gin.Context) { h.Session.ForgotPassword(c) }

// GetCurrentUser delegates to the child handler that returns current account
// details and identity summaries for the authenticated subject.
func (h *AuthenticationHandler) GetCurrentUser(c *gin.Context) { h.Session.GetCurrentUser(c) }

// GitHubOAuthCallback delegates to the child handler that exchanges GitHub
// authorization for a verified-email login or pending registration
// continuation.
func (h *AuthenticationHandler) GitHubOAuthCallback(c *gin.Context) { h.Email.GitHubOAuthCallback(c) }

// GitHubOAuthStart delegates to the child handler that starts GitHub email
// OAuth authorization after applicable action-CAPTCHA admission.
func (h *AuthenticationHandler) GitHubOAuthStart(c *gin.Context) { h.Email.GitHubOAuthStart(c) }

// GoogleOAuthCallback delegates to the child handler that exchanges Google
// authorization for a verified-email login or pending registration
// continuation.
func (h *AuthenticationHandler) GoogleOAuthCallback(c *gin.Context) { h.Email.GoogleOAuthCallback(c) }

// GoogleOAuthStart delegates to the child handler that starts Google email
// OAuth authorization after applicable action-CAPTCHA admission.
func (h *AuthenticationHandler) GoogleOAuthStart(c *gin.Context) { h.Email.GoogleOAuthStart(c) }

// GoogleOneTap delegates to the child handler that verifies a browser Google
// ID token for login or pending registration.
func (h *AuthenticationHandler) GoogleOneTap(c *gin.Context) { h.Google.GoogleOneTap(c) }

// LinuxDoOAuthCallback delegates to the child handler that resolves LinuxDo
// provider identity into login, binding, or account-choice continuation.
func (h *AuthenticationHandler) LinuxDoOAuthCallback(c *gin.Context) {
	h.LinuxDo.LinuxDoOAuthCallback(c)
}

// LinuxDoOAuthStart delegates to the child handler that starts LinuxDo
// authorization with browser state and configured PKCE support.
func (h *AuthenticationHandler) LinuxDoOAuthStart(c *gin.Context) { h.LinuxDo.LinuxDoOAuthStart(c) }

// Login delegates to the child handler that validates password credentials
// before returning a TOTP challenge or panel credentials.
func (h *AuthenticationHandler) Login(c *gin.Context) { h.Session.Login(c) }

// Login2FA delegates to the child handler that verifies a temporary TOTP login
// challenge and completes its account or pending binding.
func (h *AuthenticationHandler) Login2FA(c *gin.Context) { h.Session.Login2FA(c) }

// Logout delegates to the child handler that attempts refresh-token revocation
// and invokes the configured browser pending logout callback.
func (h *AuthenticationHandler) Logout(c *gin.Context) { h.Session.Logout(c) }

// OIDCOAuthCallback delegates to the child handler that validates configured
// OIDC token proof and resolves a namespaced provider subject.
func (h *AuthenticationHandler) OIDCOAuthCallback(c *gin.Context) { h.OIDC.OIDCOAuthCallback(c) }

// OIDCOAuthStart delegates to the child handler that starts OIDC authorization
// with browser state and configured nonce and PKCE values.
func (h *AuthenticationHandler) OIDCOAuthStart(c *gin.Context) { h.OIDC.OIDCOAuthStart(c) }

// PrepareOAuthBindAccessTokenCookie delegates to the child handler that stores
// an authenticated Bearer credential for a subsequent navigation-based
// identity bind start.
func (h *AuthenticationHandler) PrepareOAuthBindAccessTokenCookie(c *gin.Context) {
	h.Bind.PrepareOAuthBindAccessTokenCookie(c)
}

// RefreshToken delegates to the child handler that rotates a submitted refresh
// credential before applying the HTTP backend-mode role check.
func (h *AuthenticationHandler) RefreshToken(c *gin.Context) { h.Session.RefreshToken(c) }

// Register delegates to the child handler that creates an email account using
// password, verification, and supplied signup codes.
func (h *AuthenticationHandler) Register(c *gin.Context) { h.Session.Register(c) }

// ResetPassword delegates to the child handler that applies the submitted
// email reset credential and replacement password through authentication.
func (h *AuthenticationHandler) ResetPassword(c *gin.Context) { h.Session.ResetPassword(c) }

// RevokeAllSessions delegates to the child handler that requests
// refresh-session revocation for the currently authenticated account.
func (h *AuthenticationHandler) RevokeAllSessions(c *gin.Context) { h.Session.RevokeAllSessions(c) }

// SendPendingOAuthVerifyCode delegates to the child handler that requests
// signup email proof for the current browser-bound pending provider flow.
func (h *AuthenticationHandler) SendPendingOAuthVerifyCode(c *gin.Context) {
	h.Pending.SendPendingOAuthVerifyCode(c)
}

// SendVerifyCode delegates to the child handler that queues registration email
// verification after the enabled CAPTCHA check succeeds.
func (h *AuthenticationHandler) SendVerifyCode(c *gin.Context) { h.Session.SendVerifyCode(c) }

// ValidateInvitationCode delegates to the child handler that reports whether
// the submitted registration invitation has the required type and unused
// status.
func (h *AuthenticationHandler) ValidateInvitationCode(c *gin.Context) {
	h.Session.ValidateInvitationCode(c)
}

// ValidatePromoCode delegates to the child handler that reports signup
// promotion availability and the configured preview result.
func (h *AuthenticationHandler) ValidatePromoCode(c *gin.Context) { h.Session.ValidatePromoCode(c) }

// WeChatOAuthCallback delegates to the child handler that resolves WeChat
// subject and channel observations into browser pending login or binding.
func (h *AuthenticationHandler) WeChatOAuthCallback(c *gin.Context) { h.WeChat.WeChatOAuthCallback(c) }

// WeChatOAuthStart delegates to the child handler that starts WeChat
// authorization with browser state and the selected open or mp mode.
func (h *AuthenticationHandler) WeChatOAuthStart(c *gin.Context) { h.WeChat.WeChatOAuthStart(c) }
