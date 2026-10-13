package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	wireprotocol "github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Protocol admission runs after authentication and selected-group assignment,
// before endpoint execution. Extended route metadata determines the group
// control for new work, while Responses Compact waits until its wildcard path
// has been validated. Existing task resource operations continue to their
// ownership handlers.
//
// The guard installs request protocol state before checking permissions.
// Accepted non-Messages requests resolve any client restriction fallback before
// later ownership and funding captures. Messages needs parsed body information
// to identify its client, so its entry owns that later step. Rejected protocols
// record a local policy limit and abort with the inbound error envelope before
// provider selection.

// These values identify local protocol denial observations and choose the
// client error envelope used when group admission rejects an endpoint.
const (
	RouteLimitLocalPolicyDenied = "local_policy_denied" // Ops local protocol denial reason

	GroupClientProtocolErrorAnthropic GroupClientProtocolErrorFormat = "anthropic" // Anthropic client error envelope
	GroupClientProtocolErrorOpenAI    GroupClientProtocolErrorFormat = "openai"    // OpenAI client error envelope
	GroupClientProtocolErrorGoogle    GroupClientProtocolErrorFormat = "google"    // Google client error envelope
)

// GatewayErrorWriter writes an error in a client protocol's format.
type GatewayErrorWriter func(c *gin.Context, status int, message string)

// RouteAccess holds authenticated request data used by route admission.
type RouteAccess struct {
	HasGroup         bool                      // authenticated selected group present
	Composite        bool                      // authenticated composite Key flag
	AllowedProtocols []wireprotocol.ProtocolID // selected group protocol permissions
}

// RouteMiddleware contains authentication, settings, and observation callbacks assembled by app.
type RouteMiddleware struct {
	// ModelAttributes optionally installs request-local attribute loading before
	// route middleware runs. Nil leaves this optional step unregistered while the
	// remaining route callbacks retain their order.
	ModelAttributes gin.HandlerFunc
	// ClientGroupFallback resolves a permitted replacement key and funding
	// subscription. A missing resolver is surfaced as ErrClaudeCodeOnly when
	// client restriction fallback is requested.
	ClientGroupFallback ClientGroupFallbackResolver
	// APIKeyAuth and GoogleAPIKeyAuth perform their respective credential
	// admission. BodyLimit bounds general requests; TextBodyLimit bounds text
	// payloads. ClientRequestID installs request correlation, OpsErrorLogger
	// records request errors, and EndpointNormalization installs observed
	// endpoints. These handlers are assembled before route registration and reused
	// in the specified order for protocol aliases.
	APIKeyAuth, GoogleAPIKeyAuth, BodyLimit, TextBodyLimit, ClientRequestID, OpsErrorLogger, EndpointNormalization gin.HandlerFunc
	// RequireGroupAnthropic and RequireGroupGoogle check group assignment using
	// their client error envelopes. ForceAntigravity installs the dedicated route
	// platform constraint before authentication and provider selection.
	RequireGroupAnthropic, RequireGroupGoogle, ForceAntigravity gin.HandlerFunc
	// Access, InstallClientProtocol and ObserveBusinessLimit are required
	// synchronous callbacks. They read authenticated state, publish the checked
	// protocol, and observe rejection before the route handler can execute.
	// required authenticated route-state reader
	Access func(*gin.Context) RouteAccess
	// request forced-platform reader
	ForcedPlatform func(*gin.Context) (string, bool)
	// required request protocol installer
	InstallClientProtocol func(*gin.Context, wireprotocol.ProtocolID)
	// required local-denial observation callback
	ObserveBusinessLimit func(*gin.Context, string)
}

// RouteGuards checks route admission against authenticated request data.
type RouteGuards struct {
	options RouteMiddleware // bound route admission callbacks
}

// GroupClientProtocolErrorFormat chooses the client envelope for group
// admission errors.
type GroupClientProtocolErrorFormat string

// AnthropicErrorWriter writes a permission failure using the Anthropic JSON
// envelope.
func AnthropicErrorWriter(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"type":  "error",
		"error": gin.H{"type": "permission_error", "message": message},
	})
}

