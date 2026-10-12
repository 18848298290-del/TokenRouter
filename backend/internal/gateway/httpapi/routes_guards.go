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

// GatewayErrorWriter 定义网关错误响应格式（不同协议使用不同格式）。
type GatewayErrorWriter func(c *gin.Context, status int, message string)

// RouteAccess 是路由门禁从当前认证请求取得的数据。
type RouteAccess struct {
	HasGroup         bool
	Composite        bool
	AllowedProtocols []wireprotocol.ProtocolID
}

// RouteMiddleware 在 app 一次装配，路由不构造认证、配置或观测服务。
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

// RouteGuards 使用路由表和当前请求的认证数据执行门禁。
type RouteGuards struct{ options RouteMiddleware }

type GroupClientProtocolErrorFormat string

// AnthropicErrorWriter 按 Anthropic API 规范输出错误。
func AnthropicErrorWriter(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"type":  "error",
		"error": gin.H{"type": "permission_error", "message": message},
	})
}

// GoogleErrorWriter 按 Google API 规范输出错误。
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

// RequireGroupClientProtocol 在进入业务处理器前执行分组协议准入检查。
func (g *RouteGuards) RequireGroupClientProtocol(protocol wireprotocol.ProtocolID, format GroupClientProtocolErrorFormat) gin.HandlerFunc {
	return func(c *gin.Context) {
		if g.EnforceGroupClientProtocol(c, protocol, format) {
			c.Next()
		}
	}
}

// WithGroupClientProtocol 把协议门禁包在已完成路径校验的终端处理器外层。
func (g *RouteGuards) WithGroupClientProtocol(protocol wireprotocol.ProtocolID, format GroupClientProtocolErrorFormat, next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if g.EnforceGroupClientProtocol(c, protocol, format) {
			next(c)
		}
	}
}

// EnforceGroupClientProtocol 执行检查并在拒绝时写入协议原生错误。
func (g *RouteGuards) EnforceGroupClientProtocol(c *gin.Context, protocol wireprotocol.ProtocolID, format GroupClientProtocolErrorFormat) bool {
	access := g.options.Access(c)
	if protocol == wireprotocol.ProtocolOpenAIResponses && RouteProtocol(c.Request.Method, c.Request.URL.Path) == wireprotocol.ProtocolResponsesCompact {
		protocol = wireprotocol.ProtocolResponsesCompact
	}
	g.options.InstallClientProtocol(c, protocol)
	group := routing.Group{AllowedProtocols: access.AllowedProtocols}
	if !access.HasGroup || group.AllowsClientProtocol(protocol) {
		// Messages 先读取正文识别客户端，其他协议在捕获任务、资金或会话归属前完成分组回退。
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

// RequireGeminiGenerateContentProtocol 只门禁 Gemini 的三个文本生成 POST 动作。
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

// ExtendedRouteProtocol 为非文本入口及别名统一命名；已有任务操作不受新建开关影响。
func ExtendedRouteProtocol(method, path string) wireprotocol.ProtocolID {
	protocol := RouteProtocol(method, path)
	// Compact 在 Responses 通配路由完成路径校验后检查，无效路径返回路径错误。
	if protocol == wireprotocol.ProtocolResponsesCompact {
		return ""
	}
	return protocol
}

// RouteProtocol 按完整别名前缀匹配并移除前缀。
func RouteProtocol(method, path string) wireprotocol.ProtocolID {
	for _, prefix := range []string{"/backend-api/codex", "/v1"} {
		if strings.HasPrefix(path, prefix+"/") {
			path = strings.TrimPrefix(path, prefix)
		}
	}
	protocol, _ := ProtocolForRoute(method, path)
	return protocol
}

// RequireExtendedProtocol 按入口协议检查分组许可，已有资源操作仍保持独立归属检查。
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
