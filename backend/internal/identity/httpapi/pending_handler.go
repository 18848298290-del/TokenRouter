package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/oauthpkce"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// OAuth callbacks can continue in a browser-bound pending session while the
// user supplies account proof or profile-adoption choices. Shared cookies
// carry the pending session token and a separate browser key; completion
// helpers read those values before selecting signup, existing-account binding,
// or a final login response.
//
// A pending target can originate from an email hint, so exchange authorizes
// token issuance or current-user binding before applying adoption. Binding
// login, new-account finalization, and exchange use different mutation and
// consumption orders. Their errors can follow earlier completed writes or
// issued credentials; handlers expire browser state at the stages shown here,
// while the identity flow and PostgreSQL adapters own transaction
// participation and registration compensation.

// Pending labels and cookie values identify browser proof, account-choice
// progression, promotion context, and local redirect limits used by the shared
// OAuth continuation handlers.
const (
	oauthIntentBindCurrentUser    = "bind_current_user" // authenticated account binding intent
	linuxDoOAuthDefaultRedirectTo = "/dashboard"        // default local login destination
	linuxDoOAuthMaxRedirectLen    = 2048                // bytes

	OauthPendingBrowserCookiePath = "/api/v1/auth/oauth"            // browser proof cookie path
	OauthPendingBrowserCookieName = "oauth_pending_browser_session" // browser proof cookie name
	OauthPendingSessionCookiePath = "/api/v1/auth/oauth"            // pending credential cookie path
	OauthPendingSessionCookieName = "oauth_pending_session"         // pending credential cookie name
	OauthPromoCodeCookieName      = "oauth_promo_code"              // signup promotion cookie name
	OauthPendingCookieMaxAgeSec   = 10 * 60                         // seconds
	OauthPendingChoiceStep        = identity.OAuthPendingChoiceStep // account-choice frontend step

	OauthCompletionResponseKey = identity.OAuthCompletionResponseKey // stored completion payload key
	OauthPromoCodeStateKey     = identity.OAuthPromoCodeStateKey     // stored signup promotion key
)

// PendingHTTPOptions supplies optional completion observers, email policy, and
// finalization hook callbacks.
type PendingHTTPOptions struct {
	// AfterLogin runs when configured after existing-account binding and login
	// recording. It can observe an account ID before later pair issuance or
	// pending consumption fails.
	AfterLogin func(context.Context, *identity.PendingAuthSession, int64) // optional completed binding observer
	// AfterRegistration runs when configured after successful account
	// finalization, promotion application, and login recording. Its account ID is
	// available before browser cookie cleanup and response writing.
	AfterRegistration func(context.Context, *identity.PendingAuthSession, int64) // optional completed registration observer

	// ForceEmailOnSignup supplies the current signup-email decision when
	// configured. A missing callback disables this additional requirement in the
	// HTTP pending handlers.
	ForceEmailOnSignup func(context.Context) bool // optional required-signup-email decision
	// BeforeAccountCommit runs when configured inside account finalization with
	// its Ent transaction context. Returning an error stops that finalization
	// before commit and reaches flow compensation.
	BeforeAccountCommit func(context.Context, *identity.PendingAuthSession) error // optional transaction finalization hook
}

// PendingHandler serves browser-pending account proof, profile choice, and
// login completion requests.
type PendingHandler struct {
	*SessionHandler                       // shared account session HTTP operations
	flow            *identity.PendingFlow // browser pending identity use cases
	pendingOptions  PendingHTTPOptions    // retained HTTP completion callbacks
}

// OauthPendingSessionPayload aliases the prepared identity draft used for
// browser pending persistence.
type OauthPendingSessionPayload = identity.OAuthPendingDraft

// OauthAdoptionDecisionRequest retains supplied profile-adoption choices
// separately from omitted fields.
type OauthAdoptionDecisionRequest struct {
	// AdoptDisplayName and AdoptAvatar use nil for omissions and false for
	// refusals. AdoptionDecision retains saved values for omitted flags and
	// otherwise defaults those flags to false before applying supplied choices.
	AdoptDisplayName *bool `json:"adopt_display_name,omitempty"` // optional provider-name adoption choice
	AdoptAvatar      *bool `json:"adopt_avatar,omitempty"`       // optional provider-avatar adoption choice
}

// BindPendingOAuthLoginRequest supplies existing-account credentials and
// optional profile-adoption choices.
type BindPendingOAuthLoginRequest struct {
	Email    string `json:"email" binding:"required,email"` // submitted local account address
	Password string `json:"password" binding:"required"`    // submitted local account password
	// AdoptDisplayName and AdoptAvatar use nil for omissions and false for
	// refusals. AdoptionDecision retains saved values for omitted flags and
	// otherwise defaults those flags to false before applying supplied choices.
	AdoptDisplayName *bool `json:"adopt_display_name,omitempty"` // optional provider-name adoption choice
	AdoptAvatar      *bool `json:"adopt_avatar,omitempty"`       // optional provider-avatar adoption choice
}

// CreatePendingOAuthAccountRequest supplies local registration credentials,
// proof, and profile choices.
type CreatePendingOAuthAccountRequest struct {
	Email                 string `json:"email" binding:"required,email"`    // submitted local account address
	VerifyCode            string `json:"verify_code,omitempty"`             // signup email verification code
	Password              string `json:"password" binding:"required,min=6"` // submitted local account password
	TurnstileToken        string `json:"turnstile_token,omitempty"`         // shared enabled CAPTCHA proof
	TencentCaptchaTicket  string `json:"tencent_captcha_ticket,omitempty"`  // Tencent action ticket text
	TencentCaptchaRandstr string `json:"tencent_captcha_randstr,omitempty"` // Tencent ticket random string
	InvitationCode        string `json:"invitation_code,omitempty"`         // optional registration invitation code
	AffCode               string `json:"aff_code,omitempty"`                // optional signup affiliate code
	// AdoptDisplayName and AdoptAvatar use nil for omissions and false for
	// refusals. AdoptionDecision retains saved values for omitted flags and
	// otherwise defaults those flags to false before applying supplied choices.
	AdoptDisplayName *bool `json:"adopt_display_name,omitempty"` // optional provider-name adoption choice
	AdoptAvatar      *bool `json:"adopt_avatar,omitempty"`       // optional provider-avatar adoption choice
}

