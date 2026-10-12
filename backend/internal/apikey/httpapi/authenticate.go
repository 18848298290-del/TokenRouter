package httpapi

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// HTTP credential parsing selects either the generic or Google compatibility
// rules before loading Key identity. Abuse and header-size checks run first,
// and a loaded access record can still describe a failed attempt for telemetry
// and Ops. The adapter returns success after identity checks; group, protocol
// and funding admission remain the gateway caller's next decisions.
//
// Failure kinds preserve the rejection stage while the adapter selects response
// status, public text and observation callbacks. Google failures use their
// nested protocol shape; generic failures use the shared error response.
// Principal publication occurs through SetAccessPrincipal after the caller's
// admission checks, so diagnostic ownership updates alone cannot establish an
// authenticated request.

// AuthenticationOptions supplies HTTP input readers, protocol choice and
// rejection observations.
type AuthenticationOptions struct {
	Google bool // Google credential and response mode
	// Context can replace the standard request context for service lookup. Nil
	// uses c.Request.Context; callers return a usable context carrying required
	// request metadata for authentication.
	Context func(*gin.Context) context.Context // optional service lookup context reader
	// ClientIP supplies the resolved address passed to identity IP checks. Nil
	// supplies empty text, which configured IP restrictions reject when the loaded
	// Key reaches that check.
	ClientIP func(*gin.Context) string // optional resolved client address reader
	// AbuseClientKey supplies a stable client bucket independent of credential
	// text. Nil selects an empty bucket, so the configured abuse limiter records
	// and checks no client entry.
	AbuseClientKey func(*gin.Context) string // optional stable client bucket reader
	// NonConsuming returns true to skip team member allowance enforcement during
	// this identity check. Nil enforces that check; gateway funding admission uses
	// its own endpoint classification later.
	NonConsuming func(*gin.Context) bool // optional retrieval endpoint classification callback
	// Rejected receives ingress rejection reasons when present. The callback
	// observes the failed attempt before the adapter writes its response; nil
	// skips observation and retains error handling.
	Rejected func(*gin.Context, string) // optional ingress rejection observer
	// BusinessLimited observes configured permission or allowance rejection when
	// present. Nil skips this observation while the adapter still returns the same
	// rejection status and public message.
	BusinessLimited func(*gin.Context, string) // optional business allowance observer
	// Loaded receives a request Key whenever a loaded access record exists,
	// including rejected identity checks. Callers use it for diagnostics and wait
	// for successful final admission before exposing authenticated access.
	Loaded func(*gin.Context, *apikey.APIKey) // optional diagnostic loaded-Key observer
}

// AbortWithError writes the shared gateway error response with the supplied
// status and aborts Gin handling. Authentication callers provide fixed public
// code and message text; this helper forwards their supplied values directly.
func AbortWithError(c *gin.Context, status int, code, message string) {
	c.JSON(status, httpx.NewErrorResponse(code, message))
	c.Abort()
}

// reject invokes the configured ingress observer with reason when present. A
// nil callback omits this observation; the authentication caller still owns
// writing the selected error response and aborting handling.
func (o AuthenticationOptions) reject(c *gin.Context, reason string) {
	if o.Rejected != nil {
		o.Rejected(c, reason)
	}
}

// business invokes the configured business-limit observer with reason when
// present. A nil callback omits the observation; this helper leaves response
// status and request admission decisions with its authentication caller.
func (o AuthenticationOptions) business(c *gin.Context, reason string) {
	if o.BusinessLimited != nil {
		o.BusinessLimited(c, reason)
	}
}

// abort writes and aborts the selected generic or Google failure response.
// Google responses use status and message while the supplied generic code is
// omitted; ordinary responses include the supplied code and message.
func (o AuthenticationOptions) abort(c *gin.Context, status int, code, message string) {
	if o.Google {
		AbortGoogleError(c, status, message)
	} else {
		AbortWithError(c, status, code, message)
	}
}

