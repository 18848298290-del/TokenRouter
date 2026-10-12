package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// OpenAIErrorOutput writes protocol errors with synchronous observation and heartbeat callbacks.
type OpenAIErrorOutput struct {
	stopCompact func(*gin.Context) bool
	markStream  func(*gin.Context, string, string, int)
	markFailure func(*gin.Context, string, string, string, int)
	metadata    func(*gin.Context) (string, string)
}

func (h OpenAIErrorOutput) WriteAnthropicStreamingError(c *gin.Context, status int, kind, message string, started bool) {
	status, message = modelAttributeError(c, status, message)
	if !started {
		WriteAnthropicError(c, status, kind, "", message)
		return
	}
	if flusher, ok := c.Writer.(http.Flusher); ok {
		payload, _ := json.Marshal(gin.H{"type": "error", "error": gin.H{"type": kind, "message": message}})
		// This compatibility writer ignores write errors and leaves observation and terminal state to its caller.
		_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", payload)
		flusher.Flush()
	}
}

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
	// A Compact heartbeat may already have committed HTTP 200. Stop it and wait for its writer
	// before taking over the ResponseWriter and emitting an in-stream error.
	if h.stopCompact(c) {
		streamStarted = true
	}
	if streamStarted {
		if countTowardsSLA {
			h.markFailure(c, errType, code, message, status)
		} else {
			h.markStream(c, errType, message, status)
		}
		// Codex CLI expects a response.completed, response.failed, response.incomplete, or response.cancelled
		// terminal event.
		// A generic event:error can leave it reporting "stream closed before response.completed".
		if InboundIsResponses(c) {
			if WriteResponsesFailedSSE(c, errType, code, message, ErrorRequestID(c), ErrorRequestModel(c)) {
				return
			}
		}
		// Other streaming routes receive their protocol error event.
		flusher, ok := c.Writer.(http.Flusher)
		if ok {
			errorObject := gin.H{"type": errType, "message": message}
			if code != "" {
				errorObject["code"] = code
			}
			payload, err := json.Marshal(gin.H{"error": errorObject})
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

// DefaultOpenAIErrorOutput provides error writing and observation for HTTP, WS, and media adapters.
func DefaultOpenAIErrorOutput() OpenAIErrorOutput {
	return OpenAIErrorOutput{stopCompact: StopOpenAICompactSSEKeepaliveCommitted, markStream: MarkOpsStreamError, markFailure: MarkOpsStreamFailure, metadata: func(c *gin.Context) (string, string) { return ErrorRequestID(c), ErrorRequestModel(c) }}
}

func (h OpenAIErrorOutput) WriteError(c *gin.Context, status int, kind, message string) {
	status, message = modelAttributeError(c, status, message)
	writeOpenAIRequestError(c, status, kind, message, h.stopCompact, h.markStream, h.metadata)
}

func (h OpenAIErrorOutput) StreamError(c *gin.Context, status int, kind, message string, started bool) {
	h.WriteStreamingErrorWithCode(c, status, kind, "", message, started, false)
}

// EnsureFallback supplies an error response when no upstream error object is available.
func (h OpenAIErrorOutput) EnsureFallback(c *gin.Context, started bool) bool {
	return h.EnsureResponse(c, started, nil)
}

func (h OpenAIErrorOutput) WriteFailoverExhausted(c *gin.Context, failure *OpenAIFailoverError, started bool, rules ErrorRuleMatcher, hooks FailoverErrorHooks) {
	WriteOpenAIFailoverExhausted(c, failure, started, rules, hooks, h.StreamError)
}