// SendPendingOAuthVerifyCodeRequest supplies signup address and enabled
// CAPTCHA proof before pending email completion.
type SendPendingOAuthVerifyCodeRequest struct {
	Email                 string `json:"email" binding:"required,email"`    // submitted local account address
	TurnstileToken        string `json:"turnstile_token,omitempty"`         // shared enabled CAPTCHA proof
	TencentCaptchaTicket  string `json:"tencent_captcha_ticket,omitempty"`  // Tencent action ticket text
	TencentCaptchaRandstr string `json:"tencent_captcha_randstr,omitempty"` // Tencent ticket random string
	// PendingAuthToken and PendingOAuthToken are accepted body compatibility
	// fields. SendPendingOAuthVerifyCode obtains pending proof from browser
	// cookies, so these values do not select the stored session.
	PendingAuthToken  string `json:"pending_auth_token,omitempty"`  // compatibility body pending token
	PendingOAuthToken string `json:"pending_oauth_token,omitempty"` // compatibility body OAuth token
}

// NewPendingHandler retains shared session handlers, pending use cases, and
// optional HTTP completion callbacks.
func NewPendingHandler(session *SessionHandler, flow *identity.PendingFlow, options PendingHTTPOptions) *PendingHandler {
	return &PendingHandler{session, flow, options}
}

// pendingFlow returns the retained identity use cases for browser pending
// workflows.
func (h *PendingHandler) pendingFlow() *identity.PendingFlow { return h.flow }

// pendingIdentityService returns the configured store when the retained flow
// is available. Unavailable flow returns service unavailable; callers retain
// store lifecycle and browser-proof error handling for each continuation.
func (h *PendingHandler) pendingIdentityService() (identity.PendingStore, error) {
	if !h.flow.Available() {
		return nil, infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")
	}
	return h.flow.Store, nil
}

// isForceEmailOnThirdPartySignup reads the optional signup-email policy for
// this operation. Missing callbacks return false; configured callbacks receive
// the caller context and determine whether account choice requires local
// email.
func (h *PendingHandler) isForceEmailOnThirdPartySignup(ctx context.Context) bool {
	return h.pendingOptions.ForceEmailOnSignup != nil && h.pendingOptions.ForceEmailOnSignup(ctx)
}

// ensurePendingOAuthAdoptionDecision loads and merges profile flags with
// ensure enabled. Omitted flags retain saved values; with no saved decision or
// supplied flags, the flow creates false/false defaults, while storage failures
// remain completion errors.
func (h *PendingHandler) ensurePendingOAuthAdoptionDecision(c *gin.Context, id int64, r OauthAdoptionDecisionRequest) (*identity.IdentityAdoptionDecision, error) {
	return h.flow.AdoptionDecision(c.Request.Context(), id, identity.OAuthAdoptionChoice{AdoptDisplayName: r.AdoptDisplayName, AdoptAvatar: r.AdoptAvatar}, true)
}

// upsertPendingOAuthAdoptionDecision asks the flow to save optional profile
// choices for the pending ID. Omitted choice is allowed by this request;
// persistence and existing-decision handling belong to the flow.
func (h *PendingHandler) upsertPendingOAuthAdoptionDecision(c *gin.Context, id int64, r OauthAdoptionDecisionRequest) (*identity.IdentityAdoptionDecision, error) {
	return h.flow.AdoptionDecision(c.Request.Context(), id, identity.OAuthAdoptionChoice{AdoptDisplayName: r.AdoptDisplayName, AdoptAvatar: r.AdoptAvatar}, false)
}

// transitionPendingOAuthAccountToChoiceState delegates account-choice
// progression for an existing user and pending session. The flow prepares and
// saves the target and completion response; failures remain errors for the
// HTTP continuation.
func (h *PendingHandler) transitionPendingOAuthAccountToChoiceState(c *gin.Context, p *identity.PendingAuthSession, u *identity.User, email string) (*identity.PendingAuthSession, error) {
	return h.flow.TransitionAccountToChoice(c.Request.Context(), p, u, email)
}

// shouldSkipPendingOAuthAdoptionPrompt delegates whether the stored provider
// identity permits skipping profile choice. The flow interprets intent and
// existing binding, while errors stop the surrounding exchange decision.
func (h *PendingHandler) shouldSkipPendingOAuthAdoptionPrompt(ctx context.Context, p *identity.PendingAuthSession, state map[string]any) (bool, error) {
	return h.flow.SkipAdoptionPrompt(ctx, p, state)
}

// adoptionDecision returns the supplied profile-choice pointers for the
// pending login request.
func (r BindPendingOAuthLoginRequest) adoptionDecision() OauthAdoptionDecisionRequest {
	return OauthAdoptionDecisionRequest{
		AdoptDisplayName: r.AdoptDisplayName,
		AdoptAvatar:      r.AdoptAvatar,
	}
}

// GenerateOAuthPendingBrowserSession returns a forty-three-character unpadded
// base64url browser proof through the shared PKCE verifier generator. A
// returned proof has nil error. An io.ReadFull error terminates the process
// inside crypto/rand.Read during verifier generation.
func GenerateOAuthPendingBrowserSession() (string, error) {
	return oauthpkce.Verifier()
}

