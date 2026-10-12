package httpapi

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

const (
	nativeMessageStreamKey      = "native_message_stream_started"
	nativeMessageInterceptedKey = "native_message_intercepted"
)

// NativeMessageQueue connects the unified executor to the shared message queue service.
type NativeMessageQueue interface {
	AcquireWithWait(*gin.Context, int64, int, bool, *bool, time.Duration, *zap.Logger) (func(), error)
	ThrottleWithPing(*gin.Context, int64, int, bool, *bool, time.Duration, *zap.Logger) error
}

// BindNativeMessageStreamState shares queue-ping output state with the outer attempt.
func BindNativeMessageStreamState(c *gin.Context, started *bool) {
	c.Set(nativeMessageStreamKey, started)
}

func NativeMessageIntercepted(c *gin.Context) bool {
	value, _ := c.Get(nativeMessageInterceptedKey)
	intercepted, _ := value.(bool)
	return intercepted
}

// prepareNativeMessages applies warmup interception, message queueing, then Bedrock cleanup.
func (e *UnifiedTextExecutor) prepareNativeMessages(c *gin.Context, target *gatewayadapter.ExecutionProvider, body []byte) (*requeststate.ParsedRequest, func(), bool, error) {
	parsed, err := requeststate.ParseGatewayRequest(requeststate.NewRequestBodyRef(body), "anthropic")
	if err != nil {
		WriteAnthropicError(c, 400, "invalid_request_error", "", "Failed to parse request body")
		return nil, nil, false, err
	}
	key, ok := EffectiveAPIKey(c)
	if !ok {
		key, _ = keyhttp.GetAPIKeyFromContext(c)
	}
	if key != nil {
		parsed.GroupID = key.GroupID
	}
	requestedModel := ErrorRequestModel(c)
	if requestedModel == "" {
		requestedModel = parsed.Model
	}
	detection := DetectClaudeCodeRequest(c, body, parsed, clientmeta.IsHaikuProbe(requestedModel, parsed.MaxTokens))
	ctx := c.Request.Context()
	if detection.ClaudeCode {
		ctx = requeststate.SetClaudeCodeClient(ctx, true)
		if detection.Version != "" {
			ctx = requeststate.SetClaudeCodeVersion(ctx, detection.Version)
		}
	}
	c.Request = c.Request.WithContext(ctx)
	if target.View().IsInterceptWarmupEnabled() {
		intercepted := clientmeta.DetectInterceptType(body, requestedModel, parsed.MaxTokens, requeststate.IsClaudeCodeClient(ctx))
		if intercepted != clientmeta.InterceptTypeNone {
			c.Set(nativeMessageInterceptedKey, true)
			if parsed.Stream {
				WriteInterceptStream(c, parsed.Model, intercepted)
			} else {
				WriteInterceptResponse(c, parsed.Model, intercepted)
			}
			return parsed, nil, true, nil
		}
	}
	var release func()
	if e.MessageQueue != nil && target.View().IsAnthropicOAuthOrSetupToken() && requeststate.IsRealUserMessage(parsed) {
		mode := gatewayadapter.ExecutionRuntimeConfig(target).GetUserMsgQueueMode()
		if mode == "" {
			mode = e.MessageQueueMode
		}
		baseRPM := gatewayadapter.ExecutionRuntimeConfig(target).GetBaseRPM()
		started := c.Writer.Written()
		streamState := &started
		if value, ok := c.Get(nativeMessageStreamKey); ok {
			if pointer, ok := value.(*bool); ok && pointer != nil {
				streamState = pointer
			}
		}
		log := RequestLogger(c, "handler.gateway.messages", zap.Int64("provider_id", target.Record.ID))
		switch mode {
		case policy.MessageQueueSerialize:
			var queueErr error
			release, queueErr = e.MessageQueue.AcquireWithWait(c, target.Record.ID, baseRPM, parsed.Stream, streamState, e.MessageQueueWait, log)
			if queueErr != nil {
				log.Warn("gateway.umq_acquire_failed", zap.Error(queueErr))
			}
		case policy.MessageQueueThrottle:
			if queueErr := e.MessageQueue.ThrottleWithPing(c, target.Record.ID, baseRPM, parsed.Stream, streamState, e.MessageQueueWait, log); queueErr != nil {
				log.Warn("gateway.umq_throttle_failed", zap.Error(queueErr))
			}
		}
	}
	release = scheduler.WrapRelease(ctx, scheduler.ReleaseOnCancel, release)
	parsed.OnUpstreamAccepted = release
	cleanup := func() {
		if release != nil {
			release()
		}
		parsed.OnUpstreamAccepted = nil
	}
	if e.Anthropic != nil {
		if err = parsed.ReplaceBody(e.Anthropic.ApplyBedrockCCCompat(c, parsed.Body.Bytes(), parsed.Model, target, parsed.GroupID)); err != nil {
			cleanup()
			return nil, nil, false, err
		}
	}
	c.Set("parsed_request", parsed)
	return parsed, cleanup, false, nil
}
