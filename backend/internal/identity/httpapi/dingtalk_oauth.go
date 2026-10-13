package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/oauthpkce"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// DingTalk login uses unionID as the stable provider subject while
// organization and staff data feed policy checks and profile hints. Browser
// state preserves login or signed current-user binding intent, and callback
// verifies that state before token exchange and directory lookup.
//
// Organization policy decides whether staff lookup failures reject login or
// continue with an empty profile. Existing provider ownership enters pending
// login with asynchronous profile synchronization; optional-email and
// required-email branches prepare different pending completion controls.
// Compatibility email lookup errors are discarded here, so later pending
// binding owns the definitive identity check. Legacy invitation completion can
// issue credentials before adoption-and-consumption persistence, and
// successful account resolution starts synchronization before the token
// response.

// DingTalk browser values, frontend defaults, and the compiled compatibility
// gate retain authorization state, account intent, and real-email
// account-choice behavior across callback navigation.
const (
	DingTalkOAuthCookiePath         = "/api/v1/auth/oauth/dingtalk" // DingTalk browser cookie path
	DingTalkOAuthStateCookieName    = "dingtalk_oauth_state"        // DingTalk authorization state cookie
	DingTalkOAuthRedirectCookie     = "dingtalk_oauth_redirect"     // DingTalk local return cookie
	DingTalkOAuthIntentCookieName   = "dingtalk_oauth_intent"       // DingTalk account intent cookie
	DingTalkOAuthBindUserCookieName = "dingtalk_oauth_bind_user"    // signed target account cookie
	DingTalkOAuthCookieMaxAgeSec    = 600                           // seconds
	DingTalkOAuthDefaultRedirectTo  = "/dashboard"                  // default local login destination
	DingTalkOAuthDefaultFrontendCB  = "/auth/dingtalk/callback"     // default DingTalk browser callback

	DingTalkLevelThreeEnabled = true // compiled real-email compatibility gate
)

// DingTalkHTTPOptions supplies current provider configuration and the optional
// registration decision.
type DingTalkHTTPOptions struct {
	LoadConfig func(context.Context) (identity.DingTalkOAuthOptions, error) // current DingTalk configuration callback
	// RegistrationEnabled is optional in the local signup-block helper. A missing
	// callback leaves that helper unblocked; configured observations are
	// interpreted with provider bypass settings by identity rules.
	RegistrationEnabled func(context.Context) bool // optional global registration reader
}

// DingTalkHandler resolves DingTalk unionID and organization policy into
// browser pending authentication.
type DingTalkHandler struct {
	*PendingHandler                               // shared browser pending authentication
	binding         *OAuthBindHandler             // signed current-account binding helper
	syncer          *identity.DingTalkSyncRuntime // provider profile synchronization runtime
	dingTalkOptions DingTalkHTTPOptions           // retained DingTalk HTTP configuration
}

// CompleteDingTalkOAuthRequest carries invitation and profile choices for
// legacy DingTalk pending signup.
type CompleteDingTalkOAuthRequest struct {
	InvitationCode string `json:"invitation_code" binding:"required"` // required registration invitation code
	AffCode        string `json:"aff_code,omitempty"`                 // optional signup affiliate code
	// AdoptDisplayName and AdoptAvatar keep omitted choices as nil and supplied
	// false values as refusals. The helper retains saved values for omitted
	// flags and otherwise defaults those flags to false before applying supplied
	// choices.
	AdoptDisplayName *bool `json:"adopt_display_name,omitempty"` // optional provider-name adoption choice
	AdoptAvatar      *bool `json:"adopt_avatar,omitempty"`       // optional provider-avatar adoption choice
}

// NewDingTalkHandler retains pending and binding handlers, synchronization
// runtime, and HTTP callbacks. Initialize the configuration reader and
// provider synchronization capability before serving exposed authorization and
// completion routes.
func NewDingTalkHandler(p *PendingHandler, b *OAuthBindHandler, s *identity.DingTalkSyncRuntime, o DingTalkHTTPOptions) *DingTalkHandler {
	return &DingTalkHandler{p, b, s, o}
}

