package httpapi

import (
	"context"
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

// WeChat account identity prefers unionid while channel observations retain
// mode, application ID, and openid. Login callback checks state and browser
// proof before resolving current provider ownership, signed binding targets,
// or historical openid records; real local email completion stays in the
// pending flow.
//
// Mode selects open-platform QR authorization or an mp browser authorization
// endpoint. Missing mode is inferred from the request User-Agent, and callback
// URL construction prefers configured absolute API base data before raw
// request host/protocol headers. Existing-account login repairs runtime
// identity data before pending continuation. Legacy registration issues
// credentials, applies adoption, records login, and then consumes browser
// state as separate steps, so later failures can follow completed earlier
// work.

// WeChat cookie values, canonical and historical provider keys, intent labels,
// and frontend defaults retain global subject and channel context across
// browser authorization.
const (
	WechatOAuthCookiePath         = "/api/v1/auth/oauth/wechat" // WeChat browser cookie path
	WechatOAuthCookieMaxAgeSec    = 10 * 60                     // seconds
	WechatOAuthStateCookieName    = "wechat_oauth_state"        // WeChat authorization state cookie
	WechatOAuthRedirectCookieName = "wechat_oauth_redirect"     // WeChat local return cookie
	WechatOAuthIntentCookieName   = "wechat_oauth_intent"       // WeChat account intent cookie
	WechatOAuthModeCookieName     = "wechat_oauth_mode"         // selected WeChat channel mode
	WechatOAuthBindUserCookieName = "wechat_oauth_bind_user"    // signed target account cookie
	WechatOAuthDefaultRedirectTo  = "/dashboard"                // default local login destination
	WechatOAuthDefaultFrontendCB  = "/auth/wechat/callback"     // default WeChat browser callback
	WechatOAuthProviderKey        = "wechat-main"               // canonical WeChat identity namespace
	WechatOAuthLegacyProviderKey  = "wechat"                    // historical WeChat identity namespace

	WechatOAuthIntentLogin      = "login"                        // external account login intent
	WechatOAuthIntentBind       = "bind_current_user"            // authenticated account binding intent
	WechatOAuthIntentAdoptEmail = "adopt_existing_user_by_email" // existing-email account adoption intent
)

// WeChatHTTPOptions supplies mode configuration and the frontend callback
// reader for browser login.
type WeChatHTTPOptions struct {
	LoadConfig func(context.Context, string) (identity.WeChatOAuthOptions, error) // current mode configuration callback
	// FrontendCallback supplies callback text when configured, including blank
	// text. An absent function selects WechatOAuthDefaultFrontendCB in its reader.
	FrontendCallback func(context.Context) string // optional frontend callback reader
}

// WeChatHandler resolves WeChat subject and channel data into pending login
// and binding.
type WeChatHandler struct {
	*PendingHandler                            // shared browser pending authentication
	binding         *OAuthBindHandler          // signed current-account binding helper
	client          identity.WeChatOAuthClient // WeChat token and identity client
	wechatOptions   WeChatHTTPOptions          // retained WeChat HTTP configuration
}

// CompleteWeChatOAuthRequest supplies invitation and profile choices for
// legacy WeChat signup completion.
type CompleteWeChatOAuthRequest struct {
	InvitationCode string `json:"invitation_code" binding:"required"` // required registration invitation code
	AffCode        string `json:"aff_code,omitempty"`                 // optional signup affiliate code
	// AdoptDisplayName and AdoptAvatar keep omitted choices as nil and supplied
	// false values as refusals. The helper retains saved values for omitted
	// flags and otherwise defaults those flags to false before applying supplied
	// choices.
	AdoptDisplayName *bool `json:"adopt_display_name,omitempty"` // optional provider-name adoption choice
	AdoptAvatar      *bool `json:"adopt_avatar,omitempty"`       // optional provider-avatar adoption choice
}

// NewWeChatHandler retains shared pending and binding handlers, provider
// client, and HTTP options. Configure the client and mode reader for exposed
// routes; absent frontend callback reader selects the default callback.
func NewWeChatHandler(p *PendingHandler, b *OAuthBindHandler, client identity.WeChatOAuthClient, options WeChatHTTPOptions) *WeChatHandler {
	return &WeChatHandler{p, b, client, options}
}

// FrontendCallback reads the current optional frontend callback function when
// configured. Its returned text passes through directly, including blank text;
// only an absent reader selects WechatOAuthDefaultFrontendCB.
func (h *WeChatHandler) FrontendCallback(ctx context.Context) string {
	if h.wechatOptions.FrontendCallback != nil {
		return h.wechatOptions.FrontendCallback(ctx)
	}
	return WechatOAuthDefaultFrontendCB
}

// GetConfig validates and loads the selected open or mp mode, rejecting a
// disabled mode or unavailable reader. Configured redirect URI takes
// precedence over absolute URL reconstruction.
//
// The returned copy trims application credentials, selects the mode authorize
// endpoint, and supplies a default frontend callback when that field is blank.
// Redirect reconstruction joins the API base path or raw request host/protocol
// data. The caller configuration reader owns application-credential validity;
// this method requires a usable resulting redirect URI before returning
// provider options.
func (h *WeChatHandler) GetConfig(ctx context.Context, raw string, c *gin.Context) (identity.WeChatOAuthOptions, error) {
	mode, e := ResolveWeChatOAuthMode(raw, c)
	if e != nil {
		return identity.WeChatOAuthOptions{}, e
	}
	if h == nil || h.wechatOptions.LoadConfig == nil {
		return identity.WeChatOAuthOptions{}, infraerrors.ServiceUnavailable("CONFIG_NOT_READY", "wechat oauth settings service not ready")
	}
	cfg, e := h.wechatOptions.LoadConfig(ctx, mode)
	if e != nil {
		return identity.WeChatOAuthOptions{}, e
	}
	if (mode == "mp" && !cfg.MPEnabled) || (mode == "open" && !cfg.OpenEnabled) {
		return identity.WeChatOAuthOptions{}, infraerrors.NotFound("OAUTH_DISABLED", "wechat oauth is disabled")
	}
	cfg.Mode = mode
	cfg.AppID = strings.TrimSpace(cfg.AppID)
	cfg.AppSecret = strings.TrimSpace(cfg.AppSecret)
	cfg.RedirectURI = identity.OAuthFirstNonEmpty(strings.TrimSpace(cfg.RedirectURI), ResolveWeChatOAuthAbsoluteURL(cfg.APIBaseURL, c, "/api/v1/auth/oauth/wechat/callback"))
	cfg.FrontendCallback = identity.OAuthFirstNonEmpty(strings.TrimSpace(cfg.FrontendCallback), WechatOAuthDefaultFrontendCB)
	if mode == "mp" {
		cfg.AuthorizeURL = "https://open.weixin.qq.com/connect/oauth2/authorize"
	} else {
		cfg.AuthorizeURL = "https://open.weixin.qq.com/connect/qrconnect"
	}
	if strings.TrimSpace(cfg.RedirectURI) == "" {
		return identity.WeChatOAuthOptions{}, infraerrors.InternalServer("OAUTH_CONFIG_INVALID", "wechat oauth redirect url not configured")
	}
	return cfg, nil
}

// WeChatOAuthStart checks applicable CAPTCHA and captures mode, intent, state,
// and browser proof before returning authorization. Invalid local return paths
// select the dashboard, and binding intent signs the authenticated account
// target.
//
// Provider and pending cookies are written before later target resolution and
// authorize URL construction can fail. The old pending session cookie is
// expired while the new browser key is stored, keeping that start separate
// from an earlier pending session. POST returns its authorize URL envelope,
// while accepted navigation starts redirect to the selected mode endpoint.
func (h *WeChatHandler) WeChatOAuthStart(c *gin.Context) {
	if !h.RequireActionCaptchaForOAuthLoginStart(c) {
		return
	}
	cfg, err := h.GetConfig(c.Request.Context(), c.Query("mode"), c)
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
		redirectTo = WechatOAuthDefaultRedirectTo
	}

	browserSessionKey, err := GenerateOAuthPendingBrowserSession()
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_BROWSER_SESSION_GEN_FAILED", "failed to generate oauth browser session").WithCause(err))
		return
	}

	intent := NormalizeWeChatOAuthIntent(c.Query("intent"))
	secureCookie := IsRequestHTTPS(c)
	// Retain WeChat mode and intent beside fresh browser proof before target and
	// URL preparation. Later failure leaves these emitted headers after the
	// earlier pending token cookie has been expired.
	WeChatSetCookie(c, WechatOAuthStateCookieName, EncodeCookieValue(state), WechatOAuthCookieMaxAgeSec, secureCookie)
	WeChatSetCookie(c, WechatOAuthRedirectCookieName, EncodeCookieValue(redirectTo), WechatOAuthCookieMaxAgeSec, secureCookie)
	WeChatSetCookie(c, WechatOAuthIntentCookieName, EncodeCookieValue(intent), WechatOAuthCookieMaxAgeSec, secureCookie)
	WeChatSetCookie(c, WechatOAuthModeCookieName, EncodeCookieValue(cfg.Mode), WechatOAuthCookieMaxAgeSec, secureCookie)
	CaptureOAuthPromoCode(c, secureCookie)
	SetOAuthPendingBrowserCookie(c, browserSessionKey, secureCookie)
	ClearOAuthPendingSessionCookie(c, secureCookie)
	if intent == OauthIntentBindCurrentUser {
		bindCookieValue, err := h.binding.BuildOAuthBindUserCookieFromContext(c)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		WeChatSetCookie(c, WechatOAuthBindUserCookieName, EncodeCookieValue(bindCookieValue), WechatOAuthCookieMaxAgeSec, secureCookie)
	} else {
		WeChatClearCookie(c, WechatOAuthBindUserCookieName, secureCookie)
	}

	authURL, err := BuildWeChatAuthorizeURL(cfg, state)
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_BUILD_URL_FAILED", "failed to build oauth authorization url").WithCause(err))
		return
	}

	RespondOAuthStart(c, authURL)
}

