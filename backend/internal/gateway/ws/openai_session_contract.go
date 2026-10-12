package ws

import (
	"context"
	"time"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// OpenAIIngressHooks 定义入站 WS 每个 turn 的生命周期回调。
type OpenAIIngressHooks struct {
	ContextForTurn func(int) context.Context
	// ClientLifecycleContext 是加入 ingress 租约取消信号前的客户端 context。
	// 下行写入继承客户端断开和服务关闭信号，租约丢失则在当前帧完成后结束连接。
	ClientLifecycleContext context.Context
	// InitialRequestModel 是首帧分组映射前的请求模型，供会话缺省模型与策略范围使用。
	InitialRequestModel string
	// InitialTurnStartedAt 是首轮 response.create 被接受时的时间快照。
	InitialTurnStartedAt time.Time
	// MaxReasoningEffort 限制当前 WebSocket 会话中请求指定的推理强度。
	MaxReasoningEffort string
	// MaxReasoningEffortOverLimit 指定请求推理强度超限时降档或拒绝。
	MaxReasoningEffortOverLimit string
	// ReasoningEffortMappings 改写当前 WebSocket 会话中请求指定的推理强度。
	ReasoningEffortMappings []routing.ReasoningEffortMapping
	// ResolveRoutingModel 在提供商映射前逐轮把客户端模型 R 解析为分组映射模型 G。
	// payload 用于按分组映射后的完整请求判断该轮能力；返回错误时当前帧不得发送上游。
	ResolveRoutingModel func(turn int, requestedModel string, payload []byte) (string, error)
	// ResolveFastModePolicy 在每轮读取 API Key 的当前 Fast 策略。
	ResolveFastModePolicy func(turn int) string
	// TurnStarted 报告每轮 response.create 的开始时刻；时间值应在策略处理前捕获。
	TurnStarted   func(turn int, startedAt time.Time)
	BeforeTurn    func(turn int) error
	BeforeRequest func(turn int, payload []byte, originalModel, previousResponseID string) ([]byte, error)
	// OnUpstreamError 在上游 WS 返回 error/failed 类事件时触发，用于记录 OpenAI cyber 等上游风控信号。
	OnUpstreamError func(turn int, originalModel string, statusCode int, responseBody []byte, message string)
	AfterTurn       func(capture OpenAITurnCapture)
}

// OpenAITurnCapture 描述一次 WS turn 完成后用于用量结算和错误处理的上下文。
type OpenAITurnCapture struct {
	Turn               int
	StartedAt          time.Time
	RequestBody        []byte
	OriginalModel      string
	PreviousResponseID string
	Result             *forwardcore.OpenAIResult
	Err                error
	PayloadSource      string
}
