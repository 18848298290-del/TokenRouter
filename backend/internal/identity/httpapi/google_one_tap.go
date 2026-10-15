package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// google_one_tap.go verifies Google One Tap login requests and returns
// credentials or pending registration.
// Google One Tap accepts a browser ID token and produces either panel
// credentials or browser-bound pending registration. Verification requires a
// nonempty subject, email, and verified-email flag before those claims enter
// the email OAuth identity flow; raw credential text stays in the request and
// verifier call.
//
// The whole request and selected text fields have byte limits before provider
// validation. Enabled Tencent or Aliyun action CAPTCHA rejects this proof-less
// entry. Existing-account login checks backend-mode admission after the
// authentication service returns. When the initial account lookup selects new
// registration, the handler checks its registration switch and backend policy
// before storing state. An invitation-required login result also creates pending
// registration. Successful JSON responses carry no-store headers and use
// separate authenticated and registration-required shapes.

// One Tap limits bound anonymous request decoding and signup context; redirect
// defaults and status labels select the browser continuation and response
// shape.
const (
	emailOAuthDefaultRedirect   = "/dashboard"           // default local login destination
	emailOAuthDefaultFrontendCB = "/auth/oauth/callback" // default pending browser callback

	GoogleOneTapCredentialMaxBytes  = 16 * 1024               // bytes
	GoogleOneTapContextMaxBytes     = 256                     // bytes
	GoogleOneTapRequestMaxBytes     = 24 * 1024               // bytes
	GoogleOneTapStatusAuthenticated = "authenticated"         // completed panel login status
	GoogleOneTapStatusRegistration  = "registration_required" // pending registration response status
)

type (
	// GoogleOneTapOptions contains the verified-token audience and
	// pending-registration callback from one settings read.
	GoogleOneTapOptions struct {
		// ClientID is the expected Google token audience; FrontendRedirectURL is the
		// pending browser callback, with blank text selecting
		// emailOAuthDefaultFrontendCB. LoadConfig returns both for this request.
		ClientID, FrontendRedirectURL string
	}
	// GoogleOneTapHTTPOptions supplies provider configuration and the runtime
	// registration decision.
	GoogleOneTapHTTPOptions struct {
		// LoadConfig reads the token audience and frontend callback before
		// verification. A missing callback produces
		// authentication-service-not-ready; callers handle any returned settings
		// error.
		LoadConfig func(context.Context) (GoogleOneTapOptions, error)
		// RegistrationEnabled is required when the identity flow requests new
		// pending registration. Its result gates that branch; invitation-required
		// continuation after login uses the prepared pending callback directly.
		RegistrationEnabled func(context.Context) bool
	}
)

// GoogleOneTapHandler verifies browser Google credentials through the shared
// email OAuth flow.
type GoogleOneTapHandler struct {
	*PendingHandler                                // shared browser pending authentication
	Verifier        identity.GoogleIDTokenVerifier // Google credential signature verifier
	googleOptions   GoogleOneTapHTTPOptions        // configuration and registration callbacks
}

// GoogleOneTapRequest carries a browser Google credential and optional signup
// context.
type GoogleOneTapRequest struct {
	Credential string `json:"credential" binding:"required"` // browser Google ID token
	Redirect   string `json:"redirect,omitempty"`            // requested local login destination
	AffCode    string `json:"aff_code,omitempty"`            // optional signup affiliate code
	PromoCode  string `json:"promo_code,omitempty"`          // optional signup promotion code
}

// GoogleOneTapResponse reports authenticated credentials or a pending
// registration continuation.
type GoogleOneTapResponse struct {
	// Status selects authenticated or registration_required. AccessToken,
	// RefreshToken, ExpiresIn, and TokenType are set for authenticated responses;
	// Redirect is returned for the browser pending-registration branch.
	Status       string `json:"status"`                  // One Tap outcome label
	AccessToken  string `json:"access_token,omitempty"`  // issued signed access credential
	RefreshToken string `json:"refresh_token,omitempty"` // issued rotating refresh credential
	ExpiresIn    int    `json:"expires_in,omitempty"`    // seconds
	TokenType    string `json:"token_type,omitempty"`    // Bearer authorization scheme
	Redirect     string `json:"redirect,omitempty"`      // pending local login destination
}

// NewGoogleOneTapHandler retains shared pending handlers, the credential
// verifier, and HTTP option callbacks. Configure the verifier and required
// option callbacks before exposing anonymous login or registration requests.
func NewGoogleOneTapHandler(pending *PendingHandler, verifier identity.GoogleIDTokenVerifier, options GoogleOneTapHTTPOptions) *GoogleOneTapHandler {
	return &GoogleOneTapHandler{pending, verifier, options}
}

// verifyGoogleOneTapCredential delegates credential verification with the
// configured expected audience. The verifier owns signature and token-claim
// checks; its returned claims and error pass directly to One Tap admission.
func (h *GoogleOneTapHandler) verifyGoogleOneTapCredential(ctx context.Context, credential, audience string) (*identity.GoogleIDTokenClaims, error) {
	return h.Verifier.Verify(ctx, credential, audience)
}