// Authenticate parses the selected credential form and returns loaded access
// with true when identity checks succeed. Callers supply a usable Gin request
// and service, then complete group and funding admission before publishing a
// principal. Gateway routes apply their protocol gate during later processing.
//
// Abuse and header-size checks precede credential lookup, and rejected
// credential syntax records an invalid-auth failure. NonConsuming disables
// member spending checks for this identity call when true. Loaded access is
// recorded for telemetry and the optional Loaded callback before the error is
// inspected; these diagnostics remain available on failed identity checks.
// Known failures write and abort in the selected protocol shape and return
// false.
func Authenticate(c *gin.Context, auth *apikey.APIKeyService, o AuthenticationOptions) (*apikey.AccessSnapshot, bool) {
	clientKey := ""
	if o.AbuseClientKey != nil {
		clientKey = o.AbuseClientKey(c)
	}
	invalid := func() { auth.RecordInvalidAuthFailure(clientKey) }
	if retry, blocked := auth.CheckInvalidAuthAbuse(clientKey); blocked {
		// Round the remaining block upward so Retry-After does not invite
		// another attempt before the client's observed block deadline has elapsed.
		seconds := max(int(math.Ceil(retry.Seconds())), 1)
		c.Header("Retry-After", strconv.Itoa(seconds))
		o.reject(c, "invalid_auth_rate_limited")
		o.abort(c, 429, "INVALID_AUTH_RATE_LIMITED", "Too many invalid authentication attempts; retry later")
		return nil, false
	}
	if HeadersTooLarge(c) {
		invalid()
		o.reject(c, "invalid_api_key")
		o.abort(c, 401, "INVALID_API_KEY", "Invalid API key")
		return nil, false
	}
	if o.Google {
		if strings.TrimSpace(c.Query("api_key")) != "" {
			invalid()
			o.reject(c, "query_api_key_deprecated")
			o.abort(c, 400, "api_key_in_query_deprecated", "Query parameter api_key is deprecated. Use Authorization header or key instead.")
			return nil, false
		}
	} else if strings.TrimSpace(c.Query("key")) != "" || strings.TrimSpace(c.Query("api_key")) != "" {
		invalid()
		o.reject(c, "query_api_key_deprecated")
		o.abort(c, 400, "api_key_in_query_deprecated", "API key in query parameter is deprecated. Please use Authorization header instead.")
		return nil, false
	}
	credential := ""
	if o.Google {
		credential = ExtractGoogleCredential(c)
	} else {
		if header := c.GetHeader("Authorization"); header != "" {
			parts := strings.SplitN(header, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				credential = strings.TrimSpace(parts[1])
			}
		}
		// Generic clients prefer a trimmed Bearer value, then raw compatible
		// Key headers. An unusable bearer value keeps the later header choices available.
		if credential == "" {
			credential = c.GetHeader("x-api-key")
		}
		if credential == "" {
			credential = c.GetHeader("x-goog-api-key")
		}
	}
	if credential == "" {
		invalid()
		if HasCredentialInput(c) {
			o.reject(c, "invalid_api_key")
		} else {
			o.reject(c, "api_key_required")
		}
		message := "API key is required in Authorization header (Bearer scheme), x-api-key header, or x-goog-api-key header"
		if o.Google {
			message = "API key is required"
		}
		o.abort(c, 401, "API_KEY_REQUIRED", message)
		return nil, false
	}
	if len(credential) > apikey.MaxAPIKeyCredentialBytes {
		invalid()
		o.reject(c, "invalid_api_key")
		o.abort(c, 401, "INVALID_API_KEY", "Invalid API key")
		return nil, false
	}
	ctx := c.Request.Context()
	if o.Context != nil {
		ctx = o.Context(c)
	}
	ip := ""
	if o.ClientIP != nil {
		ip = o.ClientIP(c)
	}
	checkLimits := true
	if o.NonConsuming != nil {
		checkLimits = !o.NonConsuming(c)
	}
	access, err := auth.Authenticate(ctx, credential, apikey.AuthenticationInput{ClientIP: ip, CheckMemberLimits: checkLimits})
	// Loaded ownership helps observe rejected attempts too. Record diagnostics
	// before error mapping while leaving principal publication to later admission.
	recordAccessOwnership(c, access)
	if access != nil && o.Loaded != nil {
		o.Loaded(c, access.KeyView())
	}
	if err == nil {
		return access, true
	}
	var failure *apikey.AuthenticationFailure
	if !errors.As(err, &failure) {
		o.abort(c, 500, "INTERNAL_ERROR", "Failed to validate API key")
		return nil, false
	}
	switch failure.Kind {
	case apikey.AuthenticationLookup:
		switch {
		case errors.Is(err, apikey.ErrAPIKeyNotFound):
			invalid()
			o.reject(c, "invalid_api_key")
			o.abort(c, 401, "INVALID_API_KEY", "Invalid API key")
		case errors.Is(err, apikey.ErrGroupDisabledForUser):
			o.business(c, "api_key_group_unavailable")
			o.abort(c, 403, "GROUP_DISABLED_FOR_USER", "The public group assigned to this API key is disabled for your account")
		case errors.Is(err, apikey.ErrAPIKeyAuthOverloaded):
			o.reject(c, "api_key_auth_overloaded")
			o.abort(c, 503, "API_KEY_AUTH_OVERLOADED", "API key authentication is temporarily unavailable")
		default:
			if !o.abortTeam(c, err) {
				o.abort(c, 500, "INTERNAL_ERROR", "Failed to validate API key")
			}
		}
	case apikey.AuthenticationDisabled:
		o.reject(c, "api_key_disabled")
		o.abort(c, 401, "API_KEY_DISABLED", "API key is disabled")
	case apikey.AuthenticationTeam:
		if !o.abortTeam(c, err) {
			o.abort(c, 500, "INTERNAL_ERROR", "Failed to validate team API key")
		}
	case apikey.AuthenticationMemberLimit:
		if !o.abortTeam(c, err) {
			o.abort(c, 500, "INTERNAL_ERROR", "Failed to validate team member limits")
		}
	case apikey.AuthenticationIP:
		o.business(c, "api_key_ip_restriction")
		o.reject(c, "ip_restricted")
		o.abort(c, 403, "ACCESS_DENIED", err.Error())
	case apikey.AuthenticationUserMissing:
		o.abort(c, 401, "USER_NOT_FOUND", "User associated with API key not found")
	case apikey.AuthenticationUserInactive:
		o.reject(c, "user_inactive")
		o.abort(c, 401, "USER_INACTIVE", "User account is not active")
	}
	return nil, false
}

