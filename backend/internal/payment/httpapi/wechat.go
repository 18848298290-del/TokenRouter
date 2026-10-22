package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/oauthpkce"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// wechat.go carries payment purchase context through WeChat OAuth and issues
// signed checkout continuation tokens.
// Payment OAuth obtains a WeChat payer OpenID and returns signed checkout
// continuation through a frontend URL fragment. Start stores state, redirect,
// purchase context and requested scope in four encoded HttpOnly cookies scoped
// to these payment OAuth endpoints; their encoding does not itself sign the
// context.
//
// Callback provider-error and missing-code/state paths return before cookie
// cleanup is installed. Later paths compare the callback state with the
// decoded cookie, then clear all four cookies on return, exchange the code and
// issue the resume token. Redirect normalization preserves the
// payment-to-purchase compatibility mapping, while purchase amount text and
// plan IDs are carried for later checkout validation. This flow supplies
// payment identity and does not establish an account login session.

// WechatPaymentOAuth constants name the frontend callback/default destination
// and the four scoped browser cookies carrying state, redirect, purchase
// context and requested authorization scope between payment OAuth requests.
const (
	// frontend continuation callback path.
	WechatPaymentOAuthFrontendCB = "/auth/wechat/payment/callback"
	// default purchase destination path.
	WechatPaymentOAuthDefaultTo = "/purchase"
	// requested scope cookie name.
	WechatPaymentOAuthScope = "wechat_payment_oauth_scope"
	// purchase context cookie name.
	WechatPaymentOAuthContextName = "wechat_payment_oauth_context"
	// return destination cookie name.
	WechatPaymentOAuthRedirect = "wechat_payment_oauth_redirect"
	// OAuth state cookie name.
	WechatPaymentOAuthStateName = "wechat_payment_oauth_state"
	// payment OAuth cookie path.
	WechatPaymentOAuthCookiePath = "/api/v1/auth/oauth/wechat/payment"
)

// WeChatPaymentHTTPOptions supplies payment OAuth configuration, callback
// location, token exchange and resume signing.
type WeChatPaymentHTTPOptions struct {
	// Config reads OAuth options for the requested mode. Start and callback pass
	// mp and preserve its returned error before building authorization or
	// exchanging the code.
	Config func(context.Context, string, *gin.Context) (identitycore.WeChatOAuthOptions, error)
	// CallbackURL supplies the redirect URI written into configuration for both
	// authorization and code exchange. Those two phases call it independently
	// with the current request.
	CallbackURL func(context.Context, *gin.Context) string
	// Resume supplies the service that signs accepted payer/purchase
	// continuation. Callback invokes it after code exchange; callers provide a
	// nonnil service for that path.
	Resume func() *payment.PaymentResumeService
	// Exchange obtains payer identity from the callback code using current
	// configuration and request context. Callback propagates exchange failure
	// into its frontend error redirect after cookie cleanup is installed.
	Exchange func(context.Context, identitycore.WeChatOAuthOptions, string) (WeChatPaymentToken, error)
}

// WeChatPaymentHandler carries the supplied operations for payment OAuth start
// and callback.
type WeChatPaymentHandler struct {
	options WeChatPaymentHTTPOptions // copied OAuth operation options
}

// WeChatPaymentToken contains the exchanged payer identity and
// provider-reported authorization scope.
type WeChatPaymentToken struct {
	// OpenID is trimmed by callback and must be nonblank before continuation
	// signing. Its identity comes from the configured code exchange rather than
	// purchase cookies.
	OpenID string
	// Scope replaces the cookie-derived requested scope when provider exchange
	// returns nonblank text. Callback passes that trimmed value into resume
	// claims, which the signer serializes as supplied.
	Scope string
}

// WechatPaymentOAuthContext carries encoded purchase inputs across the browser
// OAuth redirect.
type WechatPaymentOAuthContext struct {
	// PaymentType is selected from supported WeChat labels at start. An empty
	// decoded context defaults it at callback; resume-token creation applies its
	// own visible method normalization.
	PaymentType string `json:"payment_type"`
	// Amount carries trimmed purchase amount text from start without numeric
	// validation. Checkout continuation application parses that optional text
	// after the signed token returns.
	Amount    string `json:"amount,omitempty"`
	OrderType string `json:"order_type,omitempty"` // requested purchase category label
	// PlanID carries ParseWeChatPaymentPlanID's result through encoded OAuth
	// context. Continuation application can adopt positive values, including
	// the maximum int64 returned for a positive range error, as the checkout
	// plan selection.
	PlanID int64 `json:"plan_id,omitempty"`
}