// isDingTalkSignupBlocked combines configured provider registration policy
// with the optional global switch. Missing registration callback returns
// false; the identity helper interprets any configured provider bypass for the
// selected observation.
func (h *DingTalkHandler) isDingTalkSignupBlocked(ctx context.Context, cfg identity.DingTalkOAuthOptions) bool {
	if h.dingTalkOptions.RegistrationEnabled == nil {
		return false
	}
	return identity.DingTalkSignupBlocked(cfg, h.dingTalkOptions.RegistrationEnabled(ctx))
}

// findDingTalkCompatEmailUser delegates real-email account lookup when the
// compiled compatibility gate permits it. A disabled gate returns nil with no
// error, leaving callback account choice without a matched local user.
func (h *DingTalkHandler) findDingTalkCompatEmailUser(ctx context.Context, email string) (*identity.User, error) {
	if !DingTalkLevelThreeEnabled {
		return nil, nil
	}
	return h.flow.Database.FindDingTalkCompatEmailUser(ctx, email)
}

// DingTalkUpstreamRedirect diagnoses a nonnil upstream failure and writes a
// frontend error fragment. Structured DingTalk errors supply provider code and
// message; other errors use application text, with the fragment helper
// applying its byte bound.
func DingTalkUpstreamRedirect(c *gin.Context, frontendCallback, step string, err error) {
	dtCode := ""
	dtMsg := ""
	dtHTTP := 0
	if apiErr, ok := errors.AsType[*identity.DingTalkAPIError](err); ok {
		dtCode = apiErr.Code
		dtMsg = apiErr.Message
		dtHTTP = apiErr.HTTP
	}
	slog.Error("dingtalk upstream call failed",
		"step", step,
		"dingtalk_code", dtCode,
		"dingtalk_msg", dtMsg,
		"http_status", dtHTTP,
		"error", err.Error(),
	)
	msg := dtMsg
	// A structured provider failure can omit message text. Application error
	// mapping then supplies the description used by the bounded frontend error
	// fragment.
	if strings.TrimSpace(msg) == "" {
		msg = infraerrors.Message(err)
	}
	if strings.TrimSpace(dtCode) != "" {
		msg = "dingtalk[" + dtCode + "] " + msg
	}
	RedirectOAuthError(c, frontendCallback, identity.DingTalkErrorCode(err), msg, "")
}

