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

const (
	oauthBindAccessTokenCookieName = "oauth_bind_access_token"
	oauthBindAccessTokenCookiePath = "/api/v1/auth/oauth"
	oauthBindAccessTokenCookieTTL  = 10 * 60
)

// OAuthBindHandler transports temporary binding cookies; session validation establishes the user identity.
type OAuthBindHandler struct {
	*SessionHandler
	Signer identity.OAuthBindingSigner
}

func NewOAuthBindHandler(session *SessionHandler, signer identity.OAuthBindingSigner) *OAuthBindHandler {
	return &OAuthBindHandler{session, signer}
}

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

func (h *OAuthBindHandler) BuildOAuthBindUserCookieFromContext(c *gin.Context) (string, error) {
	userID, err := h.ResolveOAuthBindTargetUserID(c)
	if err != nil || userID == nil || *userID <= 0 {
		return "", infraerrors.Unauthorized("UNAUTHORIZED", "authentication required")
	}
	return h.Signer.Sign(*userID)
}

func (h *OAuthBindHandler) ResolveOAuthBindTargetUserID(c *gin.Context) (*int64, error) {
	if subject, ok := authctx.GetAuthSubjectFromContext(c); ok && subject.UserID > 0 {
		return &subject.UserID, nil
	}
	if h == nil || h.SessionHandler == nil || h.authService == nil || h.userService == nil {
		return nil, identity.ErrInvalidToken
	}

	ck, err := c.Request.Cookie(oauthBindAccessTokenCookieName)
	ClearOAuthBindAccessTokenCookie(c, IsRequestHTTPS(c))
	if err != nil {
		return nil, err
	}

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

func (h *OAuthBindHandler) ReadOAuthBindUserIDFromCookie(c *gin.Context, cookieName string) (int64, error) {
	value, err := ReadCookieDecoded(c, cookieName)
	if err != nil {
		return 0, err
	}
	return h.Signer.Verify(value)
}

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

// ReadTargetUser checks database availability, verifies the binding cookie, and loads its target user.
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
