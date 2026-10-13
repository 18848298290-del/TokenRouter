package httpapi

import (
	"github.com/gin-gonic/gin"
)

// Logout expires pending-login and current-user binding cookies alongside
// provider callback state. Cookie writers use provider-specific paths, so
// cleanup calls the same provider helpers with the request HTTPS decision to
// expire the browser values at their matching paths.

// ClearOAuthLoginCookies expires shared pending cookies and LinuxDo, OIDC, and
// WeChat callback cookies for this browser. Secure attributes follow
// IsRequestHTTPS; provider helpers determine each cookie path and emit
// deletion headers.
func ClearOAuthLoginCookies(c *gin.Context) {
	secureCookie := IsRequestHTTPS(c)

	ClearOAuthPendingSessionCookie(c, secureCookie)
	ClearOAuthPendingBrowserCookie(c, secureCookie)
	ClearOAuthBindAccessTokenCookie(c, secureCookie)

	LinuxDoClearCookie(c, LinuxDoOAuthStateCookieName, secureCookie)
	LinuxDoClearCookie(c, LinuxDoOAuthVerifierCookie, secureCookie)
	LinuxDoClearCookie(c, LinuxDoOAuthRedirectCookie, secureCookie)
	LinuxDoClearCookie(c, LinuxDoOAuthIntentCookieName, secureCookie)
	LinuxDoClearCookie(c, LinuxDoOAuthBindUserCookieName, secureCookie)

	OIDCClearCookie(c, OidcOAuthStateCookieName, secureCookie)
	OIDCClearCookie(c, OidcOAuthVerifierCookie, secureCookie)
	OIDCClearCookie(c, OidcOAuthRedirectCookie, secureCookie)
	OIDCClearCookie(c, OidcOAuthNonceCookie, secureCookie)
	OIDCClearCookie(c, OidcOAuthIntentCookieName, secureCookie)
	OIDCClearCookie(c, OidcOAuthBindUserCookieName, secureCookie)

	WeChatClearCookie(c, WechatOAuthStateCookieName, secureCookie)
	WeChatClearCookie(c, WechatOAuthRedirectCookieName, secureCookie)
	WeChatClearCookie(c, WechatOAuthIntentCookieName, secureCookie)
	WeChatClearCookie(c, WechatOAuthModeCookieName, secureCookie)
	WeChatClearCookie(c, WechatOAuthBindUserCookieName, secureCookie)
}
