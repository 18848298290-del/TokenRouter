package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/oauthpkce"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// email_oauth.go handles GitHub and Google browser authorization and pending
// email registration completion.
// GitHub and Google email OAuth share browser state, provider selection, and
// verified-email account resolution. Start stores the state and selected
// provider in short-lived cookies, then callback checks both before exchanging
// authorization and fetching the provider profile.
//
// An identity needing password or invitation completion receives a
// browser-bound pending registration draft. Existing-account success instead
// delivers its access and refresh credentials in a frontend URL fragment after
// the backend-mode account check. Callback cleanup starts after required
// code/state input is present, so earlier provider/configuration errors leave
// those start cookies until expiry. Account creation is finalized through the
// pending flow, which owns transactional binding and compensation for later
// failure.

// Email OAuth cookie names, shared path, lifetime, and local redirect defaults
// retain provider and signup context across browser authorization and callback
// requests.
const (
	EmailOAuthCookiePath        = "/api/v1/auth/oauth"   // shared email OAuth cookie path
	EmailOAuthStateCookieName   = "email_oauth_state"    // authorization state cookie name
	EmailOAuthRedirectCookie    = "email_oauth_redirect" // local return path cookie
	EmailOAuthProviderCookie    = "email_oauth_provider" // selected login provider cookie
	EmailOAuthAffCookie         = "email_oauth_aff"      // signup affiliate cookie name
	EmailOAuthCookieMaxAgeSec   = 10 * 60                // seconds
	EmailOAuthDefaultRedirect   = "/dashboard"           // default local login destination
	EmailOAuthDefaultFrontendCB = "/auth/oauth/callback" // default pending browser callback
)

// EmailOAuthHandler shares verified-email authorization handling between
// GitHub and Google login providers.
type EmailOAuthHandler struct {
	*PendingHandler                                                                   // shared browser pending authentication
	client          identity.EmailOAuthClient                                         // provider authorization and profile client
	loadConfig      func(context.Context, string) (identity.EmailOAuthOptions, error) // current provider configuration callback
}

// CompleteEmailOAuthRequest supplies local password and signup codes for
// verified-email pending registration.
type CompleteEmailOAuthRequest struct {
	Password       string `json:"password" binding:"required,min=6"` // local account password input
	InvitationCode string `json:"invitation_code,omitempty"`         // optional registration invitation code
	AffCode        string `json:"aff_code,omitempty"`                // optional signup affiliate code
}

// NewEmailOAuthHandler retains shared pending handlers, provider client, and
// current configuration callback. Configure these dependencies before exposing
// authorization or verified-email completion routes for GitHub and Google.
func NewEmailOAuthHandler(p *PendingHandler, client identity.EmailOAuthClient, load func(context.Context, string) (identity.EmailOAuthOptions, error)) *EmailOAuthHandler {
	return &EmailOAuthHandler{p, client, load}
}

// GitHubOAuthStart delegates the shared handler for GitHub verified-email
// authorization start.
func (h *EmailOAuthHandler) GitHubOAuthStart(c *gin.Context) { h.EmailOAuthStart(c, "github") }

// GoogleOAuthStart delegates the shared handler for Google verified-email
// authorization start.
func (h *EmailOAuthHandler) GoogleOAuthStart(c *gin.Context) { h.EmailOAuthStart(c, "google") }

// GitHubOAuthCallback delegates the shared handler for GitHub verified-email
// provider callback.
func (h *EmailOAuthHandler) GitHubOAuthCallback(c *gin.Context) { h.EmailOAuthCallback(c, "github") }

// GoogleOAuthCallback delegates the shared handler for Google verified-email
// provider callback.
func (h *EmailOAuthHandler) GoogleOAuthCallback(c *gin.Context) { h.EmailOAuthCallback(c, "google") }

// CompleteGitHubOAuthRegistration delegates the shared handler for GitHub
// verified-email pending registration.
func (h *EmailOAuthHandler) CompleteGitHubOAuthRegistration(c *gin.Context) {
	h.CompleteEmailOAuthRegistration(c, "github")
}

// CompleteGoogleOAuthRegistration delegates the shared handler for Google
// verified-email pending registration.
func (h *EmailOAuthHandler) CompleteGoogleOAuthRegistration(c *gin.Context) {
	h.CompleteEmailOAuthRegistration(c, "google")
}