// abortTeam writes a protocol-shaped response for recognized team errors and
// reports whether it handled them. Wrapped causes keep their classification;
// unrecognized errors return false so the caller can choose its internal-error
// response.
func (o AuthenticationOptions) abortTeam(c *gin.Context, err error) bool {
	if !o.Google {
		return AbortTeamError(c, err)
	}
	status, message, ok := GoogleTeamError(err)
	if ok {
		AbortGoogleError(c, status, message)
	}
	return ok
}

// SetAccessPrincipal stores the admitted access pointer and publishes its actor
// as an API-Key principal. Nil input does nothing; callers complete group and
// funding admission first, and the stored pointer remains shared with the
// request owner.
func SetAccessPrincipal(c *gin.Context, a *apikey.AccessSnapshot) {
	if a == nil {
		return
	}
	c.Set("apikey_access_snapshot", a)
	recordAccessOwnership(c, a)
	authctx.SetAuthenticatedPrincipal(c, identity.Principal{UserID: a.ActorUserID, CredentialKind: "api_key"})
}

// recordAccessOwnership updates telemetry with the loaded Key, actor and
// optional team identifiers. Nil Gin, request or access inputs do nothing; the
// function publishes no authentication principal, and callers can record it for
// rejected requests.
func recordAccessOwnership(c *gin.Context, access *apikey.AccessSnapshot) {
	if c == nil || c.Request == nil || access == nil {
		return
	}
	telemetry.UpdateRequest(c.Request.Context(), func(record *telemetry.RequestRecord) {
		record.APIKeyID = access.KeyID
		record.UserID = access.ActorUserID
		record.TeamID = 0
		if access.TeamID != nil {
			record.TeamID = *access.TeamID
		}
	})
}
