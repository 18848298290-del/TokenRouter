package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// oauth_binding.go transfers authenticated binding intent from panel requests
// into signed OAuth target cookies.
// OAuth binding starts can be reached by browser navigation after an
// authenticated panel request prepares a short-lived access-token cookie. The
// start handler resolves the target account and signs a separate user-ID
// cookie, which the provider callback verifies before applying the binding.
//
// A Gin authenticated subject takes precedence over the temporary token
// cookie. The cookie path consumes its browser value before decoding and
// validating the token, then reloads current account status and version.
// Provider callback cookies carry the signed target ID; the supplied signer
// verifies that value, and the pending database locates the current target
// account.

// Binding-cookie values define the temporary credential name, OAuth path, and
// browser lifetime used to bridge panel requests to navigation starts.
const (
	oauthBindAccessTokenCookieName = "oauth_bind_access_token" // temporary access credential cookie
	oauthBindAccessTokenCookiePath = "/api/v1/auth/oauth"      // shared OAuth cookie path
	oauthBindAccessTokenCookieTTL  = 10 * 60                   // seconds
)

// OAuthBindHandler resolves current-user binding targets and transports their
// temporary browser cookies.
type OAuthBindHandler struct {
	*SessionHandler                             // shared session authentication handlers
	Signer          identity.OAuthBindingSigner // target user cookie signer
}

// NewOAuthBindHandler retains shared session handlers and the binding-cookie
// signer.
func NewOAuthBindHandler(session *SessionHandler, signer identity.OAuthBindingSigner) *OAuthBindHandler {
	return &OAuthBindHandler{session, signer}
}

// PrepareOAuthBindAccessTokenCookie stores the Bearer credential for an
// upcoming navigation-based binding start. Register it behind JWT
// authentication; it checks header shape, sets a short-lived HttpOnly cookie,
// and writes HTTP 204.
func (h *OAuthBindHandler) PrepareOAuthBindAccessTokenCookie(c *gin.Context) {
	const bearerPrefix = "Bearer "

	authHeader := strings.TrimSpace(c.GetHeader("Authorization"))
	if !strings.HasPrefix(strings.ToLower(authHeader), strings.ToLower(bearerPrefix)) {
		response.ErrorFrom(c, infraerrors.Unauthorized("UNAUTHORIZED", "authentication required"))
		return
	}

	token := strings.TrimSpace(authHeader[len(bearerPrefix):])
	if token == "" {
		response.ErrorFrom(c, infraerrors.Unauthorized("UNAUTHORIZED", "authentication required"))
		return
	}

	SetOAuthBindAccessTokenCookie(c, token, IsRequestHTTPS(c))
	c.Status(http.StatusNoContent)
	c.Writer.WriteHeaderNow()
}

// BuildOAuthBindUserCookieFromContext signs the resolved account ID for a
// provider binding callback. Target resolution errors and unusable IDs become
// an authentication error; a usable target requires an initialized signer.
func (h *OAuthBindHandler) BuildOAuthBindUserCookieFromContext(c *gin.Context) (string, error) {
	userID, err := h.ResolveOAuthBindTargetUserID(c)
	if err != nil || userID == nil || *userID <= 0 {
		return "", infraerrors.Unauthorized("UNAUTHORIZED", "authentication required")
	}
	return h.Signer.Sign(*userID)
}

// ResolveOAuthBindTargetUserID returns the authenticated account ID from Gin
// or a temporary access-token cookie. The cookie path requires initialized
// session authentication and current-user readers.
//
// Reading the cookie also emits its deletion header before decoding or
// validation, so a failed start still clears the browser credential. Query
// decoding restores the value escaped during cookie creation. Signed claims
// must match an active current user and credential version before that user ID
// is returned; this path does not invoke the general session-binding check.
func (h *OAuthBindHandler) ResolveOAuthBindTargetUserID(c *gin.Context) (*int64, error) {
	if subject, ok := authctx.GetAuthSubjectFromContext(c); ok && subject.UserID > 0 {
		return &subject.UserID, nil
	}
	if h == nil || h.SessionHandler == nil || h.authService == nil || h.userService == nil {
		return nil, identity.ErrInvalidToken
	}

	ck, err := c.Request.Cookie(oauthBindAccessTokenCookieName)
	// Clear the temporary browser credential before validating its contents, so a
	// rejected cookie-based binding start also consumes the value used for
	// navigation.
	ClearOAuthBindAccessTokenCookie(c, IsRequestHTTPS(c))
	if err != nil {
		return nil, err
	}

	// Cookie creation query-escapes the credential. Decode that representation
	// before JWT parsing so encoded characters remain part of the same supplied
	// token.
	tokenString, err := url.QueryUnescape(strings.TrimSpace(ck.Value))
	if err != nil {
		return nil, err
	}
	if tokenString == "" {
		return nil, identity.ErrInvalidToken
	}

	claims, err := h.authService.ValidateToken(tokenString)
	if err != nil {
		return nil, err
	}
	user, err := h.userService.GetByID(c.Request.Context(), claims.UserID)
	if err != nil {
		return nil, err
	}
	if user == nil || !user.IsActive() || claims.TokenVersion != user.TokenVersion {
		return nil, identity.ErrInvalidToken
	}
	return &user.ID, nil
}

// ReadOAuthBindUserIDFromCookie decodes a provider cookie and verifies its
// signed target account ID. Missing or malformed cookies return their read
// errors; signer verification errors propagate to the callback caller.
func (h *OAuthBindHandler) ReadOAuthBindUserIDFromCookie(c *gin.Context, cookieName string) (int64, error) {
	value, err := ReadCookieDecoded(c, cookieName)
	if err != nil {
		return 0, err
	}
	return h.Signer.Verify(value)
}

// ClearOAuthBindAccessTokenCookie emits a deletion cookie at the shared OAuth
// path. The caller supplies the HTTPS decision; HttpOnly and SameSite Lax
// match the temporary credential cookie used by binding starts.
func ClearOAuthBindAccessTokenCookie(c *gin.Context, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     oauthBindAccessTokenCookieName,
		Value:    "",
		Path:     oauthBindAccessTokenCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// SetOAuthBindAccessTokenCookie stores trimmed, query-escaped access
// credentials with a ten-minute lifetime at the OAuth path. The caller
// supplies the HTTPS decision, and the HttpOnly SameSite Lax cookie supports
// navigation-based starts.
func SetOAuthBindAccessTokenCookie(c *gin.Context, token string, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     oauthBindAccessTokenCookieName,
		Value:    url.QueryEscape(strings.TrimSpace(token)),
		Path:     oauthBindAccessTokenCookiePath,
		MaxAge:   oauthBindAccessTokenCookieTTL,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ReadTargetUser verifies a provider binding cookie and loads its target
// through the pending database. Missing database readiness returns service
// unavailable; cookie errors become authentication required before the target
// lookup runs.
func (h *OAuthBindHandler) ReadTargetUser(c *gin.Context, name string, db identity.PendingDatabase) (*identity.User, error) {
	if db == nil || !db.HasDatabase() {
		return nil, infraerrors.ServiceUnavailable("PENDING_AUTH_NOT_READY", "pending auth service is not ready")
	}
	id, e := h.ReadOAuthBindUserIDFromCookie(c, name)
	if e != nil {
		return nil, infraerrors.Unauthorized("AUTH_REQUIRED", "current user is required to bind wechat account")
	}
	return db.FindOAuthBindTarget(c.Request.Context(), id)
}