// EmailOAuthStart checks action CAPTCHA and reads the selected provider
// configuration before preparing browser authorization. Invalid local return
// paths select the dashboard default.
//
// State, provider selection, and return path enter separate short-lived
// cookies so callback can verify the same start context. Affiliate aliases and
// promotion input are captured beside those values. Cookies are written before
// authorize URL construction, so a later URL error can leave that state in the
// response. Successful POST writes the authorize URL envelope, while accepted
// navigation starts redirect.
func (h *EmailOAuthHandler) EmailOAuthStart(c *gin.Context, provider string) {
	if !h.RequireActionCaptchaForOAuthLoginStart(c) {
		return
	}
	cfg, err := h.loadConfig(c.Request.Context(), provider)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	state, err := oauthpkce.Verifier()
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_STATE_GEN_FAILED", "failed to generate oauth state").WithCause(err))
		return
	}
	redirectTo := SanitizeFrontendRedirectPath(c.Query("redirect"))
	if redirectTo == "" {
		redirectTo = EmailOAuthDefaultRedirect
	}

	secureCookie := IsRequestHTTPS(c)
	// Publish state, provider, and return cookies for callback comparisons before
	// building the authorize URL. Later errors leave the ten-minute cookies in
	// the response headers.
	EmailOAuthSetCookie(c, EmailOAuthStateCookieName, EncodeCookieValue(state), secureCookie)
	EmailOAuthSetCookie(c, EmailOAuthRedirectCookie, EncodeCookieValue(redirectTo), secureCookie)
	EmailOAuthSetCookie(c, EmailOAuthProviderCookie, EncodeCookieValue(provider), secureCookie)
	CaptureOAuthPromoCode(c, secureCookie)
	// Direct authorization links can carry affiliate text under aff or aff_code.
	// A nonblank aff value wins, then the selected value survives callback
	// through its cookie.
	if affCode := strings.TrimSpace(identity.OAuthFirstNonEmpty(c.Query("aff"), c.Query("aff_code"))); affCode != "" {
		EmailOAuthSetCookie(c, EmailOAuthAffCookie, EncodeCookieValue(affCode), secureCookie)
	} else {
		EmailOAuthClearCookie(c, EmailOAuthAffCookie, secureCookie)
	}

	authURL, err := BuildEmailOAuthAuthorizeURL(cfg, state)
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_BUILD_URL_FAILED", "failed to build oauth authorization url").WithCause(err))
		return
	}
	RespondOAuthStart(c, authURL)
}

// EmailOAuthCallback verifies stored state and provider selection before
// exchanging authorization and fetching a verified profile. The configured
// frontend callback receives provider and flow errors as redirects.
//
// Cookie cleanup is deferred after nonempty code and state are checked, then
// state/provider cookies prevent using another start flow provider. Redirect
// cookie failures select the local default. Token exchange and profile fetch
// precede account resolution; their failures retain the configured frontend
// error path. Earlier configuration, provider-error, or missing-input exits
// occur before cleanup registration.
func (h *EmailOAuthHandler) EmailOAuthCallback(c *gin.Context, provider string) {
	cfg, cfgErr := h.loadConfig(c.Request.Context(), provider)
	if cfgErr != nil {
		response.ErrorFrom(c, cfgErr)
		return
	}
	frontendCallback := strings.TrimSpace(cfg.FrontendRedirectURL)
	if frontendCallback == "" {
		frontendCallback = EmailOAuthDefaultFrontendCB
	}
	if providerErr := strings.TrimSpace(c.Query("error")); providerErr != "" {
		RedirectOAuthError(c, frontendCallback, "provider_error", providerErr, c.Query("error_description"))
		return
	}
	code := strings.TrimSpace(c.Query("code"))
	state := strings.TrimSpace(c.Query("state"))
	if code == "" || state == "" {
		RedirectOAuthError(c, frontendCallback, "missing_params", "missing code/state", "")
		return
	}

	secureCookie := IsRequestHTTPS(c)
	// Expire email OAuth state, selected provider, redirect, affiliate, and
	// promotion cookies after this callback reaches code/state validation.
	// Earlier input/configuration exits retain those values until expiry.
	defer func() {
		EmailOAuthClearCookie(c, EmailOAuthStateCookieName, secureCookie)
		EmailOAuthClearCookie(c, EmailOAuthRedirectCookie, secureCookie)
		EmailOAuthClearCookie(c, EmailOAuthProviderCookie, secureCookie)
		EmailOAuthClearCookie(c, EmailOAuthAffCookie, secureCookie)
		ClearOAuthPromoCodeCookie(c, secureCookie)
	}()
	expectedState, err := ReadCookieDecoded(c, EmailOAuthStateCookieName)
	if err != nil || expectedState == "" || expectedState != state {
		RedirectOAuthError(c, frontendCallback, "invalid_state", "invalid oauth state", "")
		return
	}
	// Read errors produce empty provider text. The equality check rejects that
	// absence, keeping a callback for one email provider from accepting another
	// provider browser state.
	expectedProvider, _ := ReadCookieDecoded(c, EmailOAuthProviderCookie)
	if !strings.EqualFold(strings.TrimSpace(expectedProvider), provider) {
		RedirectOAuthError(c, frontendCallback, "invalid_state", "invalid oauth provider", "")
		return
	}
	// Return-cookie failure leaves no usable local path. Continue the verified
	// provider callback with the dashboard destination while the required state
	// and provider checks remain enforced.
	redirectTo, _ := ReadCookieDecoded(c, EmailOAuthRedirectCookie)
	redirectTo = SanitizeFrontendRedirectPath(redirectTo)
	if redirectTo == "" {
		redirectTo = EmailOAuthDefaultRedirect
	}

	tokenResp, err := h.client.ExchangeCode(c.Request.Context(), cfg, code)
	if err != nil {
		RedirectOAuthError(c, frontendCallback, "token_exchange_failed", "failed to exchange oauth code", OAuthSingleLine(err.Error()))
		return
	}
	profile, err := h.client.FetchProfile(c.Request.Context(), provider, cfg, tokenResp)
	if err != nil {
		RedirectOAuthError(c, frontendCallback, "userinfo_failed", "failed to fetch verified email", OAuthSingleLine(err.Error()))
		return
	}
	h.EmailOAuthCallbackWithProfile(c, provider, cfg, frontendCallback, redirectTo, profile)
}