// WeChatOAuthCallback verifies provider state and browser mode before fetching
// token and userinfo identity data. Required handler dependencies are supplied
// during assembly.
//
// Userinfo unionid/openid take precedence over token response values. A
// required unionid cannot fall back to openid; other configured cases use
// openid when unionid is missing. The stable subject and channel tuple enter
// signed-target binding or current ownership lookup, with historical openid
// lookup used when needed. Existing accounts receive runtime identity repair
// before pending login; remaining identities become account-choice drafts.
// Cleanup begins after required code/state input and expires provider cookies
// on later exits.
func (h *WeChatHandler) WeChatOAuthCallback(c *gin.Context) {
	frontendCallback := h.FrontendCallback(c.Request.Context())

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
	// After code/state presence is established, expire WeChat state, return,
	// mode, intent, target, and promotion cookies on later exits. Shared pending
	// browser proof remains for the selected continuation.
	defer func() {
		WeChatClearCookie(c, WechatOAuthStateCookieName, secureCookie)
		WeChatClearCookie(c, WechatOAuthRedirectCookieName, secureCookie)
		WeChatClearCookie(c, WechatOAuthIntentCookieName, secureCookie)
		WeChatClearCookie(c, WechatOAuthModeCookieName, secureCookie)
		WeChatClearCookie(c, WechatOAuthBindUserCookieName, secureCookie)
		ClearOAuthPromoCodeCookie(c, secureCookie)
	}()

	expectedState, err := ReadCookieDecoded(c, WechatOAuthStateCookieName)
	if err != nil || expectedState == "" || state != expectedState {
		RedirectOAuthError(c, frontendCallback, "invalid_state", "invalid oauth state", "")
		return
	}

	// An unreadable return cookie contributes empty text, and local-path
	// validation selects the dashboard destination. The required browser proof
	// below remains a separate continuation check.
	redirectTo, _ := ReadCookieDecoded(c, WechatOAuthRedirectCookieName)
	redirectTo = SanitizeFrontendRedirectPath(redirectTo)
	if redirectTo == "" {
		redirectTo = WechatOAuthDefaultRedirectTo
	}
	// Read failures produce empty browser proof, so missing or malformed cookies
	// are rejected before provider exchange and pending state creation.
	browserSessionKey, _ := ReadOAuthPendingBrowserCookie(c)
	if strings.TrimSpace(browserSessionKey) == "" {
		RedirectOAuthError(c, frontendCallback, "missing_browser_session", "missing oauth browser session", "")
		return
	}

	// An unreadable intent has no binding label. Normalization selects login, so
	// a leftover bind-target cookie cannot by itself select the current-user
	// binding branch.
	intent, _ := ReadCookieDecoded(c, WechatOAuthIntentCookieName)
	mode, err := ReadCookieDecoded(c, WechatOAuthModeCookieName)
	if err != nil || strings.TrimSpace(mode) == "" {
		RedirectOAuthError(c, frontendCallback, "invalid_state", "missing oauth mode", "")
		return
	}

	cfg, err := h.GetConfig(c.Request.Context(), mode, c)
	if err != nil {
		RedirectOAuthError(c, frontendCallback, "provider_error", infraerrors.Reason(err), infraerrors.Message(err))
		return
	}

	tokenResp, userInfo, err := h.client.FetchIdentity(c.Request.Context(), cfg, code)
	if err != nil {
		RedirectOAuthError(c, frontendCallback, "provider_error", "wechat_identity_fetch_failed", OAuthSingleLine(err.Error()))
		return
	}

	unionid := strings.TrimSpace(identity.OAuthFirstNonEmpty(userInfo.UnionID, tokenResp.UnionID))
	openid := strings.TrimSpace(identity.OAuthFirstNonEmpty(userInfo.OpenID, tokenResp.OpenID))
	// Keep the global unionid separate from the application-scoped openid. Only
	// the configured unionid requirement decides whether a missing global value
	// can use that channel subject.
	providerSubject := unionid
	if providerSubject == "" {
		if cfg.RequiresUnionID() {
			RedirectOAuthError(c, frontendCallback, "provider_error", "wechat_missing_unionid", "")
			return
		}
		providerSubject = openid
	}
	if providerSubject == "" {
		RedirectOAuthError(c, frontendCallback, "provider_error", "wechat_missing_unionid", "")
		return
	}

	username := identity.OAuthFirstNonEmpty(userInfo.Nickname, identity.WeChatFallbackUsername(providerSubject))
	email := identity.WeChatSyntheticEmail(providerSubject)
	upstreamClaims := map[string]any{
		"email":                  email,
		"username":               username,
		"subject":                providerSubject,
		"openid":                 openid,
		"unionid":                unionid,
		"mode":                   cfg.Mode,
		"channel":                cfg.Mode,
		"channel_app_id":         strings.TrimSpace(cfg.AppID),
		"channel_subject":        openid,
		"suggested_display_name": strings.TrimSpace(userInfo.Nickname),
		"suggested_avatar_url":   strings.TrimSpace(userInfo.HeadImgURL),
	}
	identityRef := identity.PendingAuthIdentityKey{
		ProviderType:    "wechat",
		ProviderKey:     WechatOAuthProviderKey,
		ProviderSubject: providerSubject,
	}

	normalizedIntent := NormalizeWeChatOAuthIntent(intent)
	if normalizedIntent == WechatOAuthIntentBind {
		if err := h.CreateWeChatBindSession(c, cfg, providerSubject, openid, redirectTo, browserSessionKey, upstreamClaims); err != nil {
			switch response.ErrorCode(err) {
			case http.StatusConflict:
				RedirectOAuthError(c, frontendCallback, "ownership_conflict", infraerrors.Reason(err), infraerrors.Message(err))
			case http.StatusUnauthorized, http.StatusForbidden:
				RedirectOAuthError(c, frontendCallback, "auth_required", infraerrors.Reason(err), infraerrors.Message(err))
			default:
				RedirectOAuthError(c, frontendCallback, "session_error", infraerrors.Reason(err), infraerrors.Message(err))
			}
			return
		}
		RedirectToFrontendCallback(c, frontendCallback)
		return
	}

	existingIdentityUser, err := h.flow.Database.FindOAuthIdentityUser(c.Request.Context(), identityRef)
	if err != nil {
		RedirectOAuthError(c, frontendCallback, "session_error", infraerrors.Reason(err), infraerrors.Message(err))
		return
	}
	// No canonical subject owner was resolved, so try complete channel and
	// legacy-openid forms. That compatibility lookup still rejects different
	// owners and checks the selected current account.
	if existingIdentityUser == nil {
		existingIdentityUser, err = h.flow.Database.FindWeChatUserByLegacyOpenID(c.Request.Context(), identityRef, identity.WeChatIdentityChannel{Mode: cfg.Mode, AppID: cfg.AppID}, openid)
		if err != nil {
			RedirectOAuthError(c, frontendCallback, "session_error", infraerrors.Reason(err), infraerrors.Message(err))
			return
		}
	}
	if existingIdentityUser != nil {
		if err := h.flow.Database.EnsureWeChatRuntimeIdentityBinding(c.Request.Context(), existingIdentityUser.ID, identityRef, upstreamClaims); err != nil {
			RedirectOAuthError(c, frontendCallback, "session_error", infraerrors.Reason(err), infraerrors.Message(err))
			return
		}
		if err := h.CreateWeChatSession(c, normalizedIntent, providerSubject, existingIdentityUser.Email, redirectTo, browserSessionKey, upstreamClaims, nil, nil, &existingIdentityUser.ID); err != nil {
			RedirectOAuthError(c, frontendCallback, "session_error", "failed to continue oauth login", "")
			return
		}
		RedirectToFrontendCallback(c, frontendCallback)
		return
	}

	if h.isForceEmailOnThirdPartySignup(c.Request.Context()) {
		if err := h.CreateWeChatChoiceSession(
			c,
			identityRef,
			email,
			email,
			redirectTo,
			browserSessionKey,
			upstreamClaims,
			"",
			nil,
			true,
		); err != nil {
			RedirectOAuthError(c, frontendCallback, "session_error", "failed to continue oauth login", "")
			return
		}
		RedirectToFrontendCallback(c, frontendCallback)
		return
	}

	if err := h.CreateWeChatChoiceSession(
		c,
		identityRef,
		email,
		email,
		redirectTo,
		browserSessionKey,
		upstreamClaims,
		"",
		nil,
		false,
	); err != nil {
		RedirectOAuthError(c, frontendCallback, "session_error", "failed to continue oauth login", "")
		return
	}
	RedirectToFrontendCallback(c, frontendCallback)
}

