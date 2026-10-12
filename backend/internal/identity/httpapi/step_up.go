package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
)

// StepUpSessionKey uses the refresh-family ID when available.
// Legacy requests use a credential digest to separate sessions belonging to the same user.
// Without either value, the key falls back to the user ID.
func StepUpSessionKey(c *gin.Context, userID int64) string {
	sid := c.GetString(authctx.ContextKeySessionID)
	if principal, ok := authctx.GetPrincipal(c); ok {
		sid = principal.SessionID
	}
	if sid != "" {
		return sid
	}
	authHeader := strings.TrimSpace(c.GetHeader("Authorization"))
	if authHeader != "" {
		parts := strings.Fields(authHeader)
		credential := authHeader
		if len(parts) == 2 {
			credential = parts[1]
		}
		sum := sha256.Sum256([]byte(credential))
		return "legacy:" + hex.EncodeToString(sum[:16])
	}
	return fmt.Sprintf("u%d", userID)
}

func StepUpAuth(grantChecker StepUpGrantChecker, userReader UserReader, settings StepUpSettingReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !EnforceStepUp(c, grantChecker, userReader, settings) {
			return
		}
		c.Next()
	}
}

func EnforceStepUp(c *gin.Context, grantChecker StepUpGrantChecker, userReader UserReader, settings StepUpSettingReader) bool {
	// A disabled switch bypasses step-up for every credential kind.
	// Missing settings leave enforcement enabled.
	if settings != nil && !settings.IsStepUpEnabled(c.Request.Context()) {
		return true
	}

	adminAPIKey := c.GetString("auth_method") == "admin_api_key"
	if principal, ok := authctx.GetPrincipal(c); ok {
		// Prefer the verified Principal; use auth_method when it is absent.
		adminAPIKey = principal.CredentialKind == "admin_api_key"
	}
	if adminAPIKey {
		AbortWithError(c, 403, "STEP_UP_ADMIN_API_KEY_FORBIDDEN",
			"Admin API key cannot access this endpoint; a two-factor verified admin session is required")
		return false
	}

	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		AbortWithError(c, 401, "UNAUTHORIZED", "Authorization required")
		return false
	}

	user, err := userReader.GetByID(c.Request.Context(), subject.UserID)
	if err != nil || user == nil {
		AbortWithError(c, 500, "INTERNAL_ERROR", "Failed to load user")
		return false
	}
	if !user.TotpEnabled {
		AbortWithError(c, 403, "STEP_UP_TOTP_NOT_ENABLED",
			"This operation requires two-factor authentication; please enable TOTP first")
		return false
	}

	sessionKey := StepUpSessionKey(c, subject.UserID)
	granted, err := grantChecker.HasStepUpGrant(c.Request.Context(), subject.UserID, sessionKey)
	if err != nil {
		// Reject the request when the grant check is unavailable.
		AbortWithError(c, 503, "STEP_UP_UNAVAILABLE", "Step-up verification service unavailable")
		return false
	}
	if !granted {
		AbortWithError(c, 403, "STEP_UP_REQUIRED",
			"This operation requires recent two-factor verification")
		return false
	}

	return true
}
