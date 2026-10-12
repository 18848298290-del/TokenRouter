package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/moderation"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	protocolgemini "github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

var GeminiCLITmpDirRegex = regexp.MustCompile(`/\.gemini/tmp/([A-Fa-f0-9]{64})`)

type GeminiNativeOptions struct{ MaxSwitches int }

type GeminiNativeBackend interface {
	Access(*gin.Context) (*apikey.APIKey, bool)
	HasForcedPlatform(*gin.Context) bool
	SafeModelSegment(string) bool
	ObserveRequest(*gin.Context, string, bool)
	ObserveEndpoint(*gin.Context, bool)
	Moderate(*gin.Context, *zap.Logger, *apikey.APIKey, authctx.AuthSubject, string, []byte) *moderation.Decision
	Plan(context.Context, *apikey.APIKey, string) routing.RoutePlan
	BindPlan(*gin.Context, routing.RoutePlan)
	BindErrors(*gin.Context)
	Eligibility(context.Context, *apikey.APIKey, *billing.UserSubscription) error
	Isolate(context.Context, *apikey.APIKey, int64, string) error
	CachedSession(context.Context, *int64, string) (int64, error)
	Prefetch(*gin.Context, int64, int64)
	DigestChain(*protocolgemini.GeminiRequest) string
	PrefixHash(int64, int64, string, string, string, string) string
	FindSession(context.Context, int64, string, string) (string, int64, string, bool)
	DigestSessionKey(string, string) string
	BindSticky(context.Context, *int64, string, int64) error
	FailoverObservation(context.Context, string, map[string]any)
}
type GeminiNativeHandler struct {
	executor execution.Executor
	RequestLifetime

	options     GeminiNativeOptions
	backend     GeminiNativeBackend
	prompt      MessagesPrompt
	concurrency *ConcurrencyHelper
	newID       func() string
}

type GeminiPathParseError struct{ message string }

func NewGeminiNativeHandler(options GeminiNativeOptions, backend GeminiNativeBackend, prompt MessagesPrompt, concurrency *ConcurrencyHelper, newID func() string, executor execution.Executor) *GeminiNativeHandler {
	return &GeminiNativeHandler{executor: executor, options: options, backend: backend, prompt: prompt, concurrency: concurrency, newID: newID}
}

// WriteGoogleError 写出 Google 格式的 JSON 错误。
func WriteGoogleError(c *gin.Context, status int, message string) {
	status, message = modelAttributeError(c, status, message)
	c.JSON(status, gin.H{"error": gin.H{"code": status, "message": message, "status": HTTPStatusToGoogleStatus(status)}})
}

func geminiNativeIsolationError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, session.ErrSessionIsolationConflict) {
		WriteGoogleError(c, 403, session.SessionIsolationConflictMessage)
	} else {
		WriteGoogleError(c, 503, "Service temporarily unavailable")
	}
	return true
}

func (e *GeminiPathParseError) Error() string { return e.message }