// NewWeChatPaymentHandler retains a value copy of the supplied OAuth
// operations without applying defaults. Start/callback callers provide usable
// Config, CallbackURL, Exchange and Resume callbacks for the paths they
// invoke.
func NewWeChatPaymentHandler(options WeChatPaymentHTTPOptions) *WeChatPaymentHandler {
	return &WeChatPaymentHandler{options}
}

// WeChatPaymentOAuthStart builds official-account authorization from the
// selected WeChat method and purchase query context. It validates
// configuration/method first, then creates random state and encodes the
// purchase context before setting cookies.
//
// The redirect is sanitized and mapped to purchase compatibility paths. Four
// cookies retain state, destination, context and normalized requested scope at
// the payment OAuth cookie path. Configuration then receives the current
// callback URI/scope before authorization URL construction. A later URL-build
// error leaves those just-written cookies in the response. Amount text and
// parsed plan ID are carried for later checkout validation rather than
// creating an order here.
func (h *WeChatPaymentHandler) WeChatPaymentOAuthStart(c *gin.Context) {
	cfg, err := h.options.Config(c.Request.Context(), "mp", c)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	paymentType := NormalizeWeChatPaymentType(c.Query("payment_type"))
	if paymentType == "" {
		response.BadRequest(c, "Invalid payment type")
		return
	}

	// Use thirty-two random bytes for browser OAuth state.
	stateBytes, err := oauthpkce.RandomBytes(32)
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_STATE_GEN_FAILED", "failed to generate oauth state").WithCause(err))
		return
	}

	state := oauthpkce.Base64URL(stateBytes)
	redirectTo := NormalizeWeChatPaymentRedirectPath(identityhttp.SanitizeFrontendRedirectPath(c.Query("redirect")))
	if redirectTo == "" {
		redirectTo = WechatPaymentOAuthDefaultTo
	}
	rawContext, err := EncodeWeChatPaymentOAuthContext(WechatPaymentOAuthContext{
		PaymentType: paymentType,
		Amount:      strings.TrimSpace(c.Query("amount")),
		OrderType:   strings.TrimSpace(c.Query("order_type")),
		PlanID:      ParseWeChatPaymentPlanID(c.Query("plan_id")),
	})
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_CONTEXT_ENCODE_FAILED", "failed to encode oauth context").WithCause(err))
		return
	}

	scope := NormalizeWeChatPaymentScope(c.Query("scope"))
	secureCookie := identityhttp.IsRequestHTTPS(c)
	// Publish all four browser values before building the external authorization
	// redirect. Their shared path and lifetime let callback read the same state
	// and purchase context after WeChat returns.
	WechatPaymentSetCookie(c, WechatPaymentOAuthStateName, identityhttp.EncodeCookieValue(state), identityhttp.WechatOAuthCookieMaxAgeSec, secureCookie)
	WechatPaymentSetCookie(c, WechatPaymentOAuthRedirect, identityhttp.EncodeCookieValue(redirectTo), identityhttp.WechatOAuthCookieMaxAgeSec, secureCookie)
	WechatPaymentSetCookie(c, WechatPaymentOAuthContextName, identityhttp.EncodeCookieValue(rawContext), identityhttp.WechatOAuthCookieMaxAgeSec, secureCookie)
	WechatPaymentSetCookie(c, WechatPaymentOAuthScope, identityhttp.EncodeCookieValue(scope), identityhttp.WechatOAuthCookieMaxAgeSec, secureCookie)

	cfg.RedirectURI = h.options.CallbackURL(c.Request.Context(), c)
	cfg.Scope = scope
	authURL, err := identityhttp.BuildWeChatAuthorizeURL(cfg, state)
	if err != nil {
		response.ErrorFrom(c, infraerrors.InternalServer("OAUTH_BUILD_URL_FAILED", "failed to build oauth authorization url").WithCause(err))
		return
	}

	c.Redirect(http.StatusFound, authURL)
}