// CompleteWeChatOAuthRegistration reevaluates legacy pending email
// requirements and backend policy before invitation-based authentication.
// Account-choice progression can return before credentials are issued.
//
// For accepted context, authentication creates or resolves the account and its
// pair before saving profile choice and applying adoption. Login recording
// follows adoption, then browser consumption runs separately. Those later
// errors can follow issued credentials or saved identity changes. Successful
// completion expires both pending cookies and returns the legacy top-level
// token shape.
func (h *WeChatHandler) CompleteWeChatOAuthRegistration(c *gin.Context) {
	var req CompleteWeChatOAuthRequest
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
	if email == "" || username == "" {
		response.ErrorFrom(c, infraerrors.BadRequest("PENDING_AUTH_SESSION_INVALID", "pending auth registration context is invalid"))
		return
	}

	tokenPair, user, err := h.authService.LoginOrRegisterOAuthWithTokenPairAndPromoCode(c.Request.Context(), email, username, req.InvitationCode, req.AffCode, PendingOAuthPromoCode(session), "wechat")
	if err != nil {
		response.ErrorFrom(c, err)
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
	// Authentication already issued credentials. Adoption and browser consumption
	// run separately afterward, so a failed claim can leave the adopted identity
	// or profile saved before the response is withheld.
	if err := h.flow.Database.ApplyAdoption(c.Request.Context(), session, decision, &user.ID); err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("PENDING_AUTH_ADOPTION_APPLY_FAILED", "failed to apply oauth profile adoption").WithCause(err))
		return
	}
	h.authService.RecordSuccessfulLogin(c.Request.Context(), user.ID)
	if _, err := pendingSvc.ConsumeBrowserSession(c.Request.Context(), sessionToken, browserSessionKey); err != nil {
		ClearOAuthPendingSessionCookie(c, secureCookie)
		ClearOAuthPendingBrowserCookie(c, secureCookie)
		response.ErrorFrom(c, err)
		return
	}
	ClearOAuthPendingSessionCookie(c, secureCookie)
	ClearOAuthPendingBrowserCookie(c, secureCookie)

	c.JSON(http.StatusOK, gin.H{
		"access_token":  tokenPair.AccessToken,
		"refresh_token": tokenPair.RefreshToken,
		"expires_in":    tokenPair.ExpiresIn,
		"token_type":    "Bearer",
	})
}

