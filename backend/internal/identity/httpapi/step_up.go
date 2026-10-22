package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
)

// step_up.go checks session-specific TOTP grants before sensitive
// administrator requests proceed.
// Sensitive administrator operations require a recent TOTP grant for the
// requesting session when step-up is enabled. A recorded refresh-family ID
// identifies that session; requests with older context use a digest of their
// Authorization credential and finally a user-based key when both values are
// absent.
//
// The verified principal supplies the credential kind when present.
// Administrator API keys are refused before user lookup, while JWT requests
// must identify a user with TOTP enabled and a current grant. Settings can
// disable this check, but missing settings leave it active; grant lookup
// failures produce a service-unavailable response.

// StepUpSessionKey selects the session identifier used to store and check TOTP
// grants. A private principal overrides the compatibility session ID, even
// when the principal ID is empty.
//
// Requests with no usable session ID receive a digest of the Authorization
// credential, using its second field when there are exactly two fields. This
// separates legacy credentials belonging to one user without retaining
// credential text in the grant key. With no Authorization header, the key
// identifies the supplied user.
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
		// Keep sixteen digest bytes, producing a 32-character legacy session suffix.
		return "legacy:" + hex.EncodeToString(sum[:16])
	}
	// Requests lacking both session and credential information share the
	// user-based grant key; middleware normally supplies a more specific value.
	return fmt.Sprintf("u%d", userID)
}

// StepUpAuth builds middleware that enforces recent TOTP verification before
// downstream handlers run. Register it after authentication, and supply user
// and grant readers whenever the runtime settings enable step-up.
func StepUpAuth(grantChecker StepUpGrantChecker, userReader UserReader, settings StepUpSettingReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !EnforceStepUp(c, grantChecker, userReader, settings) {
			return
		}
		c.Next()
	}
}

// EnforceStepUp checks the current credential, user TOTP state, and session
// grant before a sensitive operation. False means an error response was
// written and the Gin request aborted; nil settings keep checking enabled.
// User and grant readers are required on the enabled JWT path.
func EnforceStepUp(c *gin.Context, grantChecker StepUpGrantChecker, userReader UserReader, settings StepUpSettingReader) bool {
	// A disabled switch bypasses step-up for every credential kind. Missing
	// settings leave enforcement enabled.
	if settings != nil && !settings.IsStepUpEnabled(c.Request.Context()) {
		return true
	}

	adminAPIKey := c.GetString("auth_method") == "admin_api_key"
	if principal, ok := authctx.GetPrincipal(c); ok {
		// The private principal captures the verified credential kind, so later
		// compatibility auth_method writes cannot change API-key admission here.
		adminAPIKey = principal.CredentialKind == "admin_api_key"
	}
	if adminAPIKey {
		AbortWithError(c, 403, "STEP_UP_ADMIN_API_KEY_FORBIDDEN",
			"Admin API key cannot access this endpoint; a two-factor verified admin session is required")
		return false
	}
	if principal, ok := authctx.GetPrincipal(c); ok && principal.CredentialKind == "mcp_token" && principal.SessionID == "" {
		AbortWithError(c, 403, "STEP_UP_REQUIRED", "This operation requires browser confirmation with recent two-factor verification")
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
		// An unavailable grant reader cannot establish recent verification, so
		// callers retry after a 503 rather than continuing the sensitive operation.
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
