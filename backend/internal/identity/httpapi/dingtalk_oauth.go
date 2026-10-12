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

const (
	DingTalkOAuthCookiePath         = "/api/v1/auth/oauth/dingtalk"
	DingTalkOAuthStateCookieName    = "dingtalk_oauth_state"
	DingTalkOAuthRedirectCookie     = "dingtalk_oauth_redirect"
	DingTalkOAuthIntentCookieName   = "dingtalk_oauth_intent"
	DingTalkOAuthBindUserCookieName = "dingtalk_oauth_bind_user"
	DingTalkOAuthCookieMaxAgeSec    = 600 // 10 minutes.
	DingTalkOAuthDefaultRedirectTo  = "/dashboard"
	DingTalkOAuthDefaultFrontendCB  = "/auth/dingtalk/callback"

	DingTalkLevelThreeEnabled = true
)

// DingTalkHTTPOptions supplies current provider configuration and registration policy.
type DingTalkHTTPOptions struct {
	LoadConfig          func(context.Context) (identity.DingTalkOAuthOptions, error)
	RegistrationEnabled func(context.Context) bool
}
type DingTalkHandler struct {
	*PendingHandler
	binding         *OAuthBindHandler
	syncer          *identity.DingTalkSyncRuntime
	dingTalkOptions DingTalkHTTPOptions
}

type CompleteDingTalkOAuthRequest struct {
	InvitationCode   string `json:"invitation_code" binding:"required"`
	AffCode          string `json:"aff_code,omitempty"`
	AdoptDisplayName *bool  `json:"adopt_display_name,omitempty"`
	AdoptAvatar      *bool  `json:"adopt_avatar,omitempty"`
}

func NewDingTalkHandler(p *PendingHandler, b *OAuthBindHandler, s *identity.DingTalkSyncRuntime, o DingTalkHTTPOptions) *DingTalkHandler {
	return &DingTalkHandler{p, b, s, o}
}

func (h *DingTalkHandler) isDingTalkSignupBlocked(ctx context.Context, cfg identity.DingTalkOAuthOptions) bool {
	if h.dingTalkOptions.RegistrationEnabled == nil {
		return false
	}
	return identity.DingTalkSignupBlocked(cfg, h.dingTalkOptions.RegistrationEnabled(ctx))
}

func (h *DingTalkHandler) findDingTalkCompatEmailUser(ctx context.Context, email string) (*identity.User, error) {
	if !DingTalkLevelThreeEnabled {
		return nil, nil
	}
	return h.flow.Database.FindDingTalkCompatEmailUser(ctx, email)
}

// DingTalkUpstreamRedirect logs an upstream failure and redirects to the frontend error page.
// The error fragment includes the DingTalk code and message for diagnosis.
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
	if strings.TrimSpace(msg) == "" {
		msg = infraerrors.Message(err)
	}
	if strings.TrimSpace(dtCode) != "" {
		msg = "dingtalk[" + dtCode + "] " + msg
	}
	RedirectOAuthError(c, frontendCallback, identity.DingTalkErrorCode(err), msg, "")
}

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