// CreateWeChatBindSession verifies the signed current-account target and
// prechecks global and complete channel ownership. It then stores browser
// pending binding state; those prechecks do not replace later transactional
// ownership validation.
func (h *WeChatHandler) CreateWeChatBindSession(
	c *gin.Context,
	cfg identity.WeChatOAuthOptions,
	providerSubject string,
	channelSubject string,
	redirectTo string,
	browserSessionKey string,
	upstreamClaims map[string]any,
) error {
	currentUser, err := h.binding.ReadTargetUser(c, WechatOAuthBindUserCookieName, h.flow.Database)
	if err != nil {
		return err
	}
	if err := h.flow.Database.EnsureWeChatBindOwnership(c.Request.Context(), currentUser.ID, providerSubject, identity.WeChatIdentityChannel{Mode: cfg.Mode, AppID: cfg.AppID}, channelSubject); err != nil {
		return err
	}
	return h.CreateWeChatSession(
		c,
		WechatOAuthIntentBind,
		providerSubject,
		currentUser.Email,
		redirectTo,
		browserSessionKey,
		upstreamClaims,
		nil,
		nil,
		&currentUser.ID,
	)
}

// ResolveWeChatOAuthMode returns trimmed case-folded open or mp input,
// rejecting other modes. Empty input selects mp for a MicroMessenger
// User-Agent and open otherwise, using the request browser observation.
func ResolveWeChatOAuthMode(rawMode string, c *gin.Context) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(rawMode))
	if mode == "" {
		if IsWeChatBrowserRequest(c) {
			return "mp", nil
		}
		return "open", nil
	}
	if mode != "open" && mode != "mp" {
		return "", infraerrors.BadRequest("INVALID_MODE", "wechat oauth mode must be open or mp")
	}
	return mode, nil
}

