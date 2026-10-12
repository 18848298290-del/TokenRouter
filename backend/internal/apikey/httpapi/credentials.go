package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	googleapi "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

const MaxAuthorizationHeaderBytes = apikey.MaxAPIKeyCredentialBytes + 128

func HeadersTooLarge(c *gin.Context) bool {
	if c == nil {
		return false
	}
	return len(c.GetHeader("Authorization")) > MaxAuthorizationHeaderBytes ||
		len(c.GetHeader("x-api-key")) > apikey.MaxAPIKeyCredentialBytes ||
		len(c.GetHeader("x-goog-api-key")) > apikey.MaxAPIKeyCredentialBytes
}

func HasCredentialInput(c *gin.Context) bool {
	if c == nil {
		return false
	}
	return c.GetHeader("Authorization") != "" ||
		c.GetHeader("x-api-key") != "" ||
		c.GetHeader("x-goog-api-key") != ""
}

// AbortTeamError maps team lifecycle and member limit failures to gateway responses.
func AbortTeamError(c *gin.Context, err error) bool {
	switch {
	case errors.Is(err, apikey.ErrTeamMemberDailyExceeded):
		AbortWithError(c, http.StatusTooManyRequests, "TEAM_MEMBER_DAILY_LIMIT_EXCEEDED", "The team member daily limit has been reached")
	case errors.Is(err, apikey.ErrTeamMemberWeeklyExceeded):
		AbortWithError(c, http.StatusTooManyRequests, "TEAM_MEMBER_WEEKLY_LIMIT_EXCEEDED", "The team member weekly limit has been reached")
	case errors.Is(err, apikey.ErrTeamMemberMonthlyExceeded):
		AbortWithError(c, http.StatusTooManyRequests, "TEAM_MEMBER_MONTHLY_LIMIT_EXCEEDED", "The team member monthly limit has been reached")
	case errors.Is(err, apikey.ErrTeamFeatureDisabled):
		AbortWithError(c, http.StatusForbidden, "TEAM_FEATURE_DISABLED", "The team feature is disabled")
	case errors.Is(err, apikey.ErrTeamSuspended):
		AbortWithError(c, http.StatusForbidden, "TEAM_SUSPENDED", "The team is suspended")
	case errors.Is(err, apikey.ErrTeamMembershipRequired):
		AbortWithError(c, http.StatusForbidden, "TEAM_MEMBERSHIP_REQUIRED", "The team membership is no longer active")
	case errors.Is(err, apikey.ErrTeamActorInactive):
		AbortWithError(c, http.StatusForbidden, "TEAM_ACTOR_INACTIVE", "The member assigned to this team API key is inactive")
	case errors.Is(err, apikey.ErrTeamBillingOwnerInactive):
		AbortWithError(c, http.StatusForbidden, "TEAM_BILLING_OWNER_INACTIVE", "The team billing owner is inactive")
	default:
		return false
	}
	return true
}

// ExtractGoogleCredential extracts API key for Google/Gemini endpoints.
// Priority: x-goog-api-key > Authorization: Bearer > x-api-key > query key
// This allows OpenClaw and other clients using Bearer auth to work with Gemini endpoints.
func ExtractGoogleCredential(c *gin.Context) string {
	// 1) preferred: Gemini native header
	if k := strings.TrimSpace(c.GetHeader("x-goog-api-key")); k != "" {
		return k
	}

	// 2) fallback: Authorization: Bearer <key>
	auth := strings.TrimSpace(c.GetHeader("Authorization"))
	if auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			if k := strings.TrimSpace(parts[1]); k != "" {
				return k
			}
		}
	}

	// 3) x-api-key header (backward compatibility)
	if k := strings.TrimSpace(c.GetHeader("x-api-key")); k != "" {
		return k
	}

	// 4) query parameter key (for specific paths)
	if AllowGoogleQueryKey(c.Request.URL.Path) {
		if v := strings.TrimSpace(c.Query("key")); v != "" {
			return v
		}
	}

	return ""
}

func AllowGoogleQueryKey(path string) bool {
	return strings.HasPrefix(path, "/v1beta") || strings.HasPrefix(path, "/antigravity/v1beta")
}

func AbortGoogleError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"code":    status,
			"message": message,
			"status":  googleapi.HTTPStatusToGoogleStatus(status),
		},
	})
	c.Abort()
}

// GoogleTeamError returns the status and message for a Google-style team authentication error.
func GoogleTeamError(err error) (int, string, bool) {
	switch {
	case errors.Is(err, apikey.ErrTeamMemberDailyExceeded), errors.Is(err, apikey.ErrTeamMemberWeeklyExceeded), errors.Is(err, apikey.ErrTeamMemberMonthlyExceeded):
		return 429, "Team member usage limit exceeded", true
	case errors.Is(err, apikey.ErrTeamFeatureDisabled):
		return 403, "Team feature is disabled", true
	case errors.Is(err, apikey.ErrTeamSuspended):
		return 403, "Team is suspended", true
	case errors.Is(err, apikey.ErrTeamMembershipRequired):
		return 403, "Team membership is no longer valid", true
	case errors.Is(err, apikey.ErrTeamActorInactive):
		return 403, "Team key member is inactive", true
	case errors.Is(err, apikey.ErrTeamBillingOwnerInactive):
		return 403, "Team billing owner is inactive", true
	default:
		return 0, "", false
	}
}