// @project-doc docs/interfaces/gemini_upstream.md#gemini_protocol_dispatch
func (h *GeminiNativeHandler) GeminiV1BetaModels(c *gin.Context) {
	done, accepted := h.BeginRequest(c, "google")
	if !accepted {
		return
	}
	defer done()

	apiKey, ok := h.backend.Access(c)
	if !ok || apiKey == nil {
		WriteGoogleError(c, http.StatusUnauthorized, "Invalid API key")
		return
	}
	authSubject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		WriteGoogleError(c, http.StatusInternalServerError, "User context not found")
		return
	}
	reqLog := RequestLogger(
		c,
		"handler.gemini_v1beta.models",
		zap.Int64("user_id", authSubject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	// 原生入口同样只检查分组协议，实际供应商由提供商选择确定。
	if apiKey.Group == nil || !apiKey.Group.AllowsClientProtocol(protocol.ProtocolGeminiGenerateContent) {
		WriteGoogleError(c, http.StatusForbidden, "This group does not allow Gemini GenerateContent requests")
		return
	}

	modelName, action, err := ParseGeminiModelAction(strings.TrimPrefix(c.Param("modelAction"), "/"))
	if err != nil {
		WriteGoogleError(c, http.StatusNotFound, err.Error())
		return
	}
	// URL 里的模型名最终会被拼进上游 /v1beta/models/{model}:{action}，
	// 转发前校验请求片段的合规性。
	if !h.backend.SafeModelSegment(modelName) {
		WriteGoogleError(c, http.StatusBadRequest, "Invalid model in URL")
		return
	}

	stream := action == "streamGenerateContent"
	reqLog = reqLog.With(zap.String("model", modelName), zap.String("action", action), zap.Bool("stream", stream))

	body, err := ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := compatibleMaxBytesError(err); ok {
			WriteGoogleError(c, http.StatusRequestEntityTooLarge, BodyTooLargeMessage(maxErr.Limit))
			return
		}
		WriteGoogleError(c, http.StatusBadRequest, "Failed to read request body")
		return
	}
	if len(body) == 0 {
		WriteGoogleError(c, http.StatusBadRequest, "Request body is empty")
		return
	}

	apiKey, err = resolveClientGroupForRequest(c, h.backend, apiKey, protocol.ProtocolGeminiGenerateContent)
	if err != nil {
		writeClientGroupFallbackError(c, err, func(c *gin.Context, status int, _, message string) { WriteGoogleError(c, status, message) })
		return
	}
	h.backend.ObserveRequest(c, modelName, stream)
	h.backend.ObserveEndpoint(c, stream)
	// 在内容审计、会话 hash 计算和转发前替换用户提示词，审计与转发使用同一请求体。
	body = h.prompt.ApplyUserPromptReplacementToBody(c.Request.Context(), body, "gemini")

	if decision := h.backend.Moderate(c, reqLog, apiKey, authSubject, modelName, body); decision != nil && decision.Blocked {
		WriteGoogleError(c, ModerationHTTPStatus(decision), decision.Message)
		return
	}

	// 解析分组模型映射
	// 当前分组和分组映射结果进入独立计划，不改变原解析位置。
	groupMappingRoutePlan := h.backend.Plan(c.Request.Context(), apiKey, modelName)
	groupMapping := groupMappingRoutePlan.Mapping()
	h.backend.BindPlan(c, groupMappingRoutePlan)
	reqModel := modelName // 保存映射前的原始模型名
	if groupMapping.Mapped {
		modelName = groupMapping.MappedModel
	}

	// 读取订阅，未绑定订阅时结果为空。
	subscription, _ := SubscriptionFromContext(c)

	// Gemini 原生请求不发送 Claude 心跳帧。
	geminiConcurrency := NewConcurrencyHelper(h.concurrency.Service(), SSEPingFormatNone, 0)

	// 1）获取用户并发槽
	streamStarted := false
	h.backend.BindErrors(c)
	userReleaseFunc, err := geminiConcurrency.AcquireUserSlotWithWait(c, authSubject.UserID, authSubject.Concurrency, stream, &streamStarted)
	if err != nil {
		reqLog.Warn("gemini.user_slot_acquire_failed", zap.Error(err))
		WriteGoogleError(c, http.StatusTooManyRequests, err.Error())
		return
	}
	// 请求取消时释放槽位，长连接中断也会触发释放。
	userReleaseFunc = scheduler.WrapRelease(c.Request.Context(), scheduler.ReleaseOnCancel, userReleaseFunc)
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	// 2）等待后复查资金资格
	if err := h.backend.Eligibility(c.Request.Context(), apiKey, subscription); err != nil {
		reqLog.Info("gemini.billing_eligibility_check_failed", zap.Error(err))
		status, _, message, retryAfter := BillingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		WriteGoogleError(c, status, message)
		return
	}

	// 3）根据请求内容准备粘性会话选择
	// 优先使用 Gemini CLI 的会话标识（privileged-user-id + tmp 目录哈希）
	sessionHash := ExtractGeminiCLISessionHash(c, body)
	explicitGeminiCLISession := sessionHash != ""
	if sessionHash == "" {
		// Fallback: 使用通用的会话哈希生成逻辑（适用于其他客户端）
		parsedReq, _ := requeststate.ParseGatewayRequest(requeststate.NewRequestBodyRef(body), capability.PlatformGemini)
		if parsedReq != nil {
			parsedReq.SessionContext = &requeststate.SessionContext{
				ClientIP:  clientip.GetClientIP(c),
				UserAgent: c.GetHeader("User-Agent"),
				APIKeyID:  apiKey.ID,
			}
		}
		sessionHash = session.GenerateSessionHash(parsedReq, slog.Info)
	}
	sessionKey := sessionHash
	if sessionHash != "" {
		sessionKey = "gemini:" + sessionHash
	}
	if explicitGeminiCLISession {
		if err := h.backend.Isolate(c.Request.Context(), apiKey, authSubject.UserID, sessionKey); geminiNativeIsolationError(c, err) {
			return
		}
	}

	// 查询粘性会话绑定的提供商 ID（用于检测提供商切换）
	var sessionBoundProviderID int64
	if sessionKey != "" {
		sessionBoundProviderID, _ = h.backend.CachedSession(c.Request.Context(), apiKey.GroupID, sessionKey)
		if sessionBoundProviderID > 0 {
			prefetchedGroupID := int64(0)
			if apiKey.GroupID != nil {
				prefetchedGroupID = *apiKey.GroupID
			}
			h.backend.Prefetch(c, sessionBoundProviderID, prefetchedGroupID)
		}
	}

	// === Gemini 内容摘要会话 Fallback 逻辑 ===
	// 会话标识无效时（sessionBoundProviderID == 0），尝试按内容摘要链匹配。
	var geminiDigestChain string
	var geminiPrefixHash string
	var geminiSessionUUID string
	var matchedDigestChain string
	useDigestFallback := sessionBoundProviderID == 0

	if useDigestFallback {
		// 解析 Gemini 请求体
		var geminiReq protocolgemini.GeminiRequest
		if err := json.Unmarshal(body, &geminiReq); err == nil && len(geminiReq.Contents) > 0 {
			// 生成摘要链
			geminiDigestChain = h.backend.DigestChain(&geminiReq)
			if geminiDigestChain != "" {
				// 生成前缀 hash
				userAgent := c.GetHeader("User-Agent")
				clientIP := clientip.GetClientIP(c)
				geminiPrefixHash = h.backend.PrefixHash(
					authSubject.UserID,
					apiKey.ID,
					clientIP,
					userAgent,
					"",
					modelName,
				)

				// 查找会话
				foundUUID, foundProviderID, foundMatchedChain, found := h.backend.FindSession(
					c.Request.Context(),
					geminiGroupID(apiKey.GroupID),
					geminiPrefixHash,
					geminiDigestChain,
				)
				if found {
					matchedDigestChain = foundMatchedChain
					sessionBoundProviderID = foundProviderID
					geminiSessionUUID = foundUUID
					reqLog.Info("gemini.digest_fallback_matched",
						zap.String("session_uuid_prefix", GeminiShortPrefix(foundUUID, 8)),
						zap.Int64("provider_id", foundProviderID),
						zap.String("digest_chain", TruncateGeminiDigestChain(geminiDigestChain)),
					)

					// 关键：如果原 sessionKey 为空，使用 prefixHash + uuid 作为 sessionKey
					// 这样 SelectProviderWithLoadAwareness 的粘性会话逻辑会优先使用匹配到的提供商
					if sessionKey == "" {
						sessionKey = h.backend.DigestSessionKey(geminiPrefixHash, foundUUID)
					}
					_ = h.backend.BindSticky(c.Request.Context(), apiKey.GroupID, sessionKey, foundProviderID)
				} else {
					// 生成新的会话 UUID
					geminiSessionUUID = h.newID()
					// 为新会话也生成 sessionKey（用于后续请求的粘性会话）
					if sessionKey == "" {
						sessionKey = h.backend.DigestSessionKey(geminiPrefixHash, geminiSessionUUID)
					}
				}
			}
		}
	}

	// 判断是否真的绑定了粘性会话：有 sessionKey 且已经绑定到某个提供商
	hasBoundSession := sessionKey != "" && sessionBoundProviderID > 0

	request := execution.Request{
		Hints:       requeststate.ExecutionHintsFromContext(c.Request.Context()),
		Routing:     requeststate.RoutingStateFromContext(c.Request.Context()),
		Route:       groupMappingRoutePlan,
		UserID:      authSubject.UserID,
		Concurrency: authSubject.Concurrency,
		Stream:      stream,
		Body:        body,
		Model:       reqModel,
		Funding: execution.FundingState{
			Key:          apiKey,
			Subscription: subscription,
		},
		SessionHash: sessionKey,
		Text: execution.TextState{
			Kind:               execution.TextNativeGemini,
			BoundProviderID:    sessionBoundProviderID,
			HasBoundSession:    hasBoundSession,
			GeminiModel:        modelName,
			Action:             action,
			UseDigestFallback:  useDigestFallback,
			DigestChain:        geminiDigestChain,
			PrefixHash:         geminiPrefixHash,
			SessionUUID:        geminiSessionUUID,
			MatchedDigestChain: matchedDigestChain,
			SignatureState: requeststate.GeminiSignatureState{
				BoundProviderID: sessionBoundProviderID,
			},
			Mapping: groupMapping,
		},
	}
	output := &MessagesOutput{
		ResponseSink:  ResponseSink{Writer: c.Writer},
		HTTP:          c,
		Log:           reqLog,
		StreamStarted: &streamStarted,
		Concurrency:   geminiConcurrency,
	}
	_, _ = h.executor.Execute(c.Request.Context(), request, output)
}

