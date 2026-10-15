package httpapi

import (
	"github.com/gin-gonic/gin"
)

// auth_endpoints.go defines the authentication endpoints accepted by public
// and protected route registration.
// Public identity routes and protected session routes accept the same
// assembled endpoint interface. Its operations write the Gin response through
// their specific session or provider handlers, while registration supplies the
// middleware and rate limits appropriate for each route.

// AuthEndpoints defines the authentication HTTP operations consumed by
// identity route registration.
type AuthEndpoints interface {
	// BindDingTalkOAuthLogin proves an existing account and restricts browser
	// pending identity to DingTalk. A required TOTP challenge defers binding;
	// direct completion binds before pair issuance and separate pending
	// consumption.
	BindDingTalkOAuthLogin(*gin.Context)
	// BindLinuxDoOAuthLogin proves an existing account for the pending LinuxDo
	// identity and checks any stored target. Direct completion binds before
	// credential issuance and separate consumption; enabled account TOTP returns
	// a challenge.
	BindLinuxDoOAuthLogin(*gin.Context)
	// BindOIDCOAuthLogin requires existing-account credentials and browser
	// pending state belonging to OIDC. Target mismatch rejects binding, while
	// accepted completion returns a TOTP challenge or binds before token issuance
	// and separate consumption.
	BindOIDCOAuthLogin(*gin.Context)
	// BindPendingOAuthLogin proves an existing account for the provider saved in
	// browser pending state. It honors a stored target and required adoption
	// choice before returning a TOTP challenge or completing binding.
	BindPendingOAuthLogin(*gin.Context)
	// BindWeChatOAuthLogin checks existing-account proof against the pending
	// WeChat provider and any targeted account. TOTP can defer binding; the
	// direct path returns credentials after binding and a separate pending claim.
	BindWeChatOAuthLogin(*gin.Context)
	// CompleteDingTalkOAuthRegistration completes legacy invitation signup or
	// returns remaining account-choice progression. Credentials can precede
	// committed adoption and consumption, and a successful completion starts
	// optional DingTalk synchronization before returning token JSON.
	CompleteDingTalkOAuthRegistration(*gin.Context)
	// CompleteGitHubOAuthRegistration requires a browser-bound verified GitHub
	// email and registration intent. Password and signup codes feed account
	// creation, followed by transactional finalization and its failure
	// compensation before the credential response.
	CompleteGitHubOAuthRegistration(*gin.Context)
	// CompleteGoogleOAuthRegistration creates a password account from
	// browser-bound verified Google-email registration state. Backend admission
	// and provider checks precede creation; finalization failures can invoke
	// compensation before a successful credential response is written.
	CompleteGoogleOAuthRegistration(*gin.Context)
	// CompleteLinuxDoOAuthRegistration reevaluates legacy LinuxDo invitation
	// state and may return account-choice progression. Accepted authentication
	// can issue credentials before adoption and consumption are committed
	// together, with top-level token JSON returned afterward.
	CompleteLinuxDoOAuthRegistration(*gin.Context)
	// CompleteOIDCOAuthRegistration reevaluates email and backend policy for
	// legacy OIDC invitation signup. Account-choice responses retain pending
	// state, while accepted registration authenticates before the combined
	// profile-adoption and browser-consumption database operation.
	CompleteOIDCOAuthRegistration(*gin.Context)
	// CompleteWeChatOAuthRegistration checks legacy WeChat invitation state and
	// remaining local account requirements. Authentication precedes adoption,
	// login recording, and separate browser consumption, so failure in those
	// later stages can follow issued credentials.
	CompleteWeChatOAuthRegistration(*gin.Context)
	// CreateDingTalkOAuthAccount restricts browser registration state to DingTalk
	// before processing local email and password. Existing-account conflicts
	// return account choice; new-account completion binds the identity and
	// consumes pending state during finalization.
	CreateDingTalkOAuthAccount(*gin.Context)
	// CreateLinuxDoOAuthAccount uses browser pending LinuxDo state to register a
	// local email account. CAPTCHA and backend policy apply to new accounts,
	// while an existing email moves the request into account choice.
	CreateLinuxDoOAuthAccount(*gin.Context)
	// CreateOIDCOAuthAccount accepts local registration inputs only for a
	// matching browser pending OIDC session. A successful new account enters
	// binding and consumption finalization; an existing email instead returns
	// account-choice status.
	CreateOIDCOAuthAccount(*gin.Context)
	// CreatePendingOAuthAccount registers local email credentials for the
	// provider recorded in browser pending state. Existing-account lookup or a
	// registration email conflict can return account choice before new-account
	// finalization completes.
	CreatePendingOAuthAccount(*gin.Context)
	// CreateWeChatOAuthAccount requires browser registration state for WeChat
	// before processing local credentials. Finalization binds and consumes the
	// pending identity for a new account, while existing-email observations lead
	// to account-choice continuation.
	CreateWeChatOAuthAccount(*gin.Context)
	// DingTalkOAuthCallback validates provider state, exchanges authorization,
	// and enforces organization policy before unionID lookup. It prepares pending
	// binding, login, or account choice; tolerated staff and optional
	// account-lookups can leave profile data absent.
	DingTalkOAuthCallback(*gin.Context)
	// DingTalkOAuthStart checks applicable action CAPTCHA and reads current
	// organization configuration before browser authorization. A signed
	// current-user target supports binding intent; successful POST returns an
	// authorize URL, and accepted navigation redirects.
	DingTalkOAuthStart(*gin.Context)
	// ExchangePendingOAuthCompletion validates browser proof and returns
	// remaining account-choice requirements or completes an eligible login.
	// Profile adoption precedes separate consumption, and fresh credentials
	// follow consumption; later failures can leave earlier state committed.
	ExchangePendingOAuthCompletion(*gin.Context)
	// ForgotPassword requires applicable CAPTCHA and a configured frontend URL
	// before queuing reset mail. Registered and unknown addresses receive the
	// same accepted response after service success, with Accept-Language
	// forwarded for mail selection.
	ForgotPassword(*gin.Context)
	// GetCurrentUser reads account data and identity summaries for the
	// authenticated Gin subject. RegisterSessionRoutes applies JWT and panel
	// guards, while either failed data read stops the response before the profile
	// DTO is written.
	GetCurrentUser(*gin.Context)
	// GitHubOAuthCallback validates the saved state and selected email provider
	// before verified GitHub profile exchange. It redirects to pending password
	// or invitation completion when required, and accepted direct login puts
	// credentials in the frontend fragment.
	GitHubOAuthCallback(*gin.Context)
	// GitHubOAuthStart checks applicable action CAPTCHA and loads GitHub email
	// OAuth configuration. State, provider, and return cookies support the
	// callback; accepted POST returns an authorize URL while navigation starts
	// redirect.
	GitHubOAuthStart(*gin.Context)
	// GoogleOAuthCallback validates browser state and selected provider before
	// exchanging Google authorization for a verified profile. Pending local
	// signup proof receives a completion redirect, while accepted direct
	// authentication returns credentials through the frontend fragment.
	GoogleOAuthCallback(*gin.Context)
	// GoogleOAuthStart reads Google email OAuth configuration after applicable
	// action CAPTCHA admission. It records state, provider, and local return
	// context in cookies before returning the authorize URL as POST JSON or a
	// navigation redirect.
	GoogleOAuthStart(*gin.Context)
	// GoogleOneTap verifies a bounded Google ID token with required subject and
	// verified email claims. Current settings and backend policy govern login or
	// pending signup, and successful responses disable caching.
	GoogleOneTap(*gin.Context)
	// LinuxDoOAuthCallback verifies state, browser proof, and configured PKCE
	// before resolving the LinuxDo subject. Signed targets and existing owners
	// become pending state; real-email observations and local policy can require
	// account choice.
	LinuxDoOAuthCallback(*gin.Context)
	// LinuxDoOAuthStart checks applicable CAPTCHA and records state, local return
	// path, and account intent. Configured PKCE adds verifier proof, current-user
	// binding signs a target, and POST returns its authorize URL instead of
	// navigating.
	LinuxDoOAuthStart(*gin.Context)
	// Login checks CAPTCHA and password credentials before backend admission and
	// optional TOTP challenge selection. A challenge postpones final credential
	// issuance, while accepted ordinary login records the account and returns an
	// access/refresh pair.
	Login(*gin.Context)
	// Login2FA verifies a temporary TOTP challenge and reloads its current active
	// account before backend admission. Pending provider binding can precede
	// credential issuance, and best-effort challenge deletion can fail while
	// login proceeds.
	Login2FA(*gin.Context)
	// Logout accepts optional refresh JSON and attempts credential revocation
	// before configured pending-cookie cleanup. Malformed bodies and revocation
	// failures are tolerated, and the operation returns success after its
	// optional cleanup callback.
	Logout(*gin.Context)
	// OIDCOAuthCallback validates configured issuer proof, nonce, and browser
	// continuation state before account resolution. Conflicting token and
	// userinfo subjects reject authentication, while present userinfo
	// email-verification state takes precedence during signup choice.
	OIDCOAuthCallback(*gin.Context)
	// OIDCOAuthStart records browser state and intent after applicable CAPTCHA
	// admission. PKCE and ID-token validation independently add verifier and
	// nonce cookies, and current-user binding requires a signed target before
	// returning the authorize URL.
	OIDCOAuthStart(*gin.Context)
	// PrepareOAuthBindAccessTokenCookie stores the authenticated Bearer
	// credential for a navigation-based provider bind start.
	// RegisterSessionRoutes supplies JWT guards; malformed header shape is
	// rejected, and success emits a short-lived HttpOnly cookie with HTTP 204.
	PrepareOAuthBindAccessTokenCookie(*gin.Context)
	// RefreshToken rotates the submitted refresh credential before applying the
	// HTTP backend-mode role check. A refused role receives no replacement pair
	// despite completed rotation, while accepted requests receive access/refresh
	// text and access lifetime.
	RefreshToken(*gin.Context)
	// Register creates an email account using password, applicable email
	// verification, and signup codes. CAPTCHA admission precedes account
	// creation, and the successful account then passes through active-account
	// checking and the shared credential response.
	Register(*gin.Context)
	// ResetPassword applies the submitted email reset credential and replacement
	// password through authentication. Invalid JSON stops before service work,
	// while successful credential application returns the standard panel
	// completion message.
	ResetPassword(*gin.Context)
	// RevokeAllSessions requests refresh-session revocation for the authenticated
	// account supplied by Gin. Service failure produces an internal response, and
	// successful completion leaves access-token validity governed by expiry,
	// account state, and credential checks.
	RevokeAllSessions(*gin.Context)
	// SendPendingOAuthVerifyCode requires CAPTCHA and browser pending
	// registration proof before requesting signup mail. A matching existing email
	// instead saves account-choice progression and returns its status so that
	// flow can prove ownership.
	SendPendingOAuthVerifyCode(*gin.Context)
	// SendVerifyCode checks submitted email and the enabled CAPTCHA before
	// queuing registration verification mail. It forwards Accept-Language, then
	// returns the service acceptance countdown rather than waiting for message
	// delivery.
	SendVerifyCode(*gin.Context)
	// ValidateInvitationCode returns disabled status when settings are absent or
	// invitations are off. Enabled requests require an invitation record with
	// unused status, while lookup errors become a not-found result in a success
	// envelope.
	ValidateInvitationCode(*gin.Context)
	// ValidatePromoCode returns disabled status before decoding when promotion is
	// off. Enabled requests use the configured preview callback to return
	// validity, bonus amount, and rejection code for the submitted signup code.
	ValidatePromoCode(*gin.Context)
	// WeChatOAuthCallback validates browser state and selected mode before
	// retrieving token and userinfo identity. UnionID requirements and historical
	// OpenID lookup govern ownership, then existing accounts receive runtime
	// identity repair before pending login.
	WeChatOAuthCallback(*gin.Context)
	// WeChatOAuthStart checks applicable CAPTCHA and captures open or mp mode
	// with state and local return context. Binding intent signs the current
	// account target, while accepted POST returns an authorize URL and navigation
	// redirects.
	WeChatOAuthStart(*gin.Context)
}