// IsWeChatBrowserRequest reports a case-insensitive MicroMessenger substring
// in the request User-Agent. Nil context or request returns false; this
// browser hint is used for default mode selection rather than account
// authentication.
func IsWeChatBrowserRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(c.GetHeader("User-Agent"))), "micromessenger")
}

// NormalizeWeChatOAuthIntent returns login, current-user bind, or
// existing-email adoption from trimmed aliases. Empty and unknown inputs
// select login, leaving ownership and local account proof to the pending flow.
func NormalizeWeChatOAuthIntent(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "login":
		return WechatOAuthIntentLogin
	case "bind", "bind_current_user":
		return WechatOAuthIntentBind
	case "adopt", "adopt_existing_user_by_email":
		return WechatOAuthIntentAdoptEmail
	default:
		return WechatOAuthIntentLogin
	}
}

// BuildWeChatAuthorizeURL encodes app, callback, code response type, scope,
// and state into the selected mode URL. Existing unrelated query values
// remain, and the required wechat_redirect fragment replaces any configured
// fragment.
func BuildWeChatAuthorizeURL(cfg identity.WeChatOAuthOptions, state string) (string, error) {
	u, err := url.Parse(cfg.AuthorizeURL)
	if err != nil {
		return "", fmt.Errorf("parse authorize url: %w", err)
	}
	query := u.Query()
	query.Set("appid", cfg.AppID)
	query.Set("redirect_uri", cfg.RedirectURI)
	query.Set("response_type", "code")
	query.Set("scope", cfg.Scope)
	query.Set("state", state)
	u.RawQuery = query.Encode()
	u.Fragment = "wechat_redirect"
	return u.String(), nil
}