// WeChatPaymentOAuthCallback validates browser state, exchanges the WeChat
// code and issues a signed payer/purchase continuation to the frontend
// fragment. Provider-error and missing-code/state paths redirect before the
// deferred cookie deletion is installed.
//
// Subsequent paths clear all four payment cookies on return. State must equal
// the decoded nonblank state cookie; optional redirect/context/scope read
// errors are discarded and their values go through applicable normalization or
// decoding. Current configuration supplies the exchange callback URI. Nonblank
// returned OpenID is required, and provider scope can replace the requested
// scope before resume signing. Signature/expiry then belongs to the returned
// token service; encoded purchase cookies alone do not authenticate an
// account.
func (h *WeChatPaymentHandler) WeChatPaymentOAuthCallback(c *gin.Context) {
	frontendCallback := WechatPaymentOAuthFrontendCB

	if providerErr := strings.TrimSpace(c.Query("error")); providerErr != "" {
		identityhttp.RedirectOAuthError(c, frontendCallback, "provider_error", providerErr, c.Query("error_description"))
		return
	}

	code := strings.TrimSpace(c.Query("code"))
	state := strings.TrimSpace(c.Query("state"))
	if code == "" || state == "" {
		identityhttp.RedirectOAuthError(c, frontendCallback, "missing_params", "missing code/state", "")
		return
	}

	secureCookie := identityhttp.IsRequestHTTPS(c)
	// Code/state are present, so every subsequent exit expires the four payment
	// cookies. Earlier provider-error and missing-parameter redirects precede
	// this cleanup and leave them untouched.
	defer func() {
		WechatPaymentClearCookie(c, WechatPaymentOAuthStateName, secureCookie)
		WechatPaymentClearCookie(c, WechatPaymentOAuthRedirect, secureCookie)
		WechatPaymentClearCookie(c, WechatPaymentOAuthContextName, secureCookie)
		WechatPaymentClearCookie(c, WechatPaymentOAuthScope, secureCookie)
	}()

	expectedState, err := identityhttp.ReadCookieDecoded(c, WechatPaymentOAuthStateName)
	if err != nil || expectedState == "" || state != expectedState {
		identityhttp.RedirectOAuthError(c, frontendCallback, "invalid_state", "invalid oauth state", "")
		return
	}

	// Optional redirect, purchase-context and scope cookie reads discard their
	// errors. Normalize returned values and let context decoding reject unusable
	// JSON before code exchange continues.
	redirectTo, _ := identityhttp.ReadCookieDecoded(c, WechatPaymentOAuthRedirect)
	redirectTo = NormalizeWeChatPaymentRedirectPath(identityhttp.SanitizeFrontendRedirectPath(redirectTo))
	if redirectTo == "" {
		redirectTo = WechatPaymentOAuthDefaultTo
	}

	rawContext, _ := identityhttp.ReadCookieDecoded(c, WechatPaymentOAuthContextName)
	paymentContext, err := DecodeWeChatPaymentOAuthContext(rawContext)
	if err != nil {
		identityhttp.RedirectOAuthError(c, frontendCallback, "invalid_context", "invalid oauth context", "")
		return
	}
	if paymentContext.PaymentType == "" {
		paymentContext.PaymentType = payment.TypeWxpay
	}

	scope, _ := identityhttp.ReadCookieDecoded(c, WechatPaymentOAuthScope)
	scope = NormalizeWeChatPaymentScope(scope)

	cfg, err := h.options.Config(c.Request.Context(), "mp", c)
	if err != nil {
		identityhttp.RedirectOAuthError(c, frontendCallback, "provider_error", infraerrors.Reason(err), infraerrors.Message(err))
		return
	}
	cfg.RedirectURI = h.options.CallbackURL(c.Request.Context(), c)
	tokenResp, err := h.options.Exchange(c.Request.Context(), cfg, code)
	if err != nil {
		identityhttp.RedirectOAuthError(c, frontendCallback, "token_exchange_failed", "failed to exchange oauth code", err.Error())
		return
	}

	openid := strings.TrimSpace(tokenResp.OpenID)
	if openid == "" {
		identityhttp.RedirectOAuthError(c, frontendCallback, "missing_openid", "missing openid", "")
		return
	}
	if strings.TrimSpace(tokenResp.Scope) != "" {
		scope = strings.TrimSpace(tokenResp.Scope)
	}

	resumeToken, err := h.options.Resume().CreateWeChatPaymentResumeToken(payment.WeChatPaymentResumeClaims{
		OpenID:      openid,
		PaymentType: paymentContext.PaymentType,
		Amount:      paymentContext.Amount,
		OrderType:   paymentContext.OrderType,
		PlanID:      paymentContext.PlanID,
		RedirectTo:  redirectTo,
		Scope:       scope,
	})
	if err != nil {
		identityhttp.RedirectOAuthError(c, frontendCallback, "invalid_context", "failed to encode payment resume context", "")
		return
	}

	// Place the signed continuation and sanitized destination in the frontend
	// fragment. The browser receives payer context there for later checkout
	// rather than as a newly created payment order.
	fragment := url.Values{}
	fragment.Set("wechat_resume_token", resumeToken)
	fragment.Set("redirect", redirectTo)
	identityhttp.RedirectOAuthFragment(c, frontendCallback, fragment)
}

