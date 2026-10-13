package middleware

import (
	"net/netip"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"

	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
)

// Admission failures can finish before gateway execution has an authenticated
// key in context. A request marker carries the expected rejection reason until
// Logger observes the completed request, then the installed recorder receives a
// route family, protocol, normalized client bucket, and whatever key ownership
// is available.
//
// Authenticated key data takes precedence over the diagnostic fallback saved
// during early authentication. IPv4-mapped addresses use IPv4 text, IPv6
// addresses share a masked /64 bucket, and invalid addresses share a fixed
// sentinel. Publishing the recorder through one atomic holder lets new requests
// observe replacement while an already loaded holder can finish its call.
// Recorder implementations and their resource lifetimes remain the installer's
// responsibility.

const (
	// Admission reasons identify expected authentication and group rejections for
	// aggregate recording and access-log sampling after the request handler exits.
	// IngressRejectQueryAPIKeyDeprecated identifies deprecated query credential
	// supplied.
	IngressRejectQueryAPIKeyDeprecated IngressRejectReason = "query_api_key_deprecated"
	// IngressRejectAPIKeyRequired identifies required credential is absent.
	IngressRejectAPIKeyRequired IngressRejectReason = "api_key_required"
	// IngressRejectInvalidAPIKey identifies credential fails authentication
	// validation.
	IngressRejectInvalidAPIKey IngressRejectReason = "invalid_api_key"
	// IngressRejectAPIKeyDisabled identifies key is administratively disabled.
	IngressRejectAPIKeyDisabled IngressRejectReason = "api_key_disabled"
	// IngressRejectIPRestricted identifies client address fails key restrictions.
	IngressRejectIPRestricted IngressRejectReason = "ip_restricted"
	// IngressRejectUserInactive identifies key user is inactive.
	IngressRejectUserInactive IngressRejectReason = "user_inactive"
	// IngressRejectGroupDeleted identifies bound group is missing or deleted.
	IngressRejectGroupDeleted IngressRejectReason = "group_deleted"
	// IngressRejectGroupDisabled identifies bound group is disabled.
	IngressRejectGroupDisabled IngressRejectReason = "group_disabled"
	// IngressRejectGroupNotAllowed identifies user cannot access selected group.
	IngressRejectGroupNotAllowed IngressRejectReason = "group_not_allowed"
	// IngressRejectGroupUnassigned identifies key lacks required group assignment.
	IngressRejectGroupUnassigned IngressRejectReason = "group_unassigned"
	// IngressRejectInvalidAuthRateLimited identifies client invalid-auth bucket is
	// blocked.
	IngressRejectInvalidAuthRateLimited IngressRejectReason = "invalid_auth_rate_limited"
	// IngressRejectAPIKeyAuthOverloaded identifies authentication lookup capacity
	// is exhausted.
	IngressRejectAPIKeyAuthOverloaded IngressRejectReason = "api_key_auth_overloaded"

	// ingressRejectReasonContextKey stores a typed admission marker in Gin context
	// for Logger and RequestLogger to inspect after handling.
	ingressRejectReasonContextKey = "ingress_reject_reason"
)

// activeIngressRejectRecorder atomically publishes the optional aggregate
// recorder holder read by access logging after requests finish.
var activeIngressRejectRecorder atomic.Pointer[ingressRejectRecorderHolder]

// IngressRejectReason classifies expected admission failures for request
// markers and aggregate recording.
type IngressRejectReason string

// IngressRejectRecorder consumes aggregate admission records produced by
// completed request logging.
type IngressRejectRecorder interface {
	// RecordIngressReject receives the rejection category, route classification,
	// normalized client bucket, and available ownership IDs. Implementations
	// support concurrent request calls; zero IDs indicate ownership that admission
	// did not establish.
	RecordIngressReject(reason, routeFamily, protocol, clientIP string, userID, apiKeyID int64)
}

// ingressRejectRecorderHolder wraps one installed interface value for atomic
// recorder publication.
type ingressRejectRecorderHolder struct {
	// recorder remains available to requests that loaded this holder before
	// replacement. The installer keeps its resources usable until those calls
	// finish.
	recorder IngressRejectRecorder
}

// InvalidAuthClientKey returns the normalized security client bucket used by
// invalid-authentication admission checks. It uses session-binding or
// trusted-proxy address resolution, then groups IPv6 addresses by /64 and
// substitutes the invalid-address sentinel.
func InvalidAuthClientKey(c *gin.Context) string {
	return normalizeIngressRejectIP(identityhttp.SecurityClientIP(c))
}