// SetDingTalkCookie emits a supplied encoded browser value at the DingTalk
// path with a seconds-based lifetime. The caller supplies HTTPS state;
// HttpOnly and SameSite Lax support provider callback navigation.
func SetDingTalkCookie(c *gin.Context, name string, value string, maxAgeSec int, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     DingTalkOAuthCookiePath,
		MaxAge:   maxAgeSec,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearDingTalkCookie expires a named browser value at the matching DingTalk
// path. The caller supplies HTTPS state, while HttpOnly and SameSite Lax match
// the retained callback cookie.
func ClearDingTalkCookie(c *gin.Context, name string, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     DingTalkOAuthCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// DingTalkOAuthStart verifies action CAPTCHA and prepares state, intent,
// return path, and browser proof before authorization. Configuration-read
// failures use the default frontend disabled-provider redirect.
//
// Current-user binding signs a target cookie; other starts expire a preceding
// target. Browser and provider cookies are emitted before later target or
// authorize URL failure, so those errors can leave partial start state. Signup
// promotion context is captured separately. Successful POST returns an
// authorize URL envelope, while accepted navigation requests redirect.
func (h *DingTalkHandler) DingTalkOAuthStart(c *gin.Context) {
	if !h.RequireActionCaptchaForOAuthLoginStart(c) {
		return
	}
	cfg, err := h.dingTalkOptions.LoadConfig(c.Request.Context())
	if err != nil {
		// A configuration failure supplies no current authorize URL. Use the fixed
		// DingTalk callback to report disabled-provider status for every such read
		// error before start cookies are emitted.
		frontendCB := DingTalkOAuthDefaultFrontendCB
		RedirectOAuthError(c, frontendCB, "dingtalk_not_enabled", "", "")
		return
	}

	state, err := oauthpkce.Verifier()
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_STATE_GEN_FAILED", "failed to generate oauth state").WithCause(err))
		return
	}

	redirectTo := SanitizeFrontendRedirectPath(c.Query("redirect"))
	if redirectTo == "" {
		redirectTo = DingTalkOAuthDefaultRedirectTo
	}

	browserSessionKey, err := GenerateOAuthPendingBrowserSession()
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_BROWSER_SESSION_GEN_FAILED", "failed to generate oauth browser session").WithCause(err))
		return
	}

	secureCookie := IsRequestHTTPS(c)
	// Save DingTalk start context and fresh browser proof before signed-target
	// and authorize URL work. These cookie headers survive a later preparation
	// failure, while the preceding pending token cookie is expired.
	SetDingTalkCookie(c, DingTalkOAuthStateCookieName, EncodeCookieValue(state), DingTalkOAuthCookieMaxAgeSec, secureCookie)
	SetDingTalkCookie(c, DingTalkOAuthRedirectCookie, EncodeCookieValue(redirectTo), DingTalkOAuthCookieMaxAgeSec, secureCookie)

	intent := NormalizeOAuthIntent(c.Query("intent"))
	SetDingTalkCookie(c, DingTalkOAuthIntentCookieName, EncodeCookieValue(intent), DingTalkOAuthCookieMaxAgeSec, secureCookie)
	CaptureOAuthPromoCode(c, secureCookie)

	SetOAuthPendingBrowserCookie(c, browserSessionKey, secureCookie)
	ClearOAuthPendingSessionCookie(c, secureCookie)

	if intent == OauthIntentBindCurrentUser {
		bindCookieValue, err := h.binding.BuildOAuthBindUserCookieFromContext(c)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		SetDingTalkCookie(c, DingTalkOAuthBindUserCookieName, EncodeCookieValue(bindCookieValue), DingTalkOAuthCookieMaxAgeSec, secureCookie)
	} else {
		ClearDingTalkCookie(c, DingTalkOAuthBindUserCookieName, secureCookie)
	}

	authURL, err := BuildDingTalkAuthorizeURL(cfg, state)
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_BUILD_URL_FAILED", "failed to build dingtalk authorization url").WithCause(err))
		return
	}

	RespondOAuthStart(c, authURL)
}

