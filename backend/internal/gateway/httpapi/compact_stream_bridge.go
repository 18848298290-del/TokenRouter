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

// compact_stream_bridge.go writes legacy Compact JSON results as Responses SSE
// output when the client requested streaming.
// Body-triggered legacy compaction can request a stream even though its upstream
// Compact exchange returns JSON. The saved client marker lets output convert
// that JSON into item events followed by a Responses terminal event. The protocol
// converter owns response ID repair, event contents, and usage shape checks.
//
// Heartbeats may already have committed HTTP 200 before the JSON result arrives.
// Output stops the heartbeat controller before taking over the writer, then
// emits response.failed for errors in that committed stream. Before commitment,
// unsupported payloads or unsuccessful status remain for the caller's ordinary
// HTTP error handling. Stream observations retain the intended failure status
// even when the committed client status is successful.

// openAICompactClientStreamKey records client stream intent before promotion
// removes stream from unary input, allowing the output adapter to select Responses SSE.
const openAICompactClientStreamKey = "openai_compact_client_stream"

// CompactStreamErrorObserver records the intended type, message, and status of a client stream error.
type CompactStreamErrorObserver func(*gin.Context, string, string, int)

// compactFailedEvent wraps the failed response in its named Responses stream event.
type compactFailedEvent struct {
	Response compactFailedResponse `json:"response"` // terminal response object
	Type     string                `json:"type"`     // response.failed event type
}

// compactFailedResponse is the Responses terminal object emitted after Compact stream failure.
type compactFailedResponse struct {
	CreatedAt int64              `json:"created_at"` // Unix seconds
	Error     compactFailedError `json:"error"`      // terminal error details
	ID        string             `json:"id"`         // generated response identifier
	Object    string             `json:"object"`     // literal response kind
	Output    []json.RawMessage  `json:"output"`     // empty output array
	Status    string             `json:"status"`     // literal failed status
}

// compactFailedError contains the code and message for the terminal Responses failure.
type compactFailedError struct {
	Code    string `json:"code"`    // protocol error classification
	Message string `json:"message"` // client-visible failure explanation
}

// MarkOpenAICompactClientStream saves the client's streaming intent during compact promotion
// so the response path can synthesize SSE from the upstream JSON.
// A nil context is ignored. Normalization calls this before removing the
// outbound stream field so later output can still recognize the client's request framing.
func MarkOpenAICompactClientStream(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(openAICompactClientStreamKey, true)
}

// OpenAICompactClientWantsStream reports whether promotion saved a true client-stream marker.
// Nil contexts, absent keys, and nonboolean values return false. The output
// adapter uses this marker after the upstream request has become unary JSON.
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
// Callers provide an available writer. Before heartbeat commitment it handles
// marked requests with a 2xx JSON object; false leaves output to the caller.
// After heartbeat framing starts, it takes over output and reports failures
// through response.failed.
// A true result means this adapter claimed output, including attempts whose
// writer errors are discarded. The caller must not write another response on
// that result and still owns the upstream response's resource cleanup.
//
// Heartbeat stopping finishes before payload selection. An unsuccessful status
// or unusable JSON leaves output unclaimed before commitment, but becomes a
// terminal stream failure afterward because the existing status cannot represent
// that error. Successful conversion writes ordered item and completed events
// through the same writer, with headers selected only when heartbeat framing
// has not already selected them.
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
	// Output is already claimed after framing selection. Flush the constructed
	// terminal events and return ownership even if the client writer reported a failure.
	_ = NewResponseSink(c).Emit(upstream.OutputEvent{Data: payload})
	c.Writer.Flush()
	return true
}

// WriteOpenAICompactSSEFailure extracts an upstream error message and writes response.failed.
// Use it after a heartbeat has committed HTTP 200, when the HTTP status can no longer
// represent the failure.
// Upstream messages are trimmed and query-sanitized; an empty extracted message
// uses a status-specific default. The observer receives the intended failure
// status before the terminal event is written.
func WriteOpenAICompactSSEFailure(c *gin.Context, statusCode int, errorBody []byte, observe CompactStreamErrorObserver) {
	message := ""
	if len(errorBody) > 0 {
		message = logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(errorBody)))
	}
	if message == "" {
		// Empty upstream error bodies still need a usable terminal explanation.
		// Include their intended status so committed-stream clients receive a meaningful failure message.
		message = "Upstream compact request failed with HTTP " + strconv.Itoa(statusCode)
	}
	WriteOpenAICompactSSEFailureMessage(c, statusCode, "upstream_error", message, observe)
}

// WriteOpenAICompactSSEFailureMessage writes a response.failed terminal event.
// The observer receives the intended failure independently of the status already
// selected by the caller. The generated object includes timestamp, response ID,
// error details, and an empty output array alongside its failed status.
// A nil context is ignored, and the observer is optional. Callers provide
// the safe message and an available writer after deciding stream framing.
// Encoding failure returns after observation; downstream write errors are ignored.
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
		// Observation has already captured the intended failure. If framing cannot
		// be encoded, leave that diagnostic available and avoid writing an incomplete terminal JSON object.
		return
	}
	// The selected stream cannot be replaced with another HTTP response. Attempt
	// the terminal frame and flush even when a disconnected writer reports an error.
	_, _ = c.Writer.Write([]byte("event: response.failed\ndata: "))
	_, _ = c.Writer.Write(payload)
	_, _ = c.Writer.Write([]byte("\n\n"))
	c.Writer.Flush()
}

// newCompactResponseID returns a Responses identifier using a hyphen-free random UUID.
func newCompactResponseID() string {
	return "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// BuildOpenAICompactSSEPayload supplies response ID generation to compact protocol conversion.
// Invalid or non-object input returns false. The delegated converter emits
// output-item events and a response.completed event containing the captured
// response, repairing unusable IDs and removing usage that fails the required
// unsigned-integer token shape. The object's status remains as supplied; callers
// decide whether the upstream result qualifies for this completion framing.
func BuildOpenAICompactSSEPayload(body []byte) ([]byte, bool) {
	return compact.StreamPayload(body, newCompactResponseID)
}