// EmailOAuthCallbackWithProfile resolves a validated provider profile into
// login or browser pending registration. The caller supplies the verified
// profile, provider, callback, and sanitized return path after state checks;
// cfg is retained in the signature but not read here.
//
// The flow can select pending password or invitation completion before
// authentication. An invitation-required result also creates a pending draft.
// Otherwise successful account resolution receives a backend-mode check before
// its credentials enter the frontend fragment. The fragment carries issued
// access and refresh text directly, while later account/pending failures
// remain errors after earlier service work.
func (h *EmailOAuthHandler) EmailOAuthCallbackWithProfile(
	c *gin.Context,
	provider string,
	cfg identity.EmailOAuthOptions,
	frontendCallback string,
	redirectTo string,
	profile *identity.EmailOAuthProfile,
) {
	input := identity.EmailOAuthIdentityFromProfile(provider, profile)
	affCode := h.EmailOAuthAffCode(c)
	// Account identity resolution can require local password or invitation
	// completion. Store a browser pending draft before redirecting the frontend
	// to collect that proof.
	if shouldCreate, err := h.flow.EmailShouldCreatePendingRegistration(c.Request.Context(), input); err != nil {
		RedirectOAuthError(c, frontendCallback, infraerrors.Reason(err), infraerrors.Message(err), "")
		return
	} else if shouldCreate {
		if pendingErr := h.CreateEmailRegistrationSession(c, provider, frontendCallback, redirectTo, profile, affCode, ReadOAuthPromoCode(c)); pendingErr != nil {
			RedirectOAuthError(c, frontendCallback, infraerrors.Reason(pendingErr), infraerrors.Message(pendingErr), "")
			return
		}
		RedirectToFrontendCallback(c, frontendCallback)
		return
	}

	tokenPair, user, err := h.authService.LoginOrRegisterVerifiedEmailOAuthWithSignupCodes(c.Request.Context(), input, "", affCode, ReadOAuthPromoCode(c))
	if err != nil {
		if errors.Is(err, identity.ErrOAuthInvitationRequired) {
			if pendingErr := h.CreateEmailRegistrationSession(c, provider, frontendCallback, redirectTo, profile, affCode, ReadOAuthPromoCode(c)); pendingErr != nil {
				RedirectOAuthError(c, frontendCallback, infraerrors.Reason(pendingErr), infraerrors.Message(pendingErr), "")
				return
			}
			RedirectToFrontendCallback(c, frontendCallback)
			return
		}
		RedirectOAuthError(c, frontendCallback, infraerrors.Reason(err), infraerrors.Message(err), "")
		return
	}
	if err := h.ensureBackendModeAllowsUser(c.Request.Context(), user); err != nil {
		RedirectOAuthError(c, frontendCallback, "login_blocked", infraerrors.Reason(err), infraerrors.Message(err))
		return
	}

	fragment := url.Values{}
	fragment.Set("access_token", tokenPair.AccessToken)
	fragment.Set("refresh_token", tokenPair.RefreshToken)
	fragment.Set("expires_in", fmt.Sprintf("%d", tokenPair.ExpiresIn))
	fragment.Set("token_type", "Bearer")
	fragment.Set("redirect", redirectTo)
	RedirectOAuthFragment(c, frontendCallback, fragment)
}