// GoogleOneTap validates a bounded browser credential before resolving Google
// login or pending signup. Provider subject and verified email are required,
// and successful responses disable caching.
//
// The identity flow decides whether a new account needs pending registration;
// that branch checks the registration switch and backend policy before
// creating browser state. Existing-account authentication can instead report
// invitation-required and create a continuation, or return credentials
// followed by the current-user backend check. Failures from these later stages
// remain errors even after preceding service work has completed.
func (h *GoogleOneTapHandler) GoogleOneTap(c *gin.Context) {
	// Bound the whole anonymous request before decoding to limit allocation from
	// oversized or unknown fields.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, GoogleOneTapRequestMaxBytes)
	var req GoogleOneTapRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request")
		return
	}
	req.Credential = strings.TrimSpace(req.Credential)
	req.AffCode = strings.TrimSpace(req.AffCode)
	req.PromoCode = strings.TrimSpace(req.PromoCode)
	// Reject over-budget credential and signup strings before reading settings or
	// validating a Google token, keeping those later stages behind the anonymous
	// request's text bounds.
	if req.Credential == "" || len(req.Credential) > GoogleOneTapCredentialMaxBytes {
		response.BadRequest(c, "Google credential is invalid")
		return
	}
	if len(req.AffCode) > GoogleOneTapContextMaxBytes || len(req.PromoCode) > GoogleOneTapContextMaxBytes {
		response.BadRequest(c, "OAuth context is invalid")
		return
	}
	if h == nil || h.PendingHandler == nil || h.SessionHandler == nil || h.authService == nil || h.googleOptions.LoadConfig == nil {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("AUTH_SERVICE_NOT_READY", "authentication service is not ready"))
		return
	}

	cfg, err := h.googleOptions.LoadConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// One Tap has no action-CAPTCHA proof. Running the shared verifier with an
	// empty proof rejects enabled Tencent or Aliyun configurations before the
	// browser credential enters provider verification.
	if err := h.authService.VerifyActionCaptchaIfEnabled(c.Request.Context(), identity.CaptchaProof{}, clientip.GetClientIP(c)); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	claims, err := h.verifyGoogleOneTapCredential(c.Request.Context(), req.Credential, cfg.ClientID)
	if err != nil || claims == nil || claims.Subject == "" || claims.Email == "" || !claims.EmailVerified {
		response.ErrorFrom(c, infraerrors.Unauthorized("GOOGLE_ONE_TAP_INVALID_CREDENTIAL", "google credential is invalid"))
		return
	}
	h.auditActor(c, 0, claims.Email)

	profile := identity.GoogleEmailProfile(claims)
	input := identity.EmailOAuthIdentityFromProfile("google", profile)
	redirectTo := SanitizeFrontendRedirectPath(req.Redirect)
	if redirectTo == "" {
		redirectTo = emailOAuthDefaultRedirect
	}
	frontendCallback := strings.TrimSpace(cfg.FrontendRedirectURL)
	if frontendCallback == "" {
		frontendCallback = emailOAuthDefaultFrontendCB
	}

	createPending := func() error {
		return h.CreateEmailRegistrationSession(
			c,
			"google",
			frontendCallback,
			redirectTo,
			profile,
			req.AffCode,
			req.PromoCode,
		)
	}
	if shouldCreate, pendingErr := h.flow.EmailShouldCreatePendingRegistration(c.Request.Context(), input); pendingErr != nil {
		response.ErrorFrom(c, pendingErr)
		return
	} else if shouldCreate {
		if !h.googleOptions.RegistrationEnabled(c.Request.Context()) {
			response.ErrorFrom(c, identity.ErrRegDisabled)
			return
		}
		if pendingErr := h.ensureBackendModeAllowsNewUserLogin(c.Request.Context()); pendingErr != nil {
			response.ErrorFrom(c, pendingErr)
			return
		}
		if pendingErr := createPending(); pendingErr != nil {
			response.ErrorFrom(c, pendingErr)
			return
		}
		WriteGoogleOneTapResponse(c, GoogleOneTapResponse{Status: GoogleOneTapStatusRegistration, Redirect: redirectTo})
		return
	}

	tokenPair, user, err := h.authService.LoginOrRegisterVerifiedEmailOAuthWithSignupCodes(
		c.Request.Context(),
		input,
		"",
		req.AffCode,
		req.PromoCode,
	)
	if err != nil {
		// Invitation-required authentication continues with browser-bound pending
		// state, so the frontend can collect the missing registration proof before
		// account completion.
		if errors.Is(err, identity.ErrOAuthInvitationRequired) {
			if pendingErr := createPending(); pendingErr != nil {
				response.ErrorFrom(c, pendingErr)
				return
			}
			WriteGoogleOneTapResponse(c, GoogleOneTapResponse{Status: GoogleOneTapStatusRegistration, Redirect: redirectTo})
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	if err := h.ensureBackendModeAllowsUser(c.Request.Context(), user); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if tokenPair == nil || user == nil {
		response.ErrorFrom(c, infraerrors.InternalServer("TOKEN_GENERATION_FAILED", "failed to generate token pair"))
		return
	}
	h.auditActor(c, user.ID, user.Email)
	WriteGoogleOneTapResponse(c, GoogleOneTapResponse{
		Status:       GoogleOneTapStatusAuthenticated,
		AccessToken:  tokenPair.AccessToken,
		RefreshToken: tokenPair.RefreshToken,
		ExpiresIn:    tokenPair.ExpiresIn,
		TokenType:    "Bearer",
	})
}

// WriteGoogleOneTapResponse adds no-store and no-cache headers before writing
// the supplied panel success payload. Callers choose its authenticated or
// registration-required fields; this helper performs no credential or account
// validation.
func WriteGoogleOneTapResponse(c *gin.Context, payload GoogleOneTapResponse) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	response.Success(c, payload)
}
