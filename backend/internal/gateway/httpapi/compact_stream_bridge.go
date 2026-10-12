package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/TokenFlux/TokenRouter/internal/gateway/compact"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// openAICompactClientStreamKey records client stream intent before a body-triggered compact request becomes unary.
// The bridge converts the upstream JSON to compaction output_item.done and response.completed events.
// Codex needs terminal events to avoid stream-closed retries (#3875).
const openAICompactClientStreamKey = "openai_compact_client_stream"

// CompactStreamErrorObserver records the type, message, and status of a client stream error.
type CompactStreamErrorObserver func(*gin.Context, string, string, int)

// compactFailedEvent includes created_at and an empty output array required by Responses clients.
type compactFailedEvent struct {
	Response compactFailedResponse `json:"response"`
	Type     string                `json:"type"`
}
type compactFailedResponse struct {
	CreatedAt int64              `json:"created_at"`
	Error     compactFailedError `json:"error"`
	ID        string             `json:"id"`
	Object    string             `json:"object"`
	Output    []json.RawMessage  `json:"output"`
	Status    string             `json:"status"`
}
type compactFailedError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// MarkOpenAICompactClientStream saves the client's streaming intent during compact promotion
// so the response path can synthesize SSE from the upstream JSON.
func MarkOpenAICompactClientStream(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(openAICompactClientStreamKey, true)
}

func OpenAICompactClientWantsStream(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, ok := c.Get(openAICompactClientStreamKey)
	if !ok {
		return false
	}
	wants, _ := value.(bool)
	return wants
}

// WriteOpenAICompactSSEBridge converts Compact JSON into Responses SSE for streaming clients.
// Before heartbeat commitment it handles marked requests with a 2xx JSON object; false leaves output to the caller.
// After a heartbeat commits HTTP 200 it takes over the response and reports failures through response.failed.
func WriteOpenAICompactSSEBridge(c *gin.Context, statusCode int, finalResponse []byte, observe CompactStreamErrorObserver) bool {
	if c == nil || !OpenAICompactClientWantsStream(c) {
		return false
	}
	// Stop and wait for heartbeat writes under the mutex before taking over the writer,
	// so comment lines cannot interleave with terminal events.
	committed := StopOpenAICompactSSEKeepaliveCommitted(c)
	if statusCode < 200 || statusCode >= 300 {
		if committed {
			WriteOpenAICompactSSEFailure(c, statusCode, finalResponse, observe)
			return true
		}
		return false
	}
	payload, ok := BuildOpenAICompactSSEPayload(finalResponse)
	if !ok {
		if committed {
			WriteOpenAICompactSSEFailure(c, http.StatusBadGateway, finalResponse, observe)
			return true
		}
		return false
	}
	if !committed {
		header := c.Writer.Header()
		header.Set("Content-Type", "text/event-stream")
		header.Set("Cache-Control", "no-cache")
		header.Set("Connection", "keep-alive")
		header.Set("X-Accel-Buffering", "no")
		c.Writer.WriteHeader(statusCode)
	}
	_, _ = c.Writer.Write(payload)
	c.Writer.Flush()
	return true
}

// WriteOpenAICompactSSEFailure extracts an upstream error message and writes response.failed.
// Use it after a heartbeat has committed HTTP 200, when the HTTP status can no longer
// represent the failure.
func WriteOpenAICompactSSEFailure(c *gin.Context, statusCode int, errorBody []byte, observe CompactStreamErrorObserver) {
	message := ""
	if len(errorBody) > 0 {
		message = logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(errorBody)))
	}
	if message == "" {
		message = "Upstream compact request failed with HTTP " + strconv.Itoa(statusCode)
	}
	WriteOpenAICompactSSEFailureMessage(c, statusCode, "upstream_error", message, observe)
}

// WriteOpenAICompactSSEFailureMessage writes a response.failed terminal event.
// Codex recognizes this as a terminal Responses event; a plain error event can lead to
// "stream closed before response.completed" retries.
// The observer records the failure even though the HTTP response status is 200.
func WriteOpenAICompactSSEFailureMessage(c *gin.Context, statusCode int, errType, message string, observe CompactStreamErrorObserver) {
	if c == nil {
		return
	}
	if observe != nil {
		observe(c, errType, message, statusCode)
	}
	responseID := newCompactResponseID()
	payload, err := json.Marshal(compactFailedEvent{
		Response: compactFailedResponse{
			CreatedAt: time.Now().Unix(),
			Error:     compactFailedError{Code: errType, Message: message},
			ID:        responseID,
			Object:    "response",
			Output:    []json.RawMessage{},
			Status:    "failed",
		},
		Type: "response.failed",
	})
	if err != nil {
		return
	}
	_, _ = c.Writer.Write([]byte("event: response.failed\ndata: "))
	_, _ = c.Writer.Write(payload)
	_, _ = c.Writer.Write([]byte("\n\n"))
	c.Writer.Flush()
}

func newCompactResponseID() string {
	return "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// BuildOpenAICompactSSEPayload supplies response ID generation to compact protocol conversion.
func BuildOpenAICompactSSEPayload(body []byte) ([]byte, bool) {
	return compact.StreamPayload(body, newCompactResponseID)
}
