package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// openai_error_output.go writes OpenAI protocol errors and records their
// intended status after stream framing begins.
// A Compact heartbeat can commit HTTP 200 before an HTTP error is ready. Error
// output first stops that writer and then emits a failed Responses terminal
// when the inbound route is Responses, because its client consumes protocol
// terminal events. Other started routes receive their generic SSE error
// framing.
//
// Stream observations also distinguish ordinary error reporting from failures
// counted toward SLA, even though the HTTP status has already been sent. The
// callbacks carry that intended status independently of the writer's committed
// status. Anthropic compatibility output uses its own error event and leaves
// observation to its caller, while an unstarted response can still receive
// JSON with the requested error status.

// OpenAIErrorOutput binds synchronous protocol error writes to request
// observation callbacks. DefaultOpenAIErrorOutput supplies the callbacks used
// by HTTP, WebSocket and media execution adapters.
type OpenAIErrorOutput struct {
	// stopCompact prevents later Compact beats and waits for an active write.
	// Its result includes ordinary reader heartbeats and failed controller writes.
	stopCompact func(*gin.Context) bool
	// markStream and markFailure receive the intended error status after streaming
	// begins. WriteStreamingErrorWithCode selects them with countTowardsSLA,
	// keeping observation independent of the HTTP status already committed.
	// ordinary stream error observation
	markStream func(*gin.Context, string, string, int)
	// SLA stream failure observation
	markFailure func(*gin.Context, string, string, string, int)
	// metadata supplies request ID and model when a stopped Compact heartbeat
	// needs a Responses terminal. Configure it before invoking WriteError.
	metadata func(*gin.Context) (string, string)
}

// WriteAnthropicStreamingError writes Anthropic JSON before stream start or an
// error event afterward. The model-attribute helper can replace status and
// message; started output requires a flusher, ignores write errors and leaves
// terminal observation to the caller.
func (h OpenAIErrorOutput) WriteAnthropicStreamingError(c *gin.Context, status int, kind, message string, started bool) {
	status, message = modelAttributeError(c, status, message)
	if !started {
		WriteAnthropicError(c, status, kind, "", message)
		return
	}
	if flusher, ok := c.Writer.(http.Flusher); ok {
		// This compatibility event contains fixed string fields. Its caller owns
		// observation and termination even if the subsequent write fails after stream start.
		payload, _ := json.Marshal(gin.H{"type": "error", "error": gin.H{"type": kind, "message": message}})
		// This compatibility writer ignores write errors and leaves observation and terminal state to its caller.
		_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", payload)
		flusher.Flush()
	}
}

// WriteStreamingErrorWithCode writes a client error in the format permitted by
// current output. Callers configure the callbacks and provide a live writer.
// It stops Compact heartbeats before taking over output, and heartbeat
// commitment selects an in-stream error even when streamStarted was false.
//
// Started output records either an ordinary stream error or an SLA failure
// according to countTowardsSLA. Responses routes first attempt
// response.failed, and other routes or an unsuccessful Responses write use
// generic SSE error framing when a flusher is available. Unstarted output uses
// JSON; a nonempty code is included directly in that envelope.
func (h OpenAIErrorOutput) WriteStreamingErrorWithCode(
	c *gin.Context,
	status int,
	errType string,
	code string,
	message string,
	streamStarted bool,
	countTowardsSLA bool,
) {
	status, message = modelAttributeError(c, status, message)
	// Stop waits for an active heartbeat write and prevents later beats, so its
	// bytes cannot interleave with this terminal error when output takes over.
	if h.stopCompact(c) {
		streamStarted = true
	}
	if streamStarted {
		if countTowardsSLA {
			h.markFailure(c, errType, code, message, status)
		} else {
			h.markStream(c, errType, message, status)
		}
		// Responses clients consume terminal lifecycle events. Emit response.failed
		// first so the already-started response carries a recognizable failure terminal.
		if InboundIsResponses(c) {
			if WriteResponsesFailedSSE(c, errType, code, message, ErrorRequestID(c), ErrorRequestModel(c)) {
				return
			}
		}
		// Use generic SSE error framing for other routes or when the Responses
		// terminal could not be written, leaving the committed HTTP status in place.
		flusher, ok := c.Writer.(http.Flusher)
		if ok {
			errorObject := gin.H{"type": errType, "message": message}
			if code != "" {
				errorObject["code"] = code
			}
			payload, err := json.Marshal(gin.H{"error": errorObject})
			// Retain a valid generic error event if envelope serialization fails, so
			// the existing stream can still receive a failure payload through this writer.
			if err != nil {
				payload = []byte(`{"error":{"type":"upstream_error","message":"Upstream request failed"}}`)
			}
			errorEvent := "event: error\ndata: " + string(payload) + "\n\n"
			if _, err := fmt.Fprint(c.Writer, errorEvent); err != nil {
				_ = c.Error(err)
			}
			flusher.Flush()
		}
		return
	}

	if code == "" {
		h.WriteError(c, status, errType, message)
		return
	}
	c.JSON(status, gin.H{"error": gin.H{
		"type": errType, "code": code, "message": message,
	}})
}

// DefaultOpenAIErrorOutput binds heartbeat stopping, request metadata and Ops
// error observations.
func DefaultOpenAIErrorOutput() OpenAIErrorOutput {
	return OpenAIErrorOutput{stopCompact: StopOpenAICompactSSEKeepaliveCommitted, markStream: MarkOpsStreamError, markFailure: MarkOpsStreamFailure, metadata: func(c *gin.Context) (string, string) { return ErrorRequestID(c), ErrorRequestModel(c) }}
}

// WriteError applies any model-attribute presentation override and writes the
// OpenAI error through the configured heartbeat-aware helper. Callers supply a
// live context and configured stop, observation and metadata callbacks for
// Compact terminal output.
func (h OpenAIErrorOutput) WriteError(c *gin.Context, status int, kind, message string) {
	status, message = modelAttributeError(c, status, message)
	writeOpenAIRequestError(c, status, kind, message, h.stopCompact, h.markStream, h.metadata)
}

// StreamError writes a streaming-aware error without a separate classification
// code. It uses ordinary stream observation rather than SLA failure counting;
// callers configure the output callbacks and provide the current started
// state.
func (h OpenAIErrorOutput) StreamError(c *gin.Context, status int, kind, message string, started bool) {
	h.WriteStreamingErrorWithCode(c, status, kind, "", message, started, false)
}

// EnsureFallback attempts an error response when no upstream failure object is
// available. Its result reports whether EnsureResponse invoked output;
// already-communicated content can suppress the fallback, and true does not
// confirm successful network delivery.
func (h OpenAIErrorOutput) EnsureFallback(c *gin.Context, started bool) bool {
	return h.EnsureResponse(c, started, nil)
}

// WriteFailoverExhausted presents the final classified failure through this
// output's stream-aware writer. The supplied rules and hooks govern final
// observation and presentation; callers provide the started state after the
// provider loop ends.
func (h OpenAIErrorOutput) WriteFailoverExhausted(c *gin.Context, failure *OpenAIFailoverError, started bool, rules ErrorRuleMatcher, hooks FailoverErrorHooks) {
	WriteOpenAIFailoverExhausted(c, failure, started, rules, hooks, h.StreamError)
}
