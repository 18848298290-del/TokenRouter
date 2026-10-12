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

// Qoder Chat admission validates JSON model and stream fields before preparing
// a platform request. A missing stream field permits buffered output, while
// null or another field type is rejected. A configured Executor takes
// precedence over the older UseCase path; both receive the same validated body
// and request start time.
//
// Failure output is skipped after request cancellation or when a configured
// presenter returns nil. Otherwise presentation copies the chosen failure
// before applying request model-attribute overrides, preserving a reusable
// failure value. Already-written streaming output receives an escaped OpenAI
// error frame and DONE marker, while requests without that combination receive
// the ordinary JSON envelope and selected HTTP status.

// InvalidStreamFieldTypeMessage identifies rejected nonboolean stream fields
// consistently across native and compatible Qoder HTTP admission paths.
const InvalidStreamFieldTypeMessage = "invalid stream field type"

// ParsedRequest retains validated client input and arrival timing for Qoder
// preparation.
type ParsedRequest struct {
	// Body retains the validated request slice for synchronous preparation; this
	// representation is passed onward without a new body copy here.
	Body      []byte    // retained validated JSON body
	Model     string    // trimmed client requested model
	Stream    bool      // validated client stream intent
	StartedAt time.Time // admission start time instant
}

// HTTPFailure carries client-facing status, error details and optional retry
// guidance.
type HTTPFailure struct {
	Status        int    // client HTTP error status
	Type, Message string // Type: client error category; Message: client error message
	// RetryAfter adds decimal seconds to the response header only when positive;
	// zero or negative values leave that header unset here.
	RetryAfter int // retry delay in seconds
}

// QoderChatHandler binds Chat admission to preferred execution or legacy
// UseCase services.
type QoderChatHandler struct {
	RequestLifetime // shared application request barrier

	// Executor, PrepareRequest and Observer form the preferred path whenever
	// Executor exists. Prepare and UseCase supply the legacy path otherwise;
	// each chosen path requires its request builder and execution service.
	Executor interface {
		// Execute forwards a prepared request to the supplied output and returns its
		// observed result and error. The HTTP handler supplies request context and
		// owns presenting any returned failure that execution has not suppressed.
		Execute(context.Context, gateway.Request, upstream.OutputSink) (gateway.ExecutionResult, error)
	} // preferred request execution service
	// preferred execution request builder
	PrepareRequest func(*gin.Context, ParsedRequest) (gateway.Request, error)
	// Observer is optional on the preferred Executor path; when configured, its
	// request-specific observation joins the active response sink before
	// execution.
	// optional execution observation builder
	Observer func(*gin.Context, gateway.Request) gateway.ExecutionObserver
	// Preflight is optional and runs before body reading; any returned error ends
	// admission through the handler’s normal failure presentation.
	// optional before-body admission check
	Preflight func(*gin.Context) error
	// legacy request execution service
	UseCase *gateway.QoderUseCase
	// legacy request and ports builder
	Prepare func(*gin.Context, ParsedRequest) (gateway.Request, gateway.RequestPorts, error)
	// Failure can return nil to suppress output. Without it, fail preserves a
	// typed HTTPFailure when present and otherwise uses a generic upstream error.
	Failure func(*gin.Context, error) *HTTPFailure
}

// observedExecutionOutput combines HTTP writes with request-specific execution
// observation.
type observedExecutionOutput struct {
	ResponseSink              // current HTTP response writer
	gateway.ExecutionObserver // request-local execution observation callbacks
}

// Error returns the HTTPFailure message for ordinary error reporting.
func (e *HTTPFailure) Error() string { return e.Message }

// ChatCompletions admits a Qoder Chat request under the request barrier and
// optional preflight check. It requires usable authentication/routing
// preparation callbacks for the chosen execution path and validates nonblank
// string model plus boolean stream intent before preparation.
//
// A configured Executor runs first and can attach request-specific
// observation; otherwise Prepare and UseCase run the legacy loop. Both own
// forwarding output while this handler presents returned errors, unless
// cancellation or the configured failure callback suppresses that response.
// Request-barrier cleanup runs when handling returns.
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

// fail presents a returned execution error unless the request context has
// ended. A configured Failure callback can suppress output with nil; otherwise
// a typed HTTPFailure is retained and an unrecognized error receives generic
// 502 details.
func (h *QoderChatHandler) fail(c *gin.Context, err error, stream bool) {
	// An ended request context suppresses this outer error write even for a
	// classified failure, retaining any output execution already left on the writer.
	if c.Request.Context().Err() != nil {
		return
	}
	failure := &HTTPFailure{Status: 502, Type: "upstream_error", Message: "Upstream request failed"}
	if h.Failure != nil {
		failure = h.Failure(c, err)
		// A nil presenter result explicitly declines another handler response; honor
		// that decision rather than replacing it with the generic upstream fallback.
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

// writeError presents one supplied failure after copying its value and
// applying request model-attribute overrides. Callers provide a nonnil failure
// and usable context; positive RetryAfter adds decimal seconds to the response
// header.
//
// When client streaming is requested and the writer has already written
// headers, the existing status cannot be replaced. The method quotes category
// and message into an OpenAI SSE error followed by DONE and flushes the
// writer; other requests receive the ordinary JSON error with the selected
// status. The supplied failure remains unchanged for other consumers.
func (h *QoderChatHandler) writeError(c *gin.Context, f *HTTPFailure, stream bool) {
	// Model-attribute state changes only this response; copy the failure first
	// so a presenter-owned value remains reusable without request-specific status mutation.
	copy := *f
	f = &copy
	f.Status, f.Message = modelAttributeError(c, f.Status, f.Message)
	if f.RetryAfter > 0 {
		c.Header("Retry-After", strconv.Itoa(f.RetryAfter))
	}
	// Already-written streaming headers keep their existing status; quote dynamic
	// fields into terminal frames and discard writer errors at this presentation boundary.
	if stream && c.Writer.Written() {
		_, _ = c.Writer.WriteString(`data: {"error":{"type":` + strconv.Quote(f.Type) + `,"message":` + strconv.Quote(f.Message) + "}}\n\ndata: [DONE]\n\n")
		c.Writer.Flush()
		return
	}
	c.JSON(f.Status, gin.H{"error": gin.H{"type": f.Type, "message": f.Message}})
}

// ParseOpenAICompatibleStream returns client stream intent and whether its
// JSON field has a supported type. A missing field means false with
// valid=true; explicit null, strings and numbers return false with
// valid=false.
func ParseOpenAICompatibleStream(body []byte) (bool, bool) {
	v := gjson.GetBytes(body, "stream")
	if v.Exists() && v.Type != gjson.True && v.Type != gjson.False {
		return false, false
	}
	return v.Bool(), true
}

// BodyLimitLabel formats a byte limit for client error messages. Values of at
// least one binary megabyte use truncated integer MiB magnitude with the
// existing MB suffix; smaller values retain their byte magnitude.
func BodyLimitLabel(limit int64) string {
	// Truncate binary megabyte magnitude while retaining the existing MB suffix.
	if limit >= 1024*1024 {
		return fmt.Sprintf("%dMB", limit/(1024*1024))
	}
	return fmt.Sprintf("%dB", limit)
}

// BodyTooLargeMessage returns the shared request-size error using
// BodyLimitLabel formatting.
func BodyTooLargeMessage(limit int64) string {
	return fmt.Sprintf("Request body too large, limit is %s", BodyLimitLabel(limit))
}