// ResolveWeChatOAuthAbsoluteURL joins a callback path to configured absolute
// API base data or request host/protocol observations. Empty callback path or
// unavailable host returns empty text.
//
// A configured base needs a scheme and host; its escaped path is retained,
// with overlapping api/v1 segments joined once and query/fragment data
// omitted. Otherwise raw X-Forwarded-Host overrides request Host, and
// IsRequestHTTPS selects the scheme. These headers are not locally checked
// against proxy trust, so callers supply the deployment header policy around
// request-based reconstruction.
func ResolveWeChatOAuthAbsoluteURL(apiBaseURL string, c *gin.Context, callbackPath string) string {
	callbackPath = strings.TrimSpace(callbackPath)
	if callbackPath == "" {
		return ""
	}

	if raw := strings.TrimSpace(apiBaseURL); raw != "" {
		if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			basePath := strings.TrimRight(parsed.EscapedPath(), "/")
			targetPath := callbackPath
			// Join an API base already ending in api/v1 with the callback suffix once.
			// Retain the escaped configured prefix so percent-encoded path data
			// survives reconstruction.
			if basePath != "" && strings.HasSuffix(basePath, "/api/v1") && strings.HasPrefix(callbackPath, "/api/v1") {
				targetPath = basePath + strings.TrimPrefix(callbackPath, "/api/v1")
			} else if basePath != "" {
				targetPath = basePath + callbackPath
			}
			return parsed.Scheme + "://" + parsed.Host + targetPath
		}
	}

	// An absent, unparsable, or nonabsolute API base leaves callback
	// reconstruction to request host and HTTPS observations. Forwarding-header
	// values are accepted directly on this fallback path.
	if c == nil || c.Request == nil {
		return ""
	}
	scheme := "http"
	if IsRequestHTTPS(c) {
		scheme = "https"
	}
	host := strings.TrimSpace(c.Request.Host)
	if forwardedHost := strings.TrimSpace(c.GetHeader("X-Forwarded-Host")); forwardedHost != "" {
		host = forwardedHost
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host + callbackPath
}

