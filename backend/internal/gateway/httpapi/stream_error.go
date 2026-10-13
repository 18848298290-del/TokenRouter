package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// After Responses streaming begins, clients recognize failure through a
// terminal response.failed object. That object carries an ID, creation
// timestamp and empty output array so clients can complete their response state
// even when generation never produced output.

// ResponsesFailedError is the error object in an OpenAI Responses failure.
type ResponsesFailedError struct {
	Code    string `json:"code"`    // client machine-readable error code
	Message string `json:"message"` // client-safe failure description text
}

// ResponsesFailedBody is the response object carried by a failure event.
// The writer supplies an empty Output slice and CreatedAt for clients that require these fields.
type ResponsesFailedBody struct {
	// synthetic response correlation identifier
	ID        string `json:"id"`
	Object    string `json:"object"`     // response object type label
	CreatedAt int64  `json:"created_at"` // Unix seconds
	// optional trimmed request model
	Model  string `json:"model,omitempty"`
	Status string `json:"status"` // failed response status label
	// Output is an empty array in WriteResponsesFailedSSE, allowing clients to
	// read the ordinary response output shape even when generation failed before
	// content.
	Output []any                `json:"output"`
	Error  ResponsesFailedError `json:"error"` // terminal client error details
}

// ResponsesFailedEvent is the top-level JSON object on an SSE data line.
// It omits sequence_number because this writer does not know the last emitted sequence.
type ResponsesFailedEvent struct {
	Type     string              `json:"type"`     // terminal failure event type
	Response ResponsesFailedBody `json:"response"` // failed response metadata object
}

// WriteResponsesFailedSSE handles a terminal response.failed event for an
// already started stream. Callers provide a Gin writer and end the request
// after true, including marshal or write failure attached to Gin. False means
// Flush is unavailable, so the caller may choose another protocol error path;
// committed HTTP status remains fixed.
func WriteResponsesFailedSSE(c *gin.Context, errType, code, message, requestID, model string) bool {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return false
	}

	payload, err := json.Marshal(ResponsesFailedEvent{
		Type: "response.failed",
		Response: ResponsesFailedBody{
			ID:        SynthesizeResponseID(requestID),
			Object:    "response",
			CreatedAt: time.Now().Unix(),
			Model:     strings.TrimSpace(model),
			Status:    "failed",
			Output:    []any{},
			Error: ResponsesFailedError{
				Code:    MapResponsesErrorCode(errType, code),
				Message: message,
			},
		},
	})
	// Attach serialization failure to the request and return handled, preventing
	// callers from appending a second incompatible envelope to the started stream.
	if err != nil {
		_ = c.Error(err)
		return true
	}

	// Attach delivery failure and finish this error path; the committed stream
	// cannot recover by switching to an ordinary JSON status response.
	if _, err := fmt.Fprintf(c.Writer, "event: response.failed\ndata: %s\n\n", payload); err != nil {
		_ = c.Error(err)
		return true
	}
	flusher.Flush()
	return true
}

// InboundIsResponses recognizes Responses roots and subpaths for terminal error
// formatting. It prefers the matched Gin path, then falls back to URL.Path when
// unmatched; nil contexts and empty paths do not identify a Responses entry.
func InboundIsResponses(c *gin.Context) bool {
	if c == nil {
		return false
	}
	p := strings.TrimRight(c.FullPath(), "/")
	// Unmatched test contexts and early error paths lack a Gin route pattern; use
	// their URL path to select the same terminal Responses envelope.
	if p == "" && c.Request != nil && c.Request.URL != nil {
		p = strings.TrimRight(c.Request.URL.Path, "/")
	}
	if p == "" {
		return false
	}
	return strings.HasSuffix(p, "/responses") || strings.Contains(p, "/responses/")
}

// MapResponsesErrorCode uses the first supplied code when that value is
// nonempty. Later variadic entries are ignored; otherwise known error types are
// mapped, unknown types pass through, and server or empty types use
// server_error.
func MapResponsesErrorCode(errType string, code ...string) string {
	if len(code) > 0 && code[0] != "" {
		return code[0]
	}
	switch errType {
	case "rate_limit_error":
		return "rate_limit_exceeded"
	case "invalid_request_error":
		return "invalid_request"
	case "permission_error":
		return "permission_denied"
	case "authentication_error":
		return "authentication_failed"
	case "upstream_error":
		return "upstream_error"
	case "server_error", "api_error", "":
		return "server_error"
	default:
		return errType
	}
}

// SynthesizeResponseID prefixes a trimmed telemetry request ID after removing
// hyphens. Blank IDs use a newly generated UUID with its hyphens removed,
// allowing a failure event to carry an ID even without request correlation
// metadata.
func SynthesizeResponseID(requestID string) string {
	if requestID = strings.TrimSpace(requestID); requestID != "" {
		return "resp_" + strings.ReplaceAll(requestID, "-", "")
	}
	// Generate a response identifier when correlation is absent, retaining the
	// resp_ shape expected by clients reading terminal Responses events.
	return "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}