// SetIngressRejectRecorder replaces the process-wide aggregate recorder, with
// nil disabling future loaded calls. Replacement is atomic; a request that
// already loaded the previous holder may still call it, so the installer
// manages recorder lifetime and concurrency.
func SetIngressRejectRecorder(recorder IngressRejectRecorder) {
	// Publish the optional holder in one atomic operation. A loaded previous
	// holder remains usable by its request even after later requests see
	// replacement.
	if recorder == nil {
		activeIngressRejectRecorder.Store(nil)
		return
	}
	activeIngressRejectRecorder.Store(&ingressRejectRecorderHolder{recorder: recorder})
}

// MarkIngressRejected attaches a nonempty admission reason for later request
// logging. Nil contexts and empty reasons are ignored; repeated calls replace
// the marker, and aggregate recording occurs when Logger finishes downstream
// execution.
func MarkIngressRejected(c *gin.Context, reason IngressRejectReason) {
	if c == nil || reason == "" {
		return
	}
	c.Set(ingressRejectReasonContextKey, reason)
}

// GetIngressRejectReason returns the stored admission reason and whether it is
// a nonempty typed marker. Nil contexts, missing entries, wrong types, and
// empty markers report false.
func GetIngressRejectReason(c *gin.Context) (IngressRejectReason, bool) {
	if c == nil {
		return "", false
	}
	value, exists := c.Get(ingressRejectReasonContextKey)
	if !exists {
		return "", false
	}
	reason, ok := value.(IngressRejectReason)
	return reason, ok && reason != ""
}

// recordIngressReject sends one completed rejection to the currently loaded
// recorder when request context is available. It prefers the admitted API key,
// then the early-auth diagnostic key, and leaves unavailable ownership IDs at
// zero. Recorder replacement can occur while this loaded call finishes.
func recordIngressReject(c *gin.Context, reason IngressRejectReason) {
	holder := activeIngressRejectRecorder.Load()
	if holder == nil || holder.recorder == nil || c == nil || c.Request == nil {
		return
	}
	routeFamily, protocol := ingressRejectRoute(c.Request.URL.Path)
	clientIP := normalizeIngressRejectIP(identityhttp.SecurityClientIP(c))
	var userID, apiKeyID int64
	if apiKey, ok := keyhttp.GetAPIKeyFromContext(c); ok && apiKey != nil {
		apiKeyID = apiKey.ID
		if apiKey.User != nil {
			userID = apiKey.User.ID
		}
	} else if apiKey, ok := keyhttp.GetOpsFallbackAPIKey(c); ok && apiKey != nil {
		// Early authentication can reject a loaded key before publishing the admitted
		// key. Its diagnostic copy still provides ownership for the rejection
		// aggregate.
		apiKeyID = apiKey.ID
		if apiKey.User != nil {
			userID = apiKey.User.ID
		}
	}
	holder.recorder.RecordIngressReject(string(reason), routeFamily, protocol, clientIP, userID, apiKeyID)
}

// normalizeIngressRejectIP returns canonical client-bucket text from a trimmed
// IP literal. Invalid values map to 0.0.0.0, mapped IPv4 uses IPv4 text, and
// IPv6 shares its masked /64 address.
func normalizeIngressRejectIP(raw string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		// An unusable address still needs a bounded bucket for rejection counting.
		// Use one sentinel rather than retaining arbitrary client-supplied text as
		// keys.
		return "0.0.0.0"
	}
	// Canonicalize mapped IPv4 before selecting the IPv6 bucket so both
	// representations of one IPv4 address share the same rejection key.
	addr = addr.Unmap()
	if addr.Is6() {
		// Addresses in one IPv6 /64 share a rejection bucket. This limits address
		// variation within that network from producing separate
		// invalid-authentication counters.
		return netip.PrefixFrom(addr, 64).Masked().Addr().String()
	}
	return addr.String()
}

// ingressRejectRoute returns a coarse route family and protocol for rejection
// aggregation. It trims and lowercases paths, gives dedicated platform prefixes
// precedence over endpoint fragments, and uses other/gateway when no
// classification matches.
func ingressRejectRoute(path string) (string, string) {
	path = strings.ToLower(strings.TrimSpace(path))
	switch {
	case strings.HasPrefix(path, "/antigravity/v1beta"):
		return "antigravity", "google"
	case strings.HasPrefix(path, "/v1beta"):
		return "gemini", "google"
	case strings.HasPrefix(path, "/backend-api/codex"):
		return "codex", "openai"
	case strings.HasPrefix(path, "/antigravity"):
		return "antigravity", "anthropic"
	case strings.Contains(path, "/messages"):
		return "messages", "anthropic"
	case strings.Contains(path, "/responses"):
		return "responses", "openai"
	case strings.Contains(path, "/chat/completions"):
		return "chat_completions", "openai"
	case strings.Contains(path, "/images"):
		return "images", "openai"
	case strings.Contains(path, "/videos"):
		return "videos", "openai"
	case strings.Contains(path, "/embeddings"):
		return "embeddings", "openai"
	case strings.Contains(path, "/models"):
		return "models", "openai"
	default:
		return "other", "gateway"
	}
}