// SetOAuthPendingBrowserCookie emits the encoded pending browser value as a
// ten-minute HttpOnly cookie. It uses the shared OAuth path and SameSite Lax;
// the caller supplies the HTTPS decision.
func SetOAuthPendingBrowserCookie(c *gin.Context, sessionKey string, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     OauthPendingBrowserCookieName,
		Value:    EncodeCookieValue(sessionKey),
		Path:     OauthPendingBrowserCookiePath,
		MaxAge:   OauthPendingCookieMaxAgeSec,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearOAuthPendingBrowserCookie emits a deletion header for its shared OAuth
// cookie. Matching path, HttpOnly, and SameSite Lax attributes accompany the
// caller HTTPS decision so browser continuation cleanup reaches that value.
func ClearOAuthPendingBrowserCookie(c *gin.Context, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     OauthPendingBrowserCookieName,
		Value:    "",
		Path:     OauthPendingBrowserCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ReadOAuthPendingBrowserCookie decodes the pending browser proof using shared
// cookie encoding. Missing cookies and invalid base64 values return their read
// or decoding errors to continuation callers.
func ReadOAuthPendingBrowserCookie(c *gin.Context) (string, error) {
	return ReadCookieDecoded(c, OauthPendingBrowserCookieName)
}

// SetOAuthPendingSessionCookie emits the encoded pending session value as a
// ten-minute HttpOnly cookie. It uses the shared OAuth path and SameSite Lax;
// the caller supplies the HTTPS decision.
func SetOAuthPendingSessionCookie(c *gin.Context, sessionToken string, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     OauthPendingSessionCookieName,
		Value:    EncodeCookieValue(sessionToken),
		Path:     OauthPendingSessionCookiePath,
		MaxAge:   OauthPendingCookieMaxAgeSec,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearOAuthPendingSessionCookie emits a deletion header for its shared OAuth
// cookie. Matching path, HttpOnly, and SameSite Lax attributes accompany the
// caller HTTPS decision so browser continuation cleanup reaches that value.
func ClearOAuthPendingSessionCookie(c *gin.Context, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     OauthPendingSessionCookieName,
		Value:    "",
		Path:     OauthPendingSessionCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ReadOAuthPendingSessionCookie decodes the stored pending session token using
// shared cookie encoding. Missing cookies and invalid base64 values return
// their read or decoding errors to continuation callers.
func ReadOAuthPendingSessionCookie(c *gin.Context) (string, error) {
	return ReadCookieDecoded(c, OauthPendingSessionCookieName)
}

// CaptureOAuthPromoCode stores a trimmed query promotion value in a
// short-lived HttpOnly cookie. Missing query promotion expires any preceding
// value at the shared OAuth path before the provider flow continues.
func CaptureOAuthPromoCode(c *gin.Context, secure bool) {
	promoCode := strings.TrimSpace(c.Query("promo_code"))
	if promoCode == "" {
		ClearOAuthPromoCodeCookie(c, secure)
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     OauthPromoCodeCookieName,
		Value:    EncodeCookieValue(promoCode),
		Path:     OauthPendingBrowserCookiePath,
		MaxAge:   OauthPendingCookieMaxAgeSec,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearOAuthPromoCodeCookie emits a deletion header for its shared OAuth
// cookie. Matching path, HttpOnly, and SameSite Lax attributes accompany the
// caller HTTPS decision so browser continuation cleanup reaches that value.
func ClearOAuthPromoCodeCookie(c *gin.Context, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     OauthPromoCodeCookieName,
		Value:    "",
		Path:     OauthPendingBrowserCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ReadOAuthPromoCode returns trimmed promotion text decoded from the browser
// cookie. Nil contexts, missing cookies, and decode failures return empty
// text; promotion validation remains with the signup service.
func ReadOAuthPromoCode(c *gin.Context) string {
	if c == nil {
		return ""
	}
	promoCode, err := ReadCookieDecoded(c, OauthPromoCodeCookieName)
	// Unreadable promotion cookies contribute no signup code. Continue the
	// provider flow with empty promotion context, leaving registration
	// availability independent of that optional cookie.
	if err != nil {
		return ""
	}
	return strings.TrimSpace(promoCode)
}

// PendingOAuthPromoCode delegates reading the trimmed signup promotion from
// local pending state. Nil sessions, missing keys, and unsupported value types
// return empty text; promotion validation remains with the signup service.
func PendingOAuthPromoCode(session *identity.PendingAuthSession) string {
	return identity.PendingOAuthPromoCode(session)
}

// RedirectToFrontendCallback redirects to the supplied callback with its
// fragment cleared and successful-path cache prevention headers. Parse
// failures or non-HTTP schemes choose the local dashboard; relative and
// HTTP(S) callbacks remain allowed.
func RedirectToFrontendCallback(c *gin.Context, frontendCallback string) {
	u, err := url.Parse(frontendCallback)
	// Invalid callback text cannot carry this continuation response. Choose the
	// local dashboard before applying the accepted callback's fragment and cache
	// headers.
	if err != nil {
		c.Redirect(http.StatusFound, linuxDoOAuthDefaultRedirectTo)
		return
	}
	// Another parsed scheme is also outside callback navigation. Select the same
	// local destination, leaving successful-path cache headers for accepted URLs.
	if u.Scheme != "" && !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		c.Redirect(http.StatusFound, linuxDoOAuthDefaultRedirectTo)
		return
	}
	u.Fragment = ""
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Redirect(http.StatusFound, u.String())
}

// CreateOAuthPendingSession stores a provider draft and emits its browser
// session cookie after persistence succeeds. Draft promotion takes precedence
// over the saved browser promotion, and completion data is retained in local
// flow state for later account-choice responses.
//
// The supplied provider tuple, target, browser proof, and redirect become one
// pending record. Store failure logs its context and returns an application
// error before this helper emits the session cookie; any browser cookie
// already emitted by the caller remains present.
func (h *PendingHandler) CreateOAuthPendingSession(c *gin.Context, payload OauthPendingSessionPayload) error {
	svc, err := h.pendingIdentityService()
	if err != nil {
		return err
	}

	localFlowState := map[string]any{
		OauthCompletionResponseKey: payload.CompletionResponse,
	}
	if promoCode := strings.TrimSpace(identity.OAuthFirstNonEmpty(payload.PromoCode, ReadOAuthPromoCode(c))); promoCode != "" {
		localFlowState[OauthPromoCodeStateKey] = promoCode
	}

	// Persist the prepared provider and browser tuple before publishing its
	// session token cookie. A failed store therefore cannot expose an unstored
	// session from this helper.
	session, err := svc.CreatePendingSession(c.Request.Context(), identity.CreatePendingAuthSessionInput{
		Intent:                 strings.TrimSpace(payload.Intent),
		Identity:               payload.Identity,
		TargetUserID:           payload.TargetUserID,
		ResolvedEmail:          strings.TrimSpace(payload.ResolvedEmail),
		RedirectTo:             strings.TrimSpace(payload.RedirectTo),
		BrowserSessionKey:      strings.TrimSpace(payload.BrowserSessionKey),
		UpstreamIdentityClaims: payload.UpstreamIdentityClaims,
		LocalFlowState:         localFlowState,
	})
	if err != nil {
		slog.Error("pending auth session create failed",
			"intent", strings.TrimSpace(payload.Intent),
			"provider_type", strings.TrimSpace(payload.Identity.ProviderType),
			"provider_key", strings.TrimSpace(payload.Identity.ProviderKey),
			"provider_subject_len", len(strings.TrimSpace(payload.Identity.ProviderSubject)),
			"resolved_email_len", len(strings.TrimSpace(payload.ResolvedEmail)),
			"has_target_user", payload.TargetUserID != nil,
			"error", err.Error())
		return infraerrors.InternalServer("PENDING_AUTH_SESSION_CREATE_FAILED", "failed to create pending auth session").WithCause(err)
	}

	SetOAuthPendingSessionCookie(c, session.SessionToken, IsRequestHTTPS(c))
	return nil
}

// ReadCompletionResponse delegates the typed completion-map read and returns
// its shared map result. Missing or unsupported values report false; a typed
// nil map can still report that the expected type exists.
func ReadCompletionResponse(session map[string]any) (map[string]any, bool) {
	return identity.ReadCompletionResponse(session)
}

// MergePendingCompletionResponse delegates outer-map merging of stored
// completion, redirect, overrides, and profile hints. Nil override values
// remove keys, and nested values remain shared according to the identity
// helper.
func MergePendingCompletionResponse(session *identity.PendingAuthSession, overrides map[string]any) map[string]any {
	return identity.MergePendingCompletionResponse(session, overrides)
}

// PendingSessionStringValue delegates trimmed string lookup from a stored
// pending map. Missing keys and nonstring values yield empty text, leaving
// later step checks to their caller.
func PendingSessionStringValue(values map[string]any, key string) string {
	return identity.PendingSessionStringValue(values, key)
}

// PendingSessionWantsInvitation delegates recognition of the stored
// invitation-required completion error label.
func PendingSessionWantsInvitation(payload map[string]any) bool {
	return identity.PendingSessionWantsInvitation(payload)
}

// PendingSessionRequiresEmailCompletion delegates recognition of the stored
// email-completion flag or step.
func PendingSessionRequiresEmailCompletion(payload map[string]any) bool {
	return identity.PendingSessionRequiresEmailCompletion(payload)
}

// PendingSessionRequiresBindLogin delegates recognition of the stored
// existing-account binding-login step.
func PendingSessionRequiresBindLogin(payload map[string]any) bool {
	return identity.PendingSessionRequiresBindLogin(payload)
}

// PendingOAuthCompletionCanIssueTokenPair delegates login completion
// eligibility from intent, positive target, invitation, and step. This
// selection leaves account activity, browser lifecycle, and actual credential
// issuance to the surrounding exchange.
func PendingOAuthCompletionCanIssueTokenPair(session *identity.PendingAuthSession, payload map[string]any) bool {
	return identity.PendingOAuthCompletionCanIssueTokenPair(session, payload)
}

// EnsurePendingOAuthCompleteRegistrationSession delegates pending registration
// intent and target validation. It rejects current-user targets and
// binding-login state, while browser expiry, proof, and registration policy
// remain separate checks.
func EnsurePendingOAuthCompleteRegistrationSession(session *identity.PendingAuthSession) error {
	return identity.EnsurePendingOAuthCompleteRegistrationSession(session)
}

// hasDecision reports whether either profile-adoption flag was supplied in
// this request.
func (r OauthAdoptionDecisionRequest) hasDecision() bool {
	return r.AdoptDisplayName != nil || r.AdoptAvatar != nil
}

// BindOptionalOAuthAdoptionDecision decodes profile choices when an HTTP body
// is present. Missing body and EOF return omitted choices with no error; other
// decoding failures remain errors for exchange.
func BindOptionalOAuthAdoptionDecision(c *gin.Context) (OauthAdoptionDecisionRequest, error) {
	var req OauthAdoptionDecisionRequest
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return req, nil
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		if errors.Is(err, io.EOF) {
			// An empty optional body supplies no adoption choice. Continue exchange
			// with that absence so stored prompt requirements can decide its next
			// browser step.
			return req, nil
		}
		return req, err
	}
	return req, nil
}

// BindLinuxDoOAuthLogin delegates browser-pending existing-account binding for
// LinuxDo identity state.
func (h *PendingHandler) BindLinuxDoOAuthLogin(c *gin.Context) {
	h.BindPendingLoginForProvider(c, "linuxdo")
}

// BindOIDCOAuthLogin delegates browser-pending existing-account binding for
// OIDC identity state.
func (h *PendingHandler) BindOIDCOAuthLogin(c *gin.Context) { h.BindPendingLoginForProvider(c, "oidc") }

// BindWeChatOAuthLogin delegates browser-pending existing-account binding for
// WeChat identity state.
func (h *PendingHandler) BindWeChatOAuthLogin(c *gin.Context) {
	h.BindPendingLoginForProvider(c, "wechat")
}

// BindPendingOAuthLogin delegates browser-pending existing-account binding for
// the stored provider identity state.
func (h *PendingHandler) BindPendingOAuthLogin(c *gin.Context) { h.BindPendingLoginForProvider(c, "") }

// CreateLinuxDoOAuthAccount delegates browser-pending local-account
// registration for LinuxDo identity state.
func (h *PendingHandler) CreateLinuxDoOAuthAccount(c *gin.Context) {
	h.CreatePendingAccountForProvider(c, "linuxdo")
}

// CreateOIDCOAuthAccount delegates browser-pending local-account registration
// for OIDC identity state.
func (h *PendingHandler) CreateOIDCOAuthAccount(c *gin.Context) {
	h.CreatePendingAccountForProvider(c, "oidc")
}

// CreateWeChatOAuthAccount delegates browser-pending local-account
// registration for WeChat identity state.
func (h *PendingHandler) CreateWeChatOAuthAccount(c *gin.Context) {
	h.CreatePendingAccountForProvider(c, "wechat")
}

// CreatePendingOAuthAccount delegates browser-pending local-account
// registration for the stored provider identity state.
func (h *PendingHandler) CreatePendingOAuthAccount(c *gin.Context) {
	h.CreatePendingAccountForProvider(c, "")
}

// SendPendingOAuthVerifyCode verifies CAPTCHA and browser pending registration
// state before requesting signup mail. A matching existing account instead
// saves account-choice progression and returns its status, leaving email
// ownership proof to that flow.
func (h *PendingHandler) SendPendingOAuthVerifyCode(c *gin.Context) {
	var req SendPendingOAuthVerifyCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	proof := captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr)
	if err := h.authService.VerifyCaptcha(c.Request.Context(), proof, clientip.GetClientIP(c)); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	_, session, _, err := ReadPendingOAuthBrowserSession(c, h)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if err := EnsurePendingOAuthCompleteRegistrationSession(session); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	if !h.flow.Available() {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready"))
		return
	}

	email := strings.TrimSpace(strings.ToLower(req.Email))
	if existingUser, err := h.flow.Database.FindUserByNormalizedEmail(c.Request.Context(), email); err == nil && existingUser != nil {
		session, err = h.transitionPendingOAuthAccountToChoiceState(c, session, existingUser, email)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		c.JSON(http.StatusOK, BuildPendingOAuthSessionStatusPayload(session))
		return
	} else if err != nil && !errors.Is(err, identity.ErrUserNotFound) {
		response.ErrorFrom(c, err)
		return
	}

	result, err := h.authService.SendPendingOAuthVerifyCode(c.Request.Context(), req.Email, c.GetHeader("Accept-Language"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, SendVerifyCodeResponse{
		Message:   "Verification code sent successfully",
		Countdown: result.Countdown,
	})
}

// ApplySuggestedProfileToCompletionResponse delegates provider hint insertion
// into the caller-owned completion map. Usable hints fill absent suggestion
// fields and can request adoption; the caller retains exclusive mutation
// access.
func ApplySuggestedProfileToCompletionResponse(payload map[string]any, upstream map[string]any) {
	identity.ApplySuggestedProfileToCompletionResponse(payload, upstream)
}

// ReadPendingOAuthBrowserSession validates both browser cookies and reads
// usable pending state, returning its store and cleanup callback. Every failed
// proof or store read expires both cookies; success leaves cleanup for the
// completion caller.
func ReadPendingOAuthBrowserSession(c *gin.Context, h *PendingHandler) (identity.PendingStore, *identity.PendingAuthSession, func(), error) {
	secureCookie := IsRequestHTTPS(c)
	clearCookies := func() {
		ClearOAuthPendingSessionCookie(c, secureCookie)
		ClearOAuthPendingBrowserCookie(c, secureCookie)
	}

	sessionToken, err := ReadOAuthPendingSessionCookie(c)
	if err != nil || strings.TrimSpace(sessionToken) == "" {
		clearCookies()
		return nil, nil, clearCookies, identity.ErrPendingAuthSessionNotFound
	}
	browserSessionKey, err := ReadOAuthPendingBrowserCookie(c)
	if err != nil || strings.TrimSpace(browserSessionKey) == "" {
		clearCookies()
		return nil, nil, clearCookies, identity.ErrPendingAuthBrowserMismatch
	}

	svc, err := h.pendingIdentityService()
	if err != nil {
		clearCookies()
		return nil, nil, clearCookies, err
	}

	session, err := svc.GetBrowserSession(c.Request.Context(), sessionToken, browserSessionKey)
	if err != nil {
		clearCookies()
		return nil, nil, clearCookies, err
	}

	return svc, session, clearCookies, nil
}

// ConsumePendingOAuthSessionOnLogout attempts browser-session consumption when
// both proof cookies and the store are available. Readiness, cookie, and
// consumption failures are discarded so the logout caller can continue cookie
// cleanup.
func (h *PendingHandler) ConsumePendingOAuthSessionOnLogout(c *gin.Context) {
	if c == nil || c.Request == nil {
		return
	}

	// Missing proof or unavailable pending storage skips server consumption on
	// logout. The caller still expires browser cookies, so these failed reads
	// need no returned logout error.
	sessionToken, err := ReadOAuthPendingSessionCookie(c)
	if err != nil || strings.TrimSpace(sessionToken) == "" {
		return
	}
	browserSessionKey, err := ReadOAuthPendingBrowserCookie(c)
	if err != nil || strings.TrimSpace(browserSessionKey) == "" {
		return
	}

	svc, err := h.pendingIdentityService()
	if err != nil {
		return
	}
	// Pending consumption is best effort during logout. Discard its failure
	// because the caller still needs to expire browser state and return its
	// logout response.
	_, _ = svc.ConsumeBrowserSession(c.Request.Context(), sessionToken, browserSessionKey)
}

// BuildPendingOAuthSessionStatusPayload combines saved completion with
// provider, intent, and email observations for a nonnil session. Initial
// pending-result labels are written first, then normalized completion fields
// can overwrite them, so callers keep stored completion labels consistent with
// their session.
//
// A nonblank session ResolvedEmail takes final precedence over completion
// email; blank session email leaves the completion value available. The
// delegated normalization strips credentials and regularizes account-choice
// prompts, while unrelated nested values remain shared with saved completion
// state.
func BuildPendingOAuthSessionStatusPayload(session *identity.PendingAuthSession) gin.H {
	completionResponse := NormalizePendingOAuthCompletionResponse(MergePendingCompletionResponse(session, nil))
	payload := gin.H{
		"auth_result": "pending_session",
		"provider":    strings.TrimSpace(session.ProviderType),
		"intent":      strings.TrimSpace(session.Intent),
	}
	maps.Copy(payload, completionResponse)
	if email := strings.TrimSpace(session.ResolvedEmail); email != "" {
		payload["email"] = email
	}
	return payload
}

// NormalizePendingOAuthCompletionResponse delegates outer copying, credential
// removal, and frontend choice normalization. Unrelated nested values remain
// shared; this compatibility conversion does not validate account ownership or
// session eligibility.
func NormalizePendingOAuthCompletionResponse(payload map[string]any) map[string]any {
	return identity.NormalizePendingOAuthCompletionResponse(payload)
}

// WriteOAuthTokenPairResponse writes a nonnil credential pair as the legacy
// top-level JSON response. Callers complete account and pending checks first;
// this shape differs from the usual panel success envelope.
func WriteOAuthTokenPairResponse(c *gin.Context, tokenPair *identity.TokenPair) {
	c.JSON(http.StatusOK, gin.H{
		"access_token":  tokenPair.AccessToken,
		"refresh_token": tokenPair.RefreshToken,
		"expires_in":    tokenPair.ExpiresIn,
		"token_type":    "Bearer",
	})
}

// BindPendingLoginForProvider proves an existing account and binds the browser
// pending provider identity. A nonblank provider argument restricts the stored
// provider; targeted sessions require the proven account to match.
//
// Configured TOTP service and runtime enablement select a challenge for an
// account with TOTP active. Saved profile choice already precedes that gate;
// identity binding waits for second-factor completion. Otherwise binding and
// the optional login observer run before
// pair issuance, followed by separate pending consumption. Pair or consumption
// failure can therefore follow completed binding or issued credentials.
// Browser cookies are cleared on consumption failure or successful completion,
// and the caller receives the top-level token response.
func (h *PendingHandler) BindPendingLoginForProvider(c *gin.Context, provider string) {
	var req BindPendingOAuthLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	pendingSvc, session, clearCookies, err := ReadPendingOAuthBrowserSession(c, h)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if strings.TrimSpace(provider) != "" && !strings.EqualFold(strings.TrimSpace(session.ProviderType), provider) {
		response.BadRequest(c, "Pending oauth session provider mismatch")
		return
	}

	user, err := h.authService.ValidatePasswordCredentials(c.Request.Context(), strings.TrimSpace(req.Email), req.Password)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if session.TargetUserID != nil && *session.TargetUserID > 0 && user.ID != *session.TargetUserID {
		response.ErrorFrom(c, infraerrors.Conflict("PENDING_AUTH_TARGET_USER_MISMATCH", "pending oauth session must be completed by the targeted user"))
		return
	}
	if err := h.ensureBackendModeAllowsUser(c.Request.Context(), user); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	decision, err := h.ensurePendingOAuthAdoptionDecision(c, session.ID, req.adoptionDecision())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if h.totpService != nil && h.settingSvc.IsTotpEnabled(c.Request.Context()) && user.TotpEnabled {
		tempToken, err := h.totpService.CreatePendingOAuthBindLoginSession(
			c.Request.Context(),
			user.ID,
			user.Email,
			session.SessionToken,
			session.BrowserSessionKey,
		)
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
	// Persist binding and first-binding grants before issuing this account pair.
	// Browser consumption follows issuance in a separate operation, so failure
	// in either later stage cannot roll back the completed binding.
	if err := h.flow.Database.ApplyBinding(c.Request.Context(), identity.PendingBinding{Session: session, Decision: decision, OverrideUserID: &user.ID, ForceBind: true, ApplyFirstBindDefaults: true}); err != nil {
		RespondPendingOAuthBindingApplyError(c, err)
		return
	}

	h.authService.RecordSuccessfulLogin(c.Request.Context(), user.ID)
	// Run optional login observation after existing-account binding and before
	// pair issuance and pending consumption. The supplied callback controls
	// provider profile synchronization performed at this stage.
	if h.pendingOptions.AfterLogin != nil {
		h.pendingOptions.AfterLogin(c.Request.Context(), session, user.ID)
	}
	tokenPair, err := h.authService.GenerateTokenPair(c.Request.Context(), user, "")
	if err != nil {
		response.InternalError(c, "Failed to generate token pair")
		return
	}
	if _, err := pendingSvc.ConsumeBrowserSession(c.Request.Context(), session.SessionToken, session.BrowserSessionKey); err != nil {
		clearCookies()
		response.ErrorFrom(c, err)
		return
	}

	clearCookies()
	WriteOAuthTokenPairResponse(c, tokenPair)
}

// RespondPendingOAuthBindingApplyError forwards application failures in the
// 400 through 499 range. Other failures become the pending-binding internal
// error with a retained cause for the shared response mapper.
func RespondPendingOAuthBindingApplyError(c *gin.Context, err error) {
	if code := response.ErrorCode(err); code >= http.StatusBadRequest && code < http.StatusInternalServerError {
		response.ErrorFrom(c, err)
		return
	}
	response.ErrorFrom(c, infraerrors.InternalServer("PENDING_AUTH_BIND_APPLY_FAILED", "failed to bind pending oauth identity").WithCause(err))
}

// CreatePendingAccountForProvider validates browser registration state and
// either saves existing-account choice or creates a local account. A nonblank
// provider restricts the stored provider, and new accounts require backend and
// CAPTCHA admission.
//
// An email conflict during registration is re-read and converted into account
// choice. Successful creation passes profile choices to flow finalization,
// whose transaction binds, completes signup, and consumes state while
// compensating failed creation. Post-finalization promotion, login recording,
// and optional registration observation precede cookie cleanup and the
// returned credential response.
func (h *PendingHandler) CreatePendingAccountForProvider(c *gin.Context, provider string) {
	var req CreatePendingOAuthAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	_, session, clearCookies, err := ReadPendingOAuthBrowserSession(c, h)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if err := EnsurePendingOAuthCompleteRegistrationSession(session); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if strings.TrimSpace(provider) != "" && !strings.EqualFold(strings.TrimSpace(session.ProviderType), provider) {
		response.BadRequest(c, "Pending oauth session provider mismatch")
		return
	}

	if !h.flow.Available() {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready"))
		return
	}

	email := strings.TrimSpace(strings.ToLower(req.Email))
	existingUser, err := h.flow.Database.FindUserByNormalizedEmail(c.Request.Context(), email)
	if err != nil {
		switch {
		// A missing normalized email is the expected new-account case. Treat it as
		// no existing candidate so registration can proceed to backend, CAPTCHA, and
		// email ownership checks.
		case errors.Is(err, identity.ErrUserNotFound):
			existingUser = nil
		case response.ErrorCode(err) >= http.StatusBadRequest && response.ErrorCode(err) < http.StatusInternalServerError:
			response.ErrorFrom(c, err)
			return
		default:
			response.ErrorFrom(c, infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "service temporarily unavailable"))
			return
		}
	}
	if existingUser != nil {
		session, err = h.transitionPendingOAuthAccountToChoiceState(c, session, existingUser, email)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		c.JSON(http.StatusOK, BuildPendingOAuthSessionStatusPayload(session))
		return
	}
	if err := h.ensureBackendModeAllowsNewUserLogin(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	proof := captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr)
	if err := h.authService.VerifyCaptcha(c.Request.Context(), proof, clientip.GetClientIP(c)); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	tokenPair, user, err := h.authService.RegisterOAuthEmailAccount(
		c.Request.Context(),
		email,
		req.Password,
		strings.TrimSpace(req.VerifyCode),
		strings.TrimSpace(req.InvitationCode),
		strings.TrimSpace(session.ProviderType),
	)
	if err != nil {
		if errors.Is(err, identity.ErrEmailExists) {
			existingUser, lookupErr := h.flow.Database.FindUserByNormalizedEmail(c.Request.Context(), email)
			if lookupErr != nil {
				response.ErrorFrom(c, lookupErr)
				return
			}
			session, err = h.transitionPendingOAuthAccountToChoiceState(c, session, existingUser, email)
			if err != nil {
				response.ErrorFrom(c, err)
				return
			}
			c.JSON(http.StatusOK, BuildPendingOAuthSessionStatusPayload(session))
			return
		}
		response.ErrorFrom(c, err)
		return
	}

	finalization := identity.PendingAccountFinalization{Session: session, User: user, InvitationCode: strings.TrimSpace(req.InvitationCode), AffiliateCode: strings.TrimSpace(req.AffCode), BeforeCommit: h.pendingOptions.BeforeAccountCommit}
	if err := h.flow.FinalizeCreatedAccountWithChoice(c.Request.Context(), finalization, identity.OAuthAdoptionChoice{AdoptDisplayName: req.AdoptDisplayName, AdoptAvatar: req.AdoptAvatar}); err != nil {
		if failure, ok := errors.AsType[*identity.PendingWriteError](err); ok {
			switch failure.Phase {
			case "binding", "hook":
				RespondPendingOAuthBindingApplyError(c, failure.Cause)
			case "consume":
				clearCookies()
				response.ErrorFrom(c, failure.Cause)
			case "begin", "commit":
				response.ErrorFrom(c, infraerrors.InternalServer("PENDING_AUTH_BIND_APPLY_FAILED", "failed to bind pending oauth identity").WithCause(failure.Cause))
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
	// Run optional registration observation after database finalization and
	// promotion/login work. Provider synchronization performed by that callback
	// can precede browser-cookie cleanup and the returned credential response.
	if h.pendingOptions.AfterRegistration != nil {
		h.pendingOptions.AfterRegistration(c.Request.Context(), session, user.ID)
	}
	clearCookies()
	WriteOAuthTokenPairResponse(c, tokenPair)
}

// ExchangePendingOAuthCompletion reads browser proof and returns the stored
// account-choice state or completes authorized login and adoption. It strips
// stored credentials, supplies redirect and profile hints, and reloads a
// current active login target before binding.
//
// Invitation, email, binding-login, and required-adoption steps keep the
// browser session available. Other sessions may apply adoption only when token
// issuance is eligible or the intent binds a current user. Adoption precedes
// separate consumption, and fresh credentials are issued afterward; later
// failures can follow saved adoption or consumed state. Completion expires
// browser cookies and writes the panel payload.
func (h *PendingHandler) ExchangePendingOAuthCompletion(c *gin.Context) {
	secureCookie := IsRequestHTTPS(c)
	clearCookies := func() {
		ClearOAuthPendingSessionCookie(c, secureCookie)
		ClearOAuthPendingBrowserCookie(c, secureCookie)
	}
	adoptionDecision, err := BindOptionalOAuthAdoptionDecision(c)
	if err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	sessionToken, err := ReadOAuthPendingSessionCookie(c)
	if err != nil || strings.TrimSpace(sessionToken) == "" {
		clearCookies()
		response.ErrorFrom(c, identity.ErrPendingAuthSessionNotFound)
		return
	}
	browserSessionKey, err := ReadOAuthPendingBrowserCookie(c)
	if err != nil || strings.TrimSpace(browserSessionKey) == "" {
		clearCookies()
		response.ErrorFrom(c, identity.ErrPendingAuthBrowserMismatch)
		return
	}

	svc, err := h.pendingIdentityService()
	if err != nil {
		clearCookies()
		response.ErrorFrom(c, err)
		return
	}

	session, err := svc.GetBrowserSession(c.Request.Context(), sessionToken, browserSessionKey)
	if err != nil {
		clearCookies()
		response.ErrorFrom(c, err)
		return
	}

	payload, ok := ReadCompletionResponse(session.LocalFlowState)
	if !ok {
		clearCookies()
		response.ErrorFrom(c, infraerrors.InternalServer("PENDING_AUTH_COMPLETION_INVALID", "pending auth completion payload is invalid"))
		return
	}
	payload = NormalizePendingOAuthCompletionResponse(payload)
	if strings.TrimSpace(session.RedirectTo) != "" {
		if _, exists := payload["redirect"]; !exists {
			payload["redirect"] = session.RedirectTo
		}
	}
	ApplySuggestedProfileToCompletionResponse(payload, session.UpstreamIdentityClaims)

	canIssueTokenPair := PendingOAuthCompletionCanIssueTokenPair(session, payload)
	var loginUser *identity.User
	if canIssueTokenPair {
		loginUser, err = h.userService.GetByID(c.Request.Context(), *session.TargetUserID)
		if err != nil {
			clearCookies()
			response.ErrorFrom(c, err)
			return
		}
		if err := EnsureLoginUserActive(loginUser); err != nil {
			clearCookies()
			response.ErrorFrom(c, err)
			return
		}
		if err := h.ensureBackendModeAllowsUser(c.Request.Context(), loginUser); err != nil {
			clearCookies()
			response.ErrorFrom(c, err)
			return
		}
	}
	skipAdoptionPrompt, err := h.shouldSkipPendingOAuthAdoptionPrompt(c.Request.Context(), session, payload)
	if err != nil {
		clearCookies()
		response.ErrorFrom(c, err)
		return
	}
	if skipAdoptionPrompt {
		delete(payload, "adoption_required")
	}

	if PendingSessionWantsInvitation(payload) {
		if adoptionDecision.hasDecision() {
			decision, err := h.upsertPendingOAuthAdoptionDecision(c, session.ID, adoptionDecision)
			if err != nil {
				response.ErrorFrom(c, err)
				return
			}
			_ = decision
		}
		response.Success(c, payload)
		return
	}
	if PendingSessionRequiresEmailCompletion(payload) {
		response.Success(c, payload)
		return
	}
	if PendingSessionRequiresBindLogin(payload) {
		response.Success(c, payload)
		return
	}
	// Adopt the provider identity only after token issuance is authorized or the intent
	// binds an already authenticated current user.
	// A nonterminal session can contain a TargetUserID found from an unverified email.
	// Binding to that ID before password or email verification would allow account takeover.
	// Return the payload and leave all other sessions pending.
	if !canIssueTokenPair && !strings.EqualFold(strings.TrimSpace(session.Intent), oauthIntentBindCurrentUser) {
		response.Success(c, payload)
		return
	}
	if !adoptionDecision.hasDecision() {
		adoptionRequired, _ := payload["adoption_required"].(bool)
		if adoptionRequired {
			response.Success(c, payload)
			return
		}
	}

	decisionReq := adoptionDecision
	if !decisionReq.hasDecision() {
		adoptDisplayName := false
		adoptAvatar := false
		decisionReq = OauthAdoptionDecisionRequest{
			AdoptDisplayName: &adoptDisplayName,
			AdoptAvatar:      &adoptAvatar,
		}
	}

	decision, err := h.ensurePendingOAuthAdoptionDecision(c, session.ID, decisionReq)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// Adoption commits before the separate browser claim. Issue fresh credentials
	// only after that claim succeeds; failure can leave adopted profile data or
	// consumed state even when no pair reaches the client.
	if err := h.flow.Database.ApplyAdoption(c.Request.Context(), session, decision, session.TargetUserID); err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("PENDING_AUTH_ADOPTION_APPLY_FAILED", "failed to apply oauth profile adoption").WithCause(err))
		return
	}

	if _, err := svc.ConsumeBrowserSession(c.Request.Context(), sessionToken, browserSessionKey); err != nil {
		clearCookies()
		response.ErrorFrom(c, err)
		return
	}

	if canIssueTokenPair {
		tokenPair, err := h.authService.GenerateTokenPair(c.Request.Context(), loginUser, "")
		if err != nil {
			clearCookies()
			response.InternalError(c, "Failed to generate token pair")
			return
		}
		h.authService.RecordSuccessfulLogin(c.Request.Context(), loginUser.ID)
		payload["access_token"] = tokenPair.AccessToken
		payload["refresh_token"] = tokenPair.RefreshToken
		payload["expires_in"] = tokenPair.ExpiresIn
		payload["token_type"] = "Bearer"
	}

	clearCookies()
	response.Success(c, payload)
}

// SanitizeFrontendRedirectPath admits trimmed text beginning with one slash
// within the byte limit. Literal scheme separators, a second leading slash,
// CRLF, and oversized values return empty text for caller defaults; escape
// decoding and browser URL normalization remain outside this check.
func SanitizeFrontendRedirectPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	// Bound the returned redirect path in bytes before browser response
	// construction.
	if len(path) > linuxDoOAuthMaxRedirectLen {
		return ""
	}
	// Require a slash-prefixed spelling before the remaining literal checks.
	// This helper performs string checks; it does not decode escapes or apply
	// browser URL normalization.
	if !strings.HasPrefix(path, "/") {
		return ""
	}
	if strings.HasPrefix(path, "//") {
		return ""
	}
	if strings.Contains(path, "://") {
		return ""
	}
	if strings.ContainsAny(path, "\r\n") {
		return ""
	}
	return path
}

// IsRequestHTTPS reports whether TLS is present or the trimmed
// X-Forwarded-Proto header says https. This helper reads the raw header
// directly; callers configure forwarding-header handling outside this local
// check.
func IsRequestHTTPS(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	proto := strings.ToLower(strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")))
	return proto == "https"
}

// EncodeCookieValue returns unpadded URL-safe base64 text for shared OAuth
// browser cookies.
func EncodeCookieValue(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

// DecodeCookieValue decodes unpadded URL-safe base64 cookie text into its
// original string. Invalid encoded input returns the decoder error so callers
// can reject unusable browser continuation proof.
func DecodeCookieValue(value string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// ReadCookieDecoded reads the named request cookie and restores shared OAuth
// string encoding. Missing-cookie and decode errors pass through; the helper
// leaves browser cookie expiration to its caller.
func ReadCookieDecoded(c *gin.Context, name string) (string, error) {
	ck, err := c.Request.Cookie(name)
	if err != nil {
		return "", err
	}
	return DecodeCookieValue(ck.Value)
}

// CreateEmailRegistrationSession generates browser proof and stores a
// registration draft with verified email through the shared pending writer.
// Missing handler or profile returns service unavailable. An io.ReadFull error
// during verifier generation terminates the process before any cookie is emitted.
//
// The browser cookie is set before draft preparation and persistence, so a
// later store failure can leave that value in the response. Invitation settings
// contribute the draft requirement, and CreateOAuthPendingSession writes the
// session cookie only after its record is saved. Callers handle failure cleanup
// for any browser state already emitted.
func (h *PendingHandler) CreateEmailRegistrationSession(c *gin.Context, provider, frontendCallback, redirectTo string, profile *identity.EmailOAuthProfile, affCode, promoCode string) error {
	if h == nil || profile == nil {
		return infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")
	}
	browser, e := GenerateOAuthPendingBrowserSession()
	if e != nil {
		return infraerrors.InternalServer("PENDING_AUTH_SESSION_CREATE_FAILED", "failed to create pending auth session").WithCause(e)
	}
	// Publish the generated browser proof before storing the prepared
	// registration draft. A later pending-session creation error can leave this
	// cookie in the response.
	SetOAuthPendingBrowserCookie(c, browser, IsRequestHTTPS(c))
	invitation := h.settingSvc != nil && h.settingSvc.IsInvitationCodeEnabled(c.Request.Context())
	draft := identity.PrepareEmailRegistrationDraft(provider, frontendCallback, redirectTo, browser, profile, affCode, promoCode, invitation)
	return h.CreateOAuthPendingSession(c, draft)
}