// GoogleErrorWriter writes a Google envelope with the supplied HTTP status. The
// status name comes from HTTPStatusToGoogleStatus, allowing admission callers
// to supply one status and retain Google's protocol-specific presentation.
func GoogleErrorWriter(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"code":    status,
			"message": message,
			"status":  HTTPStatusToGoogleStatus(status),
		},
	})
}

// NewRouteGuards retains middleware callbacks used by HTTP route admission.
func NewRouteGuards(options RouteMiddleware) *RouteGuards { return &RouteGuards{options: options} }

// RequireGroupClientProtocol builds middleware for the supplied client protocol
// and error format. It runs the next Gin handlers only after
// EnforceGroupClientProtocol accepts; rejected paths already have their
// protocol response and abort state.
func (g *RouteGuards) RequireGroupClientProtocol(protocol wireprotocol.ProtocolID, format GroupClientProtocolErrorFormat) gin.HandlerFunc {
	return func(c *gin.Context) {
		if g.EnforceGroupClientProtocol(c, protocol, format) {
			c.Next()
		}
	}
}

// WithGroupClientProtocol wraps a terminal handler with group protocol
// admission. Callers place path validation before invoking this wrapper;
// acceptance calls next directly and denial leaves the enforcement response and
// abort state in place.
func (g *RouteGuards) WithGroupClientProtocol(protocol wireprotocol.ProtocolID, format GroupClientProtocolErrorFormat, next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if g.EnforceGroupClientProtocol(c, protocol, format) {
			next(c)
		}
	}
}

// EnforceGroupClientProtocol installs the effective client protocol and checks
// the authenticated group's permission. A Responses Compact route is checked
// under its separate protocol. A missing group skips this protocol restriction
// because group assignment has its own middleware.
//
// Accepted non-Messages entries resolve client group fallback before task,
// funding and session capture; Messages defers that step until its body
// identifies the client. Fallback errors abort with the selected protocol
// envelope. A direct protocol denial records local policy limitation and writes
// 403 before any terminal handler executes, so it cannot start provider
// selection or generation.
func (g *RouteGuards) EnforceGroupClientProtocol(c *gin.Context, protocol wireprotocol.ProtocolID, format GroupClientProtocolErrorFormat) bool {
	access := g.options.Access(c)
	// Select Compact's separate permission after path validation so an allowed
	// Responses root cannot implicitly admit a disabled compaction operation.
	if protocol == wireprotocol.ProtocolOpenAIResponses && RouteProtocol(c.Request.Method, c.Request.URL.Path) == wireprotocol.ProtocolResponsesCompact {
		protocol = wireprotocol.ProtocolResponsesCompact
	}
	g.options.InstallClientProtocol(c, protocol)
	group := routing.Group{AllowedProtocols: access.AllowedProtocols}
	if !access.HasGroup || group.AllowsClientProtocol(protocol) {
		// Resolve non-Messages fallback before the terminal handler captures ownership
		// and funding; Messages delays this until body parsing can identify the client.
		if protocol != wireprotocol.ProtocolAnthropicMessages {
			key, ok := EffectiveAPIKey(c)
			if !ok {
				key, _ = keyhttp.GetAPIKeyFromContext(c)
			}
			if _, err := resolveClientGroupForRequest(c, g, key, protocol); err != nil {
				writeClientGroupFallbackError(c, err, func(c *gin.Context, status int, code, message string) {
					switch format {
					case GroupClientProtocolErrorGoogle:
						WriteGoogleError(c, status, message)
					case GroupClientProtocolErrorOpenAI:
						c.JSON(status, gin.H{"error": gin.H{"message": message, "type": code, "code": code}})
					default:
						WriteAnthropicError(c, status, code, "", message)
					}
				})
				c.Abort()
				return false
			}
		}
		return true
	}
	g.options.ObserveBusinessLimit(c, RouteLimitLocalPolicyDenied)

	message := groupClientProtocolDeniedMessage(protocol)
	switch format {
	case GroupClientProtocolErrorGoogle:
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"code":    http.StatusForbidden,
				"message": message,
				"status":  "PERMISSION_DENIED",
			},
		})
	case GroupClientProtocolErrorOpenAI:
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"message": message,
				"type":    "permission_error",
				"param":   nil,
				"code":    "protocol_not_allowed",
			},
		})
	default:
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "permission_error",
				"message": message,
			},
		})
	}
	return false
}