func ParseGeminiModelAction(rest string) (model string, action string, err error) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", &GeminiPathParseError{"missing path"}
	}

	// 标准路径：{model}:{action}
	if i := strings.Index(rest, ":"); i > 0 && i < len(rest)-1 {
		return rest[:i], rest[i+1:], nil
	}

	// 兼容路径：{model}/{action}
	if i := strings.Index(rest, "/"); i > 0 && i < len(rest)-1 {
		return rest[:i], rest[i+1:], nil
	}

	return "", "", &GeminiPathParseError{"invalid model action path"}
}

func ExtractGeminiCLISessionHash(c *gin.Context, body []byte) string {
	// 1. 从请求体中提取 tmp 目录哈希
	match := GeminiCLITmpDirRegex.FindSubmatch(body)
	if len(match) < 2 {
		return "" // 没有找到 tmp 目录，不使用粘性会话
	}
	tmpDirHash := string(match[1])

	// 2. 提取 privileged-user-id
	privilegedUserID := strings.TrimSpace(c.GetHeader("x-gemini-api-privileged-user-id"))

	// 3. 组合生成最终的 session hash
	if privilegedUserID != "" {
		// 组合两个标识符：privileged-user-id + tmp 目录哈希
		combined := privilegedUserID + ":" + tmpDirHash
		hash := sha256.Sum256([]byte(combined))
		return hex.EncodeToString(hash[:])
	}

	// 如果没有 privileged-user-id，直接使用 tmp 目录哈希
	return tmpDirHash
}

func TruncateGeminiDigestChain(chain string) string {
	if len(chain) <= 50 {
		return chain
	}
	return chain[:50] + "..."
}

func GeminiShortPrefix(value string, n int) string {
	if n <= 0 || len(value) <= n {
		return value
	}
	return value[:n]
}

func geminiGroupID(groupID *int64) int64 {
	if groupID == nil {
		return 0
	}
	return *groupID
}
