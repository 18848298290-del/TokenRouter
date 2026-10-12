package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	googleapi "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Gateway credential headers are bounded before Key lookup to constrain
// invalid-authentication work. Google entry extraction prefers its native
// header and accepts a query key only on the supported path prefixes, while
// team rejection helpers shape the same domain failures for generic or Google
// clients.

// MaxAuthorizationHeaderBytes allows credential length plus bearer-scheme and
// whitespace overhead before the HTTP adapter rejects an oversized
// authorization header.
const MaxAuthorizationHeaderBytes = apikey.MaxAPIKeyCredentialBytes + 128

// HeadersTooLarge reports whether any supported credential header exceeds its
// byte allowance. Nil Gin contexts return false; the authorization header has
// additional scheme overhead, while both direct Key headers use
// MaxAPIKeyCredentialBytes.
func HeadersTooLarge(c *gin.Context) bool {
	if c == nil {
		return false
	}
	return len(c.GetHeader("Authorization")) > MaxAuthorizationHeaderBytes ||
		len(c.GetHeader("x-api-key")) > apikey.MaxAPIKeyCredentialBytes ||
		len(c.GetHeader("x-goog-api-key")) > apikey.MaxAPIKeyCredentialBytes
}

// HasCredentialInput reports whether any supported credential header contains
// nonempty raw text. Nil contexts return false; malformed authorization text
// still counts as input, and query credentials are considered by the extractor
// separately.
func HasCredentialInput(c *gin.Context) bool {
	if c == nil {
		return false
	}
	return c.GetHeader("Authorization") != "" ||
		c.GetHeader("x-api-key") != "" ||
		c.GetHeader("x-goog-api-key") != ""
}

// AbortTeamError writes and aborts for recognized team lifecycle or
// member-allowance errors. It uses errors.Is so wrapped causes retain their
// mapping; unrecognized errors return false with no response for the caller to
// handle.
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

// ExtractGoogleCredential returns the first nonempty trimmed credential from
// Google header, Bearer authorization, ordinary Key header, then permitted
// query input. Callers provide a usable request and Gin context. The Bearer
// scheme ignores case, malformed or empty bearer input permits later sources,
// and query key is examined only when AllowGoogleQueryKey accepts the path.
func ExtractGoogleCredential(c *gin.Context) string {
	// Google-native clients put their credential in this header; its nonempty
	// value wins when a request also supplies other compatible authentication forms.
	if k := strings.TrimSpace(c.GetHeader("x-goog-api-key")); k != "" {
		return k
	}

	// Bearer credentials support clients sharing the generic gateway scheme.
	// Malformed or empty bearer text leaves the later header and query sources eligible.
	auth := strings.TrimSpace(c.GetHeader("Authorization"))
	if auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			if k := strings.TrimSpace(parts[1]); k != "" {
				return k
			}
		}
	}

	// Ordinary Key-header clients can reuse their credential form when the
	// Google and bearer sources are absent or supply no usable credential text.
	if k := strings.TrimSpace(c.GetHeader("x-api-key")); k != "" {
		return k
	}

	// Query credentials are retained for these Google route prefixes. Other
	// paths require a supported header even when query input contains a secret.
	if AllowGoogleQueryKey(c.Request.URL.Path) {
		if v := strings.TrimSpace(c.Query("key")); v != "" {
			return v
		}
	}

	return ""
}

// AllowGoogleQueryKey reports whether path starts with /v1beta or
// /antigravity/v1beta. The check is literal prefix matching, so callers needing
// a narrower route segment condition perform that routing check themselves.
func AllowGoogleQueryKey(path string) bool {
	return strings.HasPrefix(path, "/v1beta") || strings.HasPrefix(path, "/antigravity/v1beta")
}

// AbortGoogleError writes the supplied HTTP status and message in Google's
// nested error shape and aborts Gin processing. The symbolic Google status is
// derived from the HTTP code through the shared response helper.
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

// GoogleTeamError returns the HTTP status and public message for recognized
// team failure identities. Wrapped causes are accepted through errors.Is, and
// unmatched errors return false so the HTTP caller can select another response.
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