// EmailOAuthAffCode reads trimmed affiliate text from the shared email OAuth
// cookie. Nil contexts, absent cookies, or decode failures return empty text
// so signup can continue without optional referral input.
func (h *EmailOAuthHandler) EmailOAuthAffCode(c *gin.Context) string {
	if c == nil {
		return ""
	}
	// Affiliate metadata is optional registration input. A missing or malformed
	// cookie leaves the verified-email flow able to continue with no saved
	// referral code.
	if code, err := ReadCookieDecoded(c, EmailOAuthAffCookie); err == nil {
		return strings.TrimSpace(code)
	}
	return ""
}

// CompleteEmailOAuthRegistration creates a password account from a
// browser-bound verified-email pending session. It requires matching provider,
// registration intent, and backend policy before passing password and signup
// codes to authentication.
//
// Request affiliate input takes precedence over the saved provider hint. A
// created account then enters verified finalization, whose failures are mapped
// by phase and can trigger flow compensation; browser cookies are cleared on
// consumption failure and success. Promotion and login recording follow
// successful finalization before the top-level credential response is written.
func (h *EmailOAuthHandler) CompleteEmailOAuthRegistration(c *gin.Context, provider string) {
	var req CompleteEmailOAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	_, session, clearCookies, err := ReadPendingOAuthBrowserSession(c, h.PendingHandler)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if err := EnsurePendingOAuthCompleteRegistrationSession(session); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(session.ProviderType), provider) {
		response.BadRequest(c, "Pending oauth session provider mismatch")
		return
	}
	if err := h.ensureBackendModeAllowsNewUserLogin(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	affCode := strings.TrimSpace(req.AffCode)
	if affCode == "" {
		affCode = strings.TrimSpace(PendingSessionStringValue(session.UpstreamIdentityClaims, "aff_code"))
	}

	tokenPair, user, err := h.authService.RegisterVerifiedOAuthEmailAccount(
		c.Request.Context(),
		strings.TrimSpace(session.ResolvedEmail),
		req.Password,
		strings.TrimSpace(req.InvitationCode),
		strings.TrimSpace(session.ProviderType),
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	userProjection := user
	request := identity.PendingAccountFinalization{Session: session, User: userProjection, InvitationCode: strings.TrimSpace(req.InvitationCode), AffiliateCode: affCode}
	if err := h.pendingFlow().FinalizeVerifiedAccount(c.Request.Context(), request); err != nil {
		if failure, ok := errors.AsType[*identity.PendingWriteError](err); ok {
			switch failure.Phase {
			case "binding":
				RespondPendingOAuthBindingApplyError(c, failure.Cause)
			case "consume":
				clearCookies()
				response.ErrorFrom(c, failure.Cause)
			case "begin", "commit":
				response.ErrorFrom(c, infraerrors.InternalServer("PENDING_AUTH_BIND_APPLY_FAILED", "failed to consume pending oauth session").WithCause(failure.Cause))
			default:
				response.ErrorFrom(c, failure.Cause)
			}
		} else {
			response.ErrorFrom(c, err)
		}
		return
	}

	h.authService.ApplyOAuthSignupPromoCode(c.Request.Context(), user.ID, PendingOAuthPromoCode(session))
	h.authService.RecordSuccessfulLogin(c.Request.Context(), user.ID)
	clearCookies()
	WriteOAuthTokenPairResponse(c, tokenPair)
}

// BuildEmailOAuthAuthorizeURL encodes selected provider authorization
// parameters into its configured URL. Existing unrelated query values remain,
// supplied scopes are included when nonblank, and parse failure returns a
// contextual error.
func BuildEmailOAuthAuthorizeURL(cfg identity.EmailOAuthOptions, state string) (string, error) {
	u, err := url.Parse(cfg.AuthorizeURL)
	if err != nil {
		return "", fmt.Errorf("parse authorize_url: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", cfg.RedirectURL)
	q.Set("state", state)
	if strings.TrimSpace(cfg.Scopes) != "" {
		q.Set("scope", cfg.Scopes)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// EmailOAuthSetCookie emits a supplied value at the shared email OAuth path
// with a ten-minute lifetime. Callers encode its value first and supply the
// HTTPS decision; HttpOnly and SameSite Lax support callback navigation.
func EmailOAuthSetCookie(c *gin.Context, name, value string, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     EmailOAuthCookiePath,
		MaxAge:   EmailOAuthCookieMaxAgeSec,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// EmailOAuthClearCookie emits deletion of the named shared email OAuth cookie.
// Matching path and HttpOnly/SameSite Lax attributes accompany the caller
// HTTPS decision so callback cleanup targets the stored browser value.
func EmailOAuthClearCookie(c *gin.Context, name string, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     EmailOAuthCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}