// ClearWeChatPaymentCookies expires all four payment OAuth cookies using the
// request's HTTPS-derived secure flag and their shared path. Logout invokes
// this cleanup separately from callback's deferred deletion of the same cookie
// names.
func ClearWeChatPaymentCookies(c *gin.Context) {
	secureCookie := identityhttp.IsRequestHTTPS(c)
	WechatPaymentClearCookie(c, WechatPaymentOAuthStateName, secureCookie)
	WechatPaymentClearCookie(c, WechatPaymentOAuthRedirect, secureCookie)
	WechatPaymentClearCookie(c, WechatPaymentOAuthContextName, secureCookie)
	WechatPaymentClearCookie(c, WechatPaymentOAuthScope, secureCookie)
}

// NormalizeWeChatPaymentType accepts only trimmed visible or historical direct
// WeChat labels, retaining the accepted spelling. Other inputs return empty,
// so OAuth start rejects them before state/cookie creation.
func NormalizeWeChatPaymentType(raw string) string {
	switch strings.TrimSpace(raw) {
	case payment.TypeWxpay, payment.TypeWxpayDirect:
		return strings.TrimSpace(raw)
	default:
		return ""
	}
}

// NormalizeWeChatPaymentScope returns the first recognized snsapi_userinfo or
// snsapi_base token in the supplied comma/whitespace list. No recognized scope
// selects snsapi_base; ordering therefore controls a list containing both
// accepted values.
func NormalizeWeChatPaymentScope(raw string) string {
	for _, part := range strings.FieldsFunc(strings.TrimSpace(raw), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}) {
		switch strings.TrimSpace(part) {
		case "snsapi_userinfo":
			return "snsapi_userinfo"
		case "snsapi_base":
			return "snsapi_base"
		}
	}
	return "snsapi_base"
}

// NormalizeWeChatPaymentRedirectPath maps trimmed payment result paths to
// purchase compatibility paths, preserving the query suffix. Empty input
// selects the purchase default; other paths pass through, so callers sanitize
// frontend redirect safety separately.
//
// Only exact /payment or /payment? forms rewrite. Child-path or fragment forms
// pass through because this helper compares text rather than parsing a URL.
// The retained query can carry purchase continuation input for the caller's
// later handling.
func NormalizeWeChatPaymentRedirectPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return WechatPaymentOAuthDefaultTo
	}
	// Map payment-page links to the purchase destination used by checkout
	// continuation. Keep the query suffix so resumed purchase inputs survive that
	// path compatibility transformation.
	if path == "/payment" {
		return "/purchase"
	}
	if strings.HasPrefix(path, "/payment?") {
		return "/purchase" + strings.TrimPrefix(path, "/payment")
	}
	return path
}

// EncodeWeChatPaymentOAuthContext returns the JSON encoding of purchase
// context for cookie transport. It performs no signing or purchase validation;
// start separately applies cookie encoding, and callback later uses the
// decoded context for resume signing.
func EncodeWeChatPaymentOAuthContext(ctx WechatPaymentOAuthContext) (string, error) {
	data, err := json.Marshal(ctx)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// DecodeWeChatPaymentOAuthContext decodes trimmed JSON context, returning an
// empty context for blank input. JSON errors remain visible to callback, while
// successfully decoded purchase fields still require later method/amount/plan
// validation.
func DecodeWeChatPaymentOAuthContext(raw string) (WechatPaymentOAuthContext, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return WechatPaymentOAuthContext{}, nil
	}
	var ctx WechatPaymentOAuthContext
	if err := json.Unmarshal([]byte(raw), &ctx); err != nil {
		return WechatPaymentOAuthContext{}, err
	}
	return ctx, nil
}

// ParseWeChatPaymentPlanID parses trimmed decimal text and returns ParseInt's
// int64 value while discarding its error. Syntax errors yield zero; range errors
// yield the minimum or maximum int64. UserApplyWeChatPaymentResumeClaims uses
// positive results as the chosen plan when its earlier validation succeeds.
func ParseWeChatPaymentPlanID(raw string) int64 {
	// Discarding the error lets OAuth start continue with ParseInt's value.
	// A range error carries its saturated int64 result into the cookie context,
	// which callback passes into the signed checkout continuation.
	id, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return id
}

// WechatPaymentSetCookie writes the supplied value with the payment OAuth
// path, HttpOnly, Lax same-site policy and caller-selected Secure/MaxAge.
// Callers provide an already encoded cookie value; this helper does not sign
// its contents.
func WechatPaymentSetCookie(c *gin.Context, name string, value string, maxAgeSec int, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     WechatPaymentOAuthCookiePath,
		MaxAge:   maxAgeSec,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// WechatPaymentClearCookie writes an empty cookie with negative MaxAge using
// the payment OAuth path, HttpOnly/Lax policy and supplied Secure flag.
// Matching those attributes targets the start cookies for expiry during
// callback or logout.
func WechatPaymentClearCookie(c *gin.Context, name string, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     WechatPaymentOAuthCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}