// WeChatSetCookie emits the supplied encoded value at the WeChat browser path
// with a seconds-based lifetime. The caller supplies HTTPS state; HttpOnly and
// SameSite Lax support provider callback navigation.
func WeChatSetCookie(c *gin.Context, name string, value string, maxAgeSec int, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     WechatOAuthCookiePath,
		MaxAge:   maxAgeSec,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// WeChatClearCookie expires a named WeChat browser value at its matching path.
// The caller supplies HTTPS state, while HttpOnly and SameSite Lax match the
// cookie emitted for callback continuation.
func WeChatClearCookie(c *gin.Context, name string, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     WechatOAuthCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// CreateWeChatSession asks identity rules to prepare a subject and
// channel-aware pending draft, then stores it. Input authentication failure
// can remain an error from draft preparation before browser pending
// persistence.
func (h *WeChatHandler) CreateWeChatSession(
	c *gin.Context,
	intent string,
	providerSubject string,
	email string,
	redirectTo string,
	browserSessionKey string,
	upstreamClaims map[string]any,
	tokenPair *identity.TokenPair,
	authErr error,
	targetUserID *int64,
) error {
	draft, err := identity.PrepareWeChatPending(intent, providerSubject, email, redirectTo, browserSessionKey, upstreamClaims, tokenPair, authErr, targetUserID)
	if err != nil {
		return err
	}
	return h.CreateOAuthPendingSession(c, draft)
}

// CreateWeChatChoiceSession prepares account-choice state from provider and
// local email observations, then stores it. Identity rules select the
// completion controls, and persistence errors remain visible to callback
// callers.
func (h *WeChatHandler) CreateWeChatChoiceSession(
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
) error {
	draft := identity.PrepareWeChatChoice(identityKey, suggestedEmail, resolvedEmail, redirectTo, browserSessionKey, upstreamClaims, compatEmail, compatEmailUser, forceEmailOnSignup)
	return h.CreateOAuthPendingSession(c, draft)
}
