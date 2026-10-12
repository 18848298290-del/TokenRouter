package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

const InvalidStreamFieldTypeMessage = "invalid stream field type"

// ParsedRequest 是读取 HTTP 正文并校验字段后得到的只读请求数据。
type ParsedRequest struct {
	Body      []byte
	Model     string
	Stream    bool
	StartedAt time.Time
}

// HTTPFailure 描述返回客户端的协议错误。
type HTTPFailure struct {
	Status        int
	Type, Message string
	RetryAfter    int
}

// QoderChatHandler 的装配回调加载认证和路由上下文，gateway 执行请求循环。
type QoderChatHandler struct {
	RequestLifetime

	Executor interface {
		Execute(context.Context, gateway.Request, upstream.OutputSink) (gateway.ExecutionResult, error)
	}
	PrepareRequest func(*gin.Context, ParsedRequest) (gateway.Request, error)
	Observer       func(*gin.Context, gateway.Request) gateway.ExecutionObserver
	Preflight      func(*gin.Context) error
	UseCase        *gateway.QoderUseCase
	Prepare        func(*gin.Context, ParsedRequest) (gateway.Request, gateway.RequestPorts, error)
	Failure        func(*gin.Context, error) *HTTPFailure
}

// observedExecutionOutput 同步报告 HTTP 状态。
type observedExecutionOutput struct {
	ResponseSink
	gateway.ExecutionObserver
}

func (e *HTTPFailure) Error() string { return e.Message }

// ChatCompletions 读取 Chat 请求，写出协议错误或 SSE 响应。
func (h *QoderChatHandler) ChatCompletions(c *gin.Context) {
	done, accepted := h.BeginRequest(c, "openai")
	if !accepted {
		return
	}
	defer done()

	start := time.Now()
	if h.Preflight != nil {
		if err := h.Preflight(c); err != nil {
			h.fail(c, err, false)
			return
		}
	}
	body, err := httpx.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			h.writeError(c, &HTTPFailure{Status: 413, Type: "invalid_request_error", Message: BodyTooLargeMessage(maxErr.Limit)}, false)
			return
		}
		h.writeError(c, &HTTPFailure{Status: 400, Type: "invalid_request_error", Message: "Failed to read request body"}, false)
		return
	}
	if len(body) == 0 {
		h.writeError(c, &HTTPFailure{Status: 400, Type: "invalid_request_error", Message: "Request body is empty"}, false)
		return
	}
	if !gjson.ValidBytes(body) {
		h.writeError(c, &HTTPFailure{Status: 400, Type: "invalid_request_error", Message: "Failed to parse request body"}, false)
		return
	}
	model := gjson.GetBytes(body, "model")
	if !model.Exists() || model.Type != gjson.String || strings.TrimSpace(model.String()) == "" {
		h.writeError(c, &HTTPFailure{Status: 400, Type: "invalid_request_error", Message: "model is required"}, false)
		return
	}
	stream, valid := ParseOpenAICompatibleStream(body)
	if !valid {
		h.writeError(c, &HTTPFailure{Status: 400, Type: "invalid_request_error", Message: InvalidStreamFieldTypeMessage}, false)
		return
	}

	if h.Executor != nil {
		request, err := h.PrepareRequest(c, ParsedRequest{Body: body, Model: strings.TrimSpace(model.String()), Stream: stream, StartedAt: start})
		if err != nil {
			h.fail(c, err, stream)
			return
		}
		var output upstream.OutputSink = ResponseSink{Writer: c.Writer}
		if h.Observer != nil {
			output = observedExecutionOutput{ResponseSink: ResponseSink{Writer: c.Writer}, ExecutionObserver: h.Observer(c, request)}
		}
		_, err = h.Executor.Execute(c.Request.Context(), request, output)
		if err != nil {
			h.fail(c, err, stream)
		}
		return
	}
	request, ports, err := h.Prepare(c, ParsedRequest{Body: body, Model: strings.TrimSpace(model.String()), Stream: stream, StartedAt: start})
	if err != nil {
		h.fail(c, err, stream)
		return
	}
	output := &gateway.OutputTracker{Sink: ResponseSink{Writer: c.Writer}}
	err = h.UseCase.Run(c.Request.Context(), request, ports, output)
	if err != nil {
		h.fail(c, err, stream)
	}
}

func (h *QoderChatHandler) fail(c *gin.Context, err error, stream bool) {
	if c.Request.Context().Err() != nil {
		return
	}
	failure := &HTTPFailure{Status: 502, Type: "upstream_error", Message: "Upstream request failed"}
	if h.Failure != nil {
		failure = h.Failure(c, err)
		if failure == nil {
			return
		}
	} else {
		if typed, ok := errors.AsType[*HTTPFailure](err); ok {
			failure = typed
		}
	}
	h.writeError(c, failure, stream)
}

func (h *QoderChatHandler) writeError(c *gin.Context, f *HTTPFailure, stream bool) {
	copy := *f
	f = &copy
	f.Status, f.Message = modelAttributeError(c, f.Status, f.Message)
	if f.RetryAfter > 0 {
		c.Header("Retry-After", strconv.Itoa(f.RetryAfter))
	}
	if stream && c.Writer.Written() {
		_, _ = c.Writer.WriteString(`data: {"error":{"type":` + strconv.Quote(f.Type) + `,"message":` + strconv.Quote(f.Message) + "}}\n\ndata: [DONE]\n\n")
		c.Writer.Flush()
		return
	}
	c.JSON(f.Status, gin.H{"error": gin.H{"type": f.Type, "message": f.Message}})
}

// ParseOpenAICompatibleStream 读取布尔字段，缺省返回 false，包含 null 在内的其他类型返回校验失败。
func ParseOpenAICompatibleStream(body []byte) (bool, bool) {
	v := gjson.GetBytes(body, "stream")
	if v.Exists() && v.Type != gjson.True && v.Type != gjson.False {
		return false, false
	}
	return v.Bool(), true
}

// BodyLimitLabel 和 BodyTooLargeMessage 生成正文大小限制的错误文本。
func BodyLimitLabel(limit int64) string {
	if limit >= 1024*1024 {
		return fmt.Sprintf("%dMB", limit/(1024*1024))
	}
	return fmt.Sprintf("%dB", limit)
}

func BodyTooLargeMessage(limit int64) string {
	return fmt.Sprintf("Request body too large, limit is %s", BodyLimitLabel(limit))
}
