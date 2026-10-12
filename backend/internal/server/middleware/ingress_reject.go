package middleware

import (
	"net/netip"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"

	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	identityhttp "github.com/TokenFlux/TokenRouter/internal/identity/httpapi"
)

const (
	IngressRejectQueryAPIKeyDeprecated  IngressRejectReason = "query_api_key_deprecated"
	IngressRejectAPIKeyRequired         IngressRejectReason = "api_key_required"
	IngressRejectInvalidAPIKey          IngressRejectReason = "invalid_api_key"
	IngressRejectAPIKeyDisabled         IngressRejectReason = "api_key_disabled"
	IngressRejectIPRestricted           IngressRejectReason = "ip_restricted"
	IngressRejectUserInactive           IngressRejectReason = "user_inactive"
	IngressRejectGroupDeleted           IngressRejectReason = "group_deleted"
	IngressRejectGroupDisabled          IngressRejectReason = "group_disabled"
	IngressRejectGroupNotAllowed        IngressRejectReason = "group_not_allowed"
	IngressRejectGroupUnassigned        IngressRejectReason = "group_unassigned"
	IngressRejectInvalidAuthRateLimited IngressRejectReason = "invalid_auth_rate_limited"
	IngressRejectAPIKeyAuthOverloaded   IngressRejectReason = "api_key_auth_overloaded"

	ingressRejectReasonContextKey = "ingress_reject_reason"
)

var activeIngressRejectRecorder atomic.Pointer[ingressRejectRecorderHolder]

// IngressRejectReason identifies expected admission failures, excluded from Ops request errors.
type IngressRejectReason string

type IngressRejectRecorder interface {
	RecordIngressReject(reason, routeFamily, protocol, clientIP string, userID, apiKeyID int64)
}

type ingressRejectRecorderHolder struct{ recorder IngressRejectRecorder }

// InvalidAuthClientKey normalizes the security client IP for invalid-authentication buckets.
func InvalidAuthClientKey(c *gin.Context) string {
	return normalizeIngressRejectIP(identityhttp.SecurityClientIP(c))
}

func SetIngressRejectRecorder(recorder IngressRejectRecorder) {
	if recorder == nil {
		activeIngressRejectRecorder.Store(nil)
		return
	}
	activeIngressRejectRecorder.Store(&ingressRejectRecorderHolder{recorder: recorder})
}

// MarkIngressRejected records a rejection before gateway execution.
func MarkIngressRejected(c *gin.Context, reason IngressRejectReason) {
	if c == nil || reason == "" {
		return
	}
	c.Set(ingressRejectReasonContextKey, reason)
}

// GetIngressRejectReason returns the admission rejection reason and whether a nonempty marker exists.
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
		apiKeyID = apiKey.ID
		if apiKey.User != nil {
			userID = apiKey.User.ID
		}
	}
	holder.recorder.RecordIngressReject(string(reason), routeFamily, protocol, clientIP, userID, apiKeyID)
}

func normalizeIngressRejectIP(raw string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return "0.0.0.0"
	}
	addr = addr.Unmap()
	if addr.Is6() {
		return netip.PrefixFrom(addr, 64).Masked().Addr().String()
	}
	return addr.String()
}

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