// DingTalkOAuthStart starts the DingTalk Connect login flow.
// GET /api/v1/auth/oauth/dingtalk/start?redirect=/dashboard&intent=login
func (h *DingTalkHandler) DingTalkOAuthStart(c *gin.Context) {
	if !h.RequireActionCaptchaForOAuthLoginStart(c) {
		return
	}
	cfg, err := h.dingTalkOptions.LoadConfig(c.Request.Context())
	if err != nil {
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

// DingTalkOAuthCallback handles DingTalk authorization and creates pending login or binding state.
// GET /api/v1/auth/oauth/dingtalk/callback?code=...&state=...
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
	redirectTo, _ := ReadCookieDecoded(c, DingTalkOAuthRedirectCookie)
	intent, _ := ReadCookieDecoded(c, DingTalkOAuthIntentCookieName)
	intent = NormalizeOAuthIntent(intent)
	browserSessionKey, _ := ReadOAuthPendingBrowserCookie(c)
	if strings.TrimSpace(browserSessionKey) == "" {
		RedirectOAuthError(c, frontendCallback, "missing_browser_session", "missing browser session cookie", "")
		return
	}
	forceEmailOnSignup := h.isForceEmailOnThirdPartySignup(c.Request.Context())

	// Token exchange and unionID lookup are required; staff lookup follows the organization policy.
	client := h.syncer.Client(cfg)
	userToken, err := client.ExchangeCodeForUserToken(c.Request.Context(), code)
	if err != nil {
		DingTalkUpstreamRedirect(c, frontendCallback, "exchange_code", err)
		return
	}

	// Check organization policy before making further provider calls.
	corpID := strings.TrimSpace(userToken.CorpID)
	if !identity.DingTalkCorpAllowed(cfg, corpID) {
		// Keep the internal organization ID out of the redirect URL.
		RedirectOAuthError(c, frontendCallback, "corp_rejected", "", "")
		return
	}

	// The global unionID identifies the account and seeds its synthetic email; the nickname is app-specific.
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

	// Prefer the nickname returned by the OAuth user profile.
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
		// Cross-organization profiles can lack email; use a synthetic address for binding audit data.
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

	// Resolve an existing provider identity before considering account creation.
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

	// When email is optional, use a subject-derived synthetic address.
	if !cfg.RequireEmail {
		if signupBlocked {
			// Blocked signup proceeds to existing-account binding.
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

	// A missing required email needs completion, unless signup policy requires binding an existing account.
	if staff.Email == "" {
		completionResponse := map[string]any{
			"step":                      "email_completion",
			"requires_email_completion": true,
			"redirect":                  redirectTo,
		}
		if signupBlocked {
			// Skip email completion when registration is blocked and no exemption applies.
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

// BuildDingTalkAuthorizeURL builds the provider authorization URL with state.
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

// CompleteDingTalkOAuthRegistration validates the invitation and completes pending DingTalk signup.
// POST /api/v1/auth/oauth/dingtalk/complete-registration
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
	// Use the email local part when the provider supplied no username.
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
	if err := h.flow.Database.ApplyAdoptionAndConsume(c.Request.Context(), session, decision, user.ID); err != nil {
		RespondPendingOAuthBindingApplyError(c, err)
		return
	}
	// The created user ID permits profile synchronization.
	// Run it asynchronously so provider calls do not delay the token response.
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

// CreateDingTalkOAuthAccount creates an account from a pending DingTalk session.
// POST /api/v1/auth/oauth/dingtalk/create-account
func (h *DingTalkHandler) CreateDingTalkOAuthAccount(c *gin.Context) {
	h.CreatePendingAccountForProvider(c, "dingtalk")
}

// BindDingTalkOAuthLogin binds a pending DingTalk identity to an existing account.
// POST /api/v1/auth/oauth/dingtalk/bind-login
func (h *DingTalkHandler) BindDingTalkOAuthLogin(c *gin.Context) {
	h.BindPendingLoginForProvider(c, "dingtalk")
}

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

// loadDingTalkStaff requires successful user and profile lookups for internal_only.
// Other policies allow empty staff data after lookup errors; callers handle require_email afterward.
func loadDingTalkStaff(ctx context.Context, client identity.DingTalkOAuthClient, policy, unionID, corpID string) (*identity.DingTalkProfileSnapshot, string, error) {
	upstreamUserID, err := client.GetUserIdByUnionId(ctx, unionID)
	if err != nil {
		if policy == "internal_only" {
			return nil, "get_user_id", err
		}
		slog.Debug("dingtalk step3 fallback (none/cross-org)", "corp_id", corpID, "union_id", unionID, "err", err)
		return &identity.DingTalkProfileSnapshot{}, "", nil
	}
	staff, err := client.GetStaffInfoByUserId(ctx, upstreamUserID)
	if err != nil {
		if policy == "internal_only" {
			return nil, "get_staff_info", err
		}
		slog.Debug("dingtalk step4 fallback (none/cross-org)", "corp_id", corpID, "union_id", unionID, "err", err)
		return &identity.DingTalkProfileSnapshot{}, "", nil
	}
	return staff, "", nil
}
