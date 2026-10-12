package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// UserReader loads current user state for authentication.
type UserReader interface {
	GetByID(context.Context, int64) (*identity.User, error)
}
type AdminUserReader interface {
	UserReader
	GetFirstAdmin(context.Context) (*identity.User, error)
}
type ActivityToucher interface {
	TouchLastActiveForUser(context.Context, *identity.User)
}
type SessionAuth interface {
	ValidateToken(string) (*identity.JWTClaims, error)
	RevokeSessionFamily(context.Context, string) error
}
type (
	BindingSettings interface{ IsSessionBindingEnabled(context.Context) bool }
	AuthSettings    interface {
		BindingSettings
		GetAdminAPIKey(context.Context) (string, error)
	}
)

type (
	StepUpSettingReader interface{ IsStepUpEnabled(context.Context) bool }
	StepUpGrantChecker  interface {
		HasStepUpGrant(context.Context, int64, string) (bool, error)
	}
)

// BindingMismatchEvent contains user and request details for a session-binding audit event.
type BindingMismatchEvent struct {
	UserID                                         int64
	Email, Role, Method, Path, ClientIP, UserAgent string
}
type AuthObserver interface {
	RecordBindingMismatch(context.Context, BindingMismatchEvent)
}
type ForwardedIPSettings struct {
	TrustForwardedIP bool
	Headers          []string
}
type (
	JWTAuthMiddleware    gin.HandlerFunc
	AdminAuthMiddleware  gin.HandlerFunc
	StepUpAuthMiddleware gin.HandlerFunc
)

func AbortWithError(c *gin.Context, status int, code, message string) {
	httpx.AbortWithError(c, status, code, message)
}
func normalizePersistentText(v string, n int) string { return httpx.NormalizePersistentText(v, n) }
