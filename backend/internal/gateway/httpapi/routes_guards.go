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

const (
	RouteLimitLocalPolicyDenied = "local_policy_denied"

	GroupClientProtocolErrorAnthropic GroupClientProtocolErrorFormat = "anthropic"
	GroupClientProtocolErrorOpenAI    GroupClientProtocolErrorFormat = "openai"
	GroupClientProtocolErrorGoogle    GroupClientProtocolErrorFormat = "google"
)

// GatewayErrorWriter writes an error in a client protocol's format.
type GatewayErrorWriter func(c *gin.Context, status int, message string)

// RouteAccess holds authenticated request data used by route admission.
type RouteAccess struct {
	HasGroup         bool
	Composite        bool
	AllowedProtocols []wireprotocol.ProtocolID
}

// RouteMiddleware contains authentication, settings, and observation callbacks assembled by app.
type RouteMiddleware struct {
	ModelAttributes                                                                                                gin.HandlerFunc
	ClientGroupFallback                                                                                            ClientGroupFallbackResolver
	APIKeyAuth, GoogleAPIKeyAuth, BodyLimit, TextBodyLimit, ClientRequestID, OpsErrorLogger, EndpointNormalization gin.HandlerFunc
	RequireGroupAnthropic, RequireGroupGoogle, ForceAntigravity                                                    gin.HandlerFunc
	Access                                                                                                         func(*gin.Context) RouteAccess
	ForcedPlatform                                                                                                 func(*gin.Context) (string, bool)
	InstallClientProtocol                                                                                          func(*gin.Context, wireprotocol.ProtocolID)
	ObserveBusinessLimit                                                                                           func(*gin.Context, string)
}

// RouteGuards checks route admission against authenticated request data.
type RouteGuards struct{ options RouteMiddleware }

type GroupClientProtocolErrorFormat string

// AnthropicErrorWriter writes an Anthropic permission error.
func AnthropicErrorWriter(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"type":  "error",
		"error": gin.H{"type": "permission_error", "message": message},
	})
}

// GoogleErrorWriter writes a Google error with the mapped status name.
func GoogleErrorWriter(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"code":    status,
			"message": message,
			"status":  HTTPStatusToGoogleStatus(status),
		},
	})
}

func NewRouteGuards(options RouteMiddleware) *RouteGuards { return &RouteGuards{options: options} }

// RequireGroupClientProtocol checks group protocol admission before the endpoint handler.
func (g *RouteGuards) RequireGroupClientProtocol(protocol wireprotocol.ProtocolID, format GroupClientProtocolErrorFormat) gin.HandlerFunc {
	return func(c *gin.Context) {
		if g.EnforceGroupClientProtocol(c, protocol, format) {
			c.Next()
		}
	}
}

// WithGroupClientProtocol wraps a terminal handler with admission after path validation.
func (g *RouteGuards) WithGroupClientProtocol(protocol wireprotocol.ProtocolID, format GroupClientProtocolErrorFormat, next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if g.EnforceGroupClientProtocol(c, protocol, format) {
			next(c)
		}
	}
}

// EnforceGroupClientProtocol checks admission and writes a protocol error on rejection.
func (g *RouteGuards) EnforceGroupClientProtocol(c *gin.Context, protocol wireprotocol.ProtocolID, format GroupClientProtocolErrorFormat) bool {
	access := g.options.Access(c)
	if protocol == wireprotocol.ProtocolOpenAIResponses && RouteProtocol(c.Request.Method, c.Request.URL.Path) == wireprotocol.ProtocolResponsesCompact {
		protocol = wireprotocol.ProtocolResponsesCompact
	}
	g.options.InstallClientProtocol(c, protocol)
	group := routing.Group{AllowedProtocols: access.AllowedProtocols}
	if !access.HasGroup || group.AllowsClientProtocol(protocol) {
		// Messages needs the body to identify the client; other protocols resolve group fallback before capturing
		// task, funding, or session ownership.
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

// RequireGeminiGenerateContentProtocol gates Gemini's three text-generation POST actions.
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

// ExtendedRouteProtocol identifies non-text endpoints and aliases, with separate IDs for existing-task operations.
func ExtendedRouteProtocol(method, path string) wireprotocol.ProtocolID {
	protocol := RouteProtocol(method, path)
	// Compact admission follows wildcard-path validation so invalid paths receive a path error first.
	if protocol == wireprotocol.ProtocolResponsesCompact {
		return ""
	}
	return protocol
}

// RouteProtocol removes complete alias prefixes before matching a route.
func RouteProtocol(method, path string) wireprotocol.ProtocolID {
	for _, prefix := range []string{"/backend-api/codex", "/v1"} {
		if strings.HasPrefix(path, prefix+"/") {
			path = strings.TrimPrefix(path, prefix)
		}
	}
	protocol, _ := ProtocolForRoute(method, path)
	return protocol
}

// RequireExtendedProtocol checks group permission for the endpoint; resource handlers also check ownership.
func (g *RouteGuards) RequireExtendedProtocol(c *gin.Context) {
	protocol := ExtendedRouteProtocol(c.Request.Method, c.Request.URL.Path)
	if protocol == "" || g.EnforceGroupClientProtocol(c, protocol, GroupClientProtocolErrorOpenAI) {
		c.Next()
	}
}

func (g *RouteGuards) ResolveClientGroup(ctx context.Context, key *apikey.APIKey, source wireprotocol.ProtocolID) (*apikey.APIKey, *billing.UserSubscription, error) {
	if g.options.ClientGroupFallback == nil {
		return nil, nil, routing.ErrClaudeCodeOnly
	}
	return g.options.ClientGroupFallback(ctx, key, source)
}