// DingTalkOAuthCallback verifies browser state and exchanges authorization
// before enforcing organization policy and resolving unionID. Staff lookup can
// return empty profile data when policy tolerates provider failures.
//
// Signed targets enter pending binding, while resolved existing identities
// enter pending login and start asynchronous synchronization.
// Existing-identity lookup errors are ignored here and behave like no selected
// account. Optional-email policy uses a synthetic address or binding-login
// continuation; required missing email enters completion unless signup is
// blocked. Real-email matching feeds account choice, with its lookup errors
// also discarded. Cookie cleanup begins after code/state presence; the
// bind-target cookie is cleared on its successful binding branch.
func (h *DingTalkHandler) DingTalkOAuthCallback(c *gin.Context) {
	cfg, cfgErr := h.dingTalkOptions.LoadConfig(c.Request.Context())
	if cfgErr != nil {
		response.ErrorFrom(c, cfgErr)
		return
	}

	frontendCallback := strings.TrimSpace(cfg.FrontendRedirectURL)
	if frontendCallback == "" {
		frontendCallback = DingTalkOAuthDefaultFrontendCB
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
	// Expire state, return, intent, and promotion cookies on later callback exits
	// after code/state presence. The signed bind target is expired separately
	// after successful binding continuation.
	defer func() {
		ClearDingTalkCookie(c, DingTalkOAuthStateCookieName, secureCookie)
		ClearDingTalkCookie(c, DingTalkOAuthRedirectCookie, secureCookie)
		ClearDingTalkCookie(c, DingTalkOAuthIntentCookieName, secureCookie)
		ClearOAuthPromoCodeCookie(c, secureCookie)
	}()

	expectedState, err := ReadCookieDecoded(c, DingTalkOAuthStateCookieName)
	if err != nil || state != expectedState {
		RedirectOAuthError(c, frontendCallback, "csrf", "state mismatch", "")
		return
	}
	// Return and intent cookies supply optional callback context, so failed reads
	// produce empty strings. Intent normalization then selects login, while the
	// browser proof below still needs to be present.
	redirectTo, _ := ReadCookieDecoded(c, DingTalkOAuthRedirectCookie)
	intent, _ := ReadCookieDecoded(c, DingTalkOAuthIntentCookieName)
	intent = NormalizeOAuthIntent(intent)
	// Unreadable browser proof becomes empty and rejects continuation. Optional
	// return and intent defaults cannot replace the browser key required before
	// provider exchange.
	browserSessionKey, _ := ReadOAuthPendingBrowserCookie(c)
	if strings.TrimSpace(browserSessionKey) == "" {
		RedirectOAuthError(c, frontendCallback, "missing_browser_session", "missing browser session cookie", "")
		return
	}
	forceEmailOnSignup := h.isForceEmailOnThirdPartySignup(c.Request.Context())

	// Token exchange and unionID lookup are required; staff lookup follows the
	// organization policy.
	client := h.syncer.Client(cfg)
	userToken, err := client.ExchangeCodeForUserToken(c.Request.Context(), code)
	if err != nil {
		DingTalkUpstreamRedirect(c, frontendCallback, "exchange_code", err)
		return
	}

	// Check organization policy before making further provider calls.
	corpID := strings.TrimSpace(userToken.CorpID)
	if !identity.DingTalkCorpAllowed(cfg, corpID) {
		// Organization rejection uses a fixed frontend code with blank details,
		// keeping the observed corporation ID out of this rejection redirect while
		// the policy decision remains server-side.
		RedirectOAuthError(c, frontendCallback, "corp_rejected", "", "")
		return
	}

	// The returned unionID becomes the provider subject and the seed for
	// synthetic email. The nickname contributes profile metadata, while account
	// ownership uses that stable subject.
	unionID, oauthNick, err := client.GetUnionIdByUserToken(c.Request.Context(), userToken.AccessToken)
	if err != nil {
		DingTalkUpstreamRedirect(c, frontendCallback, "get_union_id", err)
		return
	}

	identityKey := identity.PendingAuthIdentityKey{ProviderType: "dingtalk", ProviderKey: "dingtalk", ProviderSubject: unionID}

	staff, failedStep, err := loadDingTalkStaff(c.Request.Context(), client, cfg.CorpRestrictionPolicy, unionID, corpID)
	if err != nil {
		DingTalkUpstreamRedirect(c, frontendCallback, failedStep, err)
		return
	}

	// A nonblank personal OAuth nickname overrides the directory nickname, so
	// later provider claims and account synchronization observe that profile
	// value even when staff lookup supplied a different name.
	if staff != nil && strings.TrimSpace(oauthNick) != "" {
		staff.Nickname = strings.TrimSpace(oauthNick)
	}

	upstreamClaims := identity.DingTalkUpstreamClaims(staff, unionID, corpID)

	// Bind the external identity to the authenticated user.
	if intent == OauthIntentBindCurrentUser {
		targetUserID, err := h.binding.ReadOAuthBindUserIDFromCookie(c, DingTalkOAuthBindUserCookieName)
		if err != nil {
			RedirectOAuthError(c, frontendCallback, "invalid_state", "invalid bind user cookie", "")
			return
		}
		// Cross-organization staff profiles can lack email. Use the unionID-derived
		// synthetic address as pending context while the signed target, rather than
		// that address, selects the binding account.
		bindResolvedEmail := staff.Email
		if bindResolvedEmail == "" {
			bindResolvedEmail = identity.DingTalkSyntheticEmail(unionID)
		}
		if err := h.CreateOAuthPendingSession(c, OauthPendingSessionPayload{
			Intent: OauthIntentBindCurrentUser, Identity: identityKey,
			TargetUserID: &targetUserID, ResolvedEmail: bindResolvedEmail,
			RedirectTo: redirectTo, BrowserSessionKey: browserSessionKey,
			UpstreamIdentityClaims: upstreamClaims,
			CompletionResponse:     map[string]any{"redirect": redirectTo},
		}); err != nil {
			RedirectOAuthError(c, frontendCallback, "session_error", infraerrors.Reason(err), infraerrors.Message(err))
			return
		}
		ClearDingTalkCookie(c, DingTalkOAuthBindUserCookieName, secureCookie)
		RedirectToFrontendCallback(c, frontendCallback)
		return
	}

	// Existing-identity lookup failures are discarded in this callback and select
	// no existing account. Later pending identity binding still owns the
	// definitive provider-subject ownership validation.
	if existing, _ := h.flow.Database.FindOAuthIdentityUser(c.Request.Context(), identityKey); existing != nil {
		// The existing user ID permits profile synchronization immediately.
		// Run it asynchronously so department lookups do not delay the login redirect.
		h.syncer.Async(c.Request.Context(), cfg, client, existing.ID, staff, false)
		if err := h.CreateOAuthPendingSession(c, OauthPendingSessionPayload{
			Intent: OauthIntentLogin, Identity: identityKey, TargetUserID: &existing.ID,
			ResolvedEmail: existing.Email, RedirectTo: redirectTo, BrowserSessionKey: browserSessionKey,
			UpstreamIdentityClaims: upstreamClaims,
			CompletionResponse:     map[string]any{"redirect": redirectTo},
		}); err != nil {
			RedirectOAuthError(c, frontendCallback, "session_error", infraerrors.Reason(err), infraerrors.Message(err))
			return
		}
		RedirectToFrontendCallback(c, frontendCallback)
		return
	}

	signupBlocked := h.isDingTalkSignupBlocked(c.Request.Context(), cfg)

	// Optional-email login retains subject-based continuation even when staff
	// supplied a real email. The synthetic address enters pending state for
	// new-account handling, while blocked signup selects existing-account proof.
	if !cfg.RequireEmail {
		if signupBlocked {
			// Blocked registration leaves external identity ownership unresolved. Ask
			// for an existing-account login with no target or resolved email, allowing
			// credential proof to choose the eligible account.
			if err := h.CreateOAuthPendingSession(c, OauthPendingSessionPayload{
				Intent: OauthIntentLogin, Identity: identityKey, TargetUserID: nil,
				ResolvedEmail: "", RedirectTo: redirectTo, BrowserSessionKey: browserSessionKey,
				UpstreamIdentityClaims: upstreamClaims,
				CompletionResponse:     identity.DingTalkBindLoginCompletionResponse(redirectTo),
			}); err != nil {
				RedirectOAuthError(c, frontendCallback, "session_error", infraerrors.Reason(err), infraerrors.Message(err))
				return
			}
			RedirectToFrontendCallback(c, frontendCallback)
			return
		}
		syntheticEmail := identity.DingTalkSyntheticEmail(unionID)
		if err := h.CreateOAuthPendingSession(c, OauthPendingSessionPayload{
			Intent: OauthIntentLogin, Identity: identityKey, TargetUserID: nil,
			ResolvedEmail: syntheticEmail, RedirectTo: redirectTo, BrowserSessionKey: browserSessionKey,
			UpstreamIdentityClaims: upstreamClaims,
			CompletionResponse:     map[string]any{"redirect": redirectTo, "synthetic_email": syntheticEmail},
		}); err != nil {
			RedirectOAuthError(c, frontendCallback, "session_error", infraerrors.Reason(err), infraerrors.Message(err))
			return
		}
		RedirectToFrontendCallback(c, frontendCallback)
		return
	}

	// Missing required email leaves no real address for new-account creation.
	// Prepare email completion as the next browser step, then let
	// blocked-registration policy choose existing-account proof instead.
	if staff.Email == "" {
		completionResponse := map[string]any{
			"step":                      "email_completion",
			"requires_email_completion": true,
			"redirect":                  redirectTo,
		}
		if signupBlocked {
			// When registration is blocked, collecting a new address cannot complete
			// signup. Replace email-completion instructions with existing-account
			// binding so the next browser request supplies account credentials.
			completionResponse = identity.DingTalkBindLoginCompletionResponse(redirectTo)
		}
		if err := h.CreateOAuthPendingSession(c, OauthPendingSessionPayload{
			Intent: OauthIntentLogin, Identity: identityKey, TargetUserID: nil,
			ResolvedEmail: "", RedirectTo: redirectTo, BrowserSessionKey: browserSessionKey,
			UpstreamIdentityClaims: upstreamClaims,
			CompletionResponse:     completionResponse,
		}); err != nil {
			RedirectOAuthError(c, frontendCallback, "session_error", infraerrors.Reason(err), infraerrors.Message(err))
			return
		}
		RedirectToFrontendCallback(c, frontendCallback)
		return
	}

	// A provider email enters the account-choice flow.
	var compatEmailUser *identity.User
	if DingTalkLevelThreeEnabled && staff.Email != "" {
		// Compatibility email is an optional account-choice hint. Discard lookup
		// failure and let pending account proof and later identity persistence
		// decide the continuation.
		compatEmailUser, _ = h.findDingTalkCompatEmailUser(c.Request.Context(), staff.Email)
	}
	if err := h.CreateDingTalkChoiceSession(
		c, identityKey, staff.Email, staff.Email,
		redirectTo, browserSessionKey, upstreamClaims,
		staff.Email, compatEmailUser, forceEmailOnSignup,
		signupBlocked,
	); err != nil {
		RedirectOAuthError(c, frontendCallback, "session_error", infraerrors.Reason(err), infraerrors.Message(err))
		return
	}
	RedirectToFrontendCallback(c, frontendCallback)
}

// BuildDingTalkAuthorizeURL encodes client, callback, state, scopes, and
// consent prompt into the configured provider URL. Blank scopes select openid;
// missing authorize/callback text and parse failures return application
// errors.
func BuildDingTalkAuthorizeURL(cfg identity.DingTalkOAuthOptions, state string) (string, error) {
	base := strings.TrimSpace(cfg.AuthorizeURL)
	if base == "" {
		return "", infraerrors.InternalServer("DINGTALK_AUTHORIZE_URL_EMPTY", "dingtalk authorize_url not configured")
	}
	redirectURI := strings.TrimSpace(cfg.RedirectURL)
	if redirectURI == "" {
		return "", infraerrors.InternalServer("DINGTALK_REDIRECT_URL_EMPTY", "dingtalk redirect_url not configured")
	}

	u, err := url.Parse(base)
	if err != nil {
		return "", infraerrors.InternalServer("DINGTALK_AUTHORIZE_URL_PARSE_FAILED", "failed to parse dingtalk authorize_url").WithCause(err)
	}

	scopes := strings.TrimSpace(cfg.Scopes)
	if scopes == "" {
		scopes = "openid"
	}

	q := u.Query()
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", scopes)
	q.Set("state", state)
	q.Set("prompt", "consent")
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// CompleteDingTalkOAuthRegistration completes legacy browser signup or returns
// updated account-choice progression. Missing stored username falls back to
// the email local part before account and provider-identity checks.
//
// Invitation-based authentication produces credentials before adoption and
// browser consumption are committed together. Successful persistence starts
// profile synchronization when configuration can be read, then records login
// and expires pending proof before writing top-level token JSON.
// Synchronization setup configuration errors are ignored, so that optional
// work does not change the completed registration response.
func (h *DingTalkHandler) CompleteDingTalkOAuthRegistration(c *gin.Context) {
	var req CompleteDingTalkOAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}

	secureCookie := IsRequestHTTPS(c)
	sessionToken, err := ReadOAuthPendingSessionCookie(c)
	if err != nil {
		ClearOAuthPendingSessionCookie(c, secureCookie)
		ClearOAuthPendingBrowserCookie(c, secureCookie)
		response.ErrorFrom(c, identity.ErrPendingAuthSessionNotFound)
		return
	}
	browserSessionKey, err := ReadOAuthPendingBrowserCookie(c)
	if err != nil {
		ClearOAuthPendingSessionCookie(c, secureCookie)
		ClearOAuthPendingBrowserCookie(c, secureCookie)
		response.ErrorFrom(c, identity.ErrPendingAuthBrowserMismatch)
		return
	}
	pendingSvc, err := h.pendingIdentityService()
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	session, err := pendingSvc.GetBrowserSession(c.Request.Context(), sessionToken, browserSessionKey)
	if err != nil {
		ClearOAuthPendingSessionCookie(c, secureCookie)
		ClearOAuthPendingBrowserCookie(c, secureCookie)
		response.ErrorFrom(c, err)
		return
	}
	if err := EnsurePendingOAuthCompleteRegistrationSession(session); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	updatedSession, handled, err := h.flow.LegacyRegistrationStatus(c.Request.Context(), session, h.authService != nil && h.authService.IsEmailVerifyEnabled(c.Request.Context()), h.isForceEmailOnThirdPartySignup(c.Request.Context()))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	} else if handled {
		c.JSON(http.StatusOK, BuildPendingOAuthSessionStatusPayload(updatedSession))
		return
	}
	session = updatedSession
	if err := h.ensureBackendModeAllowsNewUserLogin(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	email := strings.TrimSpace(session.ResolvedEmail)
	username := PendingSessionStringValue(session.UpstreamIdentityClaims, "username")
	// Missing stored provider username falls back to the email local part for
	// legacy invitation completion. If no usable separator exists, retain the
	// whole stored email as that account-name input.
	if username == "" {
		if at := strings.Index(email, "@"); at > 0 {
			username = email[:at]
		} else {
			username = email
		}
	}
	if email == "" || username == "" {
		response.ErrorFrom(c, infraerrors.BadRequest("PENDING_AUTH_SESSION_INVALID", "pending auth registration context is invalid"))
		return
	}

	if !h.flow.Available() {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready"))
		return
	}
	if err := h.flow.Database.EnsureRegistrationIdentityAvailable(c.Request.Context(), session); err != nil {
		RespondPendingOAuthBindingApplyError(c, err)
		return
	}
	decision, err := h.ensurePendingOAuthAdoptionDecision(c, session.ID, OauthAdoptionDecisionRequest{
		AdoptDisplayName: req.AdoptDisplayName,
		AdoptAvatar:      req.AdoptAvatar,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	tokenPair, user, err := h.authService.LoginOrRegisterOAuthWithTokenPairAndPromoCode(c.Request.Context(), email, username, req.InvitationCode, req.AffCode, PendingOAuthPromoCode(session), "dingtalk")
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// Credentials have already been issued before this combined pending write.
	// Commit adoption and browser consumption before optional synchronization and
	// delivery; failure cannot undo the preceding authentication operation.
	if err := h.flow.Database.ApplyAdoptionAndConsume(c.Request.Context(), session, decision, user.ID); err != nil {
		RespondPendingOAuthBindingApplyError(c, err)
		return
	}
	// A committed account can start asynchronous profile synchronization when
	// configuration is available. Skip a failed settings read as optional work;
	// pending completion already succeeded and the token response still follows.
	if completionCfg, cfgErr := h.dingTalkOptions.LoadConfig(c.Request.Context()); cfgErr == nil {
		dtClient := h.syncer.Client(completionCfg)
		claims := session.UpstreamIdentityClaims
		h.syncer.FromClaims(c.Request.Context(), completionCfg, dtClient, user.ID, claims, true)
	}
	h.authService.RecordSuccessfulLogin(c.Request.Context(), user.ID)
	ClearOAuthPendingSessionCookie(c, secureCookie)
	ClearOAuthPendingBrowserCookie(c, secureCookie)

	c.JSON(http.StatusOK, gin.H{
		"access_token":  tokenPair.AccessToken,
		"refresh_token": tokenPair.RefreshToken,
		"expires_in":    tokenPair.ExpiresIn,
		"token_type":    "Bearer",
	})
}

// CreateDingTalkOAuthAccount delegates local account registration for the
// browser pending DingTalk provider.
func (h *DingTalkHandler) CreateDingTalkOAuthAccount(c *gin.Context) {
	h.CreatePendingAccountForProvider(c, "dingtalk")
}

// BindDingTalkOAuthLogin delegates existing-account proof and binding for the
// pending DingTalk provider.
func (h *DingTalkHandler) BindDingTalkOAuthLogin(c *gin.Context) {
	h.BindPendingLoginForProvider(c, "dingtalk")
}

// CreateDingTalkChoiceSession prepares account choice from staff email, local
// match, signup, and email policies. The identity helper owns those controls
// before the shared pending writer persists the browser draft.
func (h *DingTalkHandler) CreateDingTalkChoiceSession(
	c *gin.Context,
	identityKey identity.PendingAuthIdentityKey,
	suggestedEmail string,
	resolvedEmail string,
	redirectTo string,
	browserSessionKey string,
	upstreamClaims map[string]any,
	compatEmail string,
	compatEmailUser *identity.User,
	forceEmailOnSignup bool,
	signupBlocked bool,
) error {
	draft := identity.PrepareDingTalkChoice(identityKey, suggestedEmail, resolvedEmail, redirectTo, browserSessionKey, upstreamClaims, compatEmail, compatEmailUser, forceEmailOnSignup, signupBlocked)
	return h.CreateOAuthPendingSession(c, draft)
}

// loadDingTalkStaff resolves the staff ID and profile after unionID lookup.
// Exact internal_only policy returns the failing stage and error; callers
// handle later email requirements independently.
//
// The user-ID request runs first, and a failure stops the profile lookup.
// Other policies diagnose either stage failure and return an allocated empty
// profile with no error label, allowing claim and email-policy processing to
// continue. Successful calls retain the client profile pointer, which callback
// callers require to be nonnil.
func loadDingTalkStaff(ctx context.Context, client identity.DingTalkOAuthClient, policy, unionID, corpID string) (*identity.DingTalkProfileSnapshot, string, error) {
	upstreamUserID, err := client.GetUserIdByUnionId(ctx, unionID)
	if err != nil {
		if policy == "internal_only" {
			return nil, "get_user_id", err
		}
		slog.Debug("dingtalk step3 fallback (none/cross-org)", "corp_id", corpID, "union_id", unionID, "err", err)
		// This organization policy permits continuation without directory details
		// after provider failure. Return an allocated empty profile so subsequent
		// email policy and claim preparation can inspect it safely.
		return &identity.DingTalkProfileSnapshot{}, "", nil
	}
	staff, err := client.GetStaffInfoByUserId(ctx, upstreamUserID)
	if err != nil {
		if policy == "internal_only" {
			return nil, "get_staff_info", err
		}
		slog.Debug("dingtalk step4 fallback (none/cross-org)", "corp_id", corpID, "union_id", unionID, "err", err)
		// This organization policy permits continuation without directory details
		// after provider failure. Return an allocated empty profile so subsequent
		// email policy and claim preparation can inspect it safely.
		return &identity.DingTalkProfileSnapshot{}, "", nil
	}
	return staff, "", nil
}