// groupClientProtocolDeniedMessage returns the protocol-specific group
// rejection message when defined. Messages, Responses, Chat and Gemini have
// dedicated wording; other protocols receive the shared client-protocol
// rejection text.
func groupClientProtocolDeniedMessage(protocol wireprotocol.ProtocolID) string {
	switch protocol {
	case wireprotocol.ProtocolAnthropicMessages:
		return "This group does not allow Anthropic Messages requests"
	case wireprotocol.ProtocolOpenAIResponses:
		return "This group does not allow OpenAI Responses requests"
	case wireprotocol.ProtocolOpenAIChatCompletions:
		return "This group does not allow OpenAI Chat Completions requests"
	case wireprotocol.ProtocolGeminiGenerateContent:
		return "This group does not allow Gemini GenerateContent requests"
	default:
		return "This group does not allow the requested client protocol"
	}
}

// RequireGeminiGenerateContentProtocol gates generateContent,
// streamGenerateContent and countTokens model actions under Gemini text
// permission. Malformed or unrelated actions continue to their handler for
// action validation; this middleware extracts the final colon or slash
// component.
func (g *RouteGuards) RequireGeminiGenerateContentProtocol(c *gin.Context) {
	rest := strings.TrimSpace(strings.TrimPrefix(c.Param("modelAction"), "/"))
	separator := strings.LastIndexAny(rest, ":/")
	if separator <= 0 || separator == len(rest)-1 {
		c.Next()
		return
	}
	switch rest[separator+1:] {
	case "generateContent", "streamGenerateContent", "countTokens":
		g.RequireGroupClientProtocol(wireprotocol.ProtocolGeminiGenerateContent, GroupClientProtocolErrorGoogle)(c)
	default:
		c.Next()
	}
}

// ExtendedRouteProtocol identifies extended-route admission after alias
// normalization. Responses Compact returns empty here because its gate follows
// wildcard suffix validation; other matched extended routes retain their
// catalogue ID and unknown routes remain empty.
func ExtendedRouteProtocol(method, path string) wireprotocol.ProtocolID {
	protocol := RouteProtocol(method, path)
	// Defer Compact admission to the validated wildcard handler so malformed
	// suffixes receive a path error before any separate compaction permission check.
	if protocol == wireprotocol.ProtocolResponsesCompact {
		return ""
	}
	return protocol
}

// RouteProtocol removes complete Codex and /v1 alias prefixes before matching
// extended metadata. Prefix removal requires a following slash, and method/path
// spelling otherwise stays as supplied; dedicated protocol handlers handle
// routes without extended metadata.
func RouteProtocol(method, path string) wireprotocol.ProtocolID {
	for _, prefix := range []string{"/backend-api/codex", "/v1"} {
		if strings.HasPrefix(path, prefix+"/") {
			path = strings.TrimPrefix(path, prefix)
		}
	}
	protocol, _ := ProtocolForRoute(method, path)
	return protocol
}

// RequireExtendedProtocol applies OpenAI-format group admission when extended
// metadata identifies a route. Unknown or deferred Compact routes continue the
// Gin chain; resource handlers still perform their separate ownership checks
// before serving existing work.
func (g *RouteGuards) RequireExtendedProtocol(c *gin.Context) {
	protocol := ExtendedRouteProtocol(c.Request.Method, c.Request.URL.Path)
	if protocol == "" || g.EnforceGroupClientProtocol(c, protocol, GroupClientProtocolErrorOpenAI) {
		c.Next()
	}
}

// ResolveClientGroup delegates fallback authorization using the bound resolver
// and supplied context. An absent resolver returns ErrClaudeCodeOnly with no
// replacement key or subscription, so callers handle unresolved client
// restriction through their protocol error path.
func (g *RouteGuards) ResolveClientGroup(ctx context.Context, key *apikey.APIKey, source wireprotocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
	if g.options.ClientGroupFallback == nil {
		return nil, nil, routing.ErrClaudeCodeOnly
	}
	return g.options.ClientGroupFallback(ctx, key, source)
}
