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

// ResponsesFailedError is the error object in an OpenAI Responses failure.
type ResponsesFailedError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ResponsesFailedBody is the response object carried by a failure event.
// The writer supplies an empty Output slice and CreatedAt for clients that require these fields.
type ResponsesFailedBody struct {
	ID        string               `json:"id"`
	Object    string               `json:"object"`
	CreatedAt int64                `json:"created_at"`
	Model     string               `json:"model,omitempty"`
	Status    string               `json:"status"`
	Output    []any                `json:"output"`
	Error     ResponsesFailedError `json:"error"`
}

// ResponsesFailedEvent is the top-level JSON object on an SSE data line.
// It omits sequence_number because this writer does not know the last emitted sequence.
type ResponsesFailedEvent struct {
	Type     string              `json:"type"`
	Response ResponsesFailedBody `json:"response"`
}

// WriteResponsesFailedSSE writes a terminal response.failed event for a started stream.
// Once headers or a keepalive have been flushed, errors must be sent within SSE.
// Responses clients need a terminal response event to recognize stream completion.
//
// It returns true when the event was handled, including marshal or write failures,
// which are attached to Gin. Callers should end the request after a true result.
// It returns false when the writer cannot flush. A committed HTTP status cannot
// be changed by falling back to a JSON error.
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
	if err != nil {
		_ = c.Error(err)
		return true
	}

	if _, err := fmt.Fprintf(c.Writer, "event: response.failed\ndata: %s\n\n", payload); err != nil {
		_ = c.Error(err)
		return true
	}
	flusher.Flush()
	return true
}

// InboundIsResponses recognizes Responses roots and subpaths, including compact.
// It checks the matched Gin path, falling back to the URL path when no route is available.
// Wildcard route patterns contain /responses/ and use the same failure event format.
func InboundIsResponses(c *gin.Context) bool {
	if c == nil {
		return false
	}
	p := strings.TrimRight(c.FullPath(), "/")
	if p == "" && c.Request != nil && c.Request.URL != nil {
		p = strings.TrimRight(c.Request.URL.Path, "/")
	}
	if p == "" {
		return false
	}
	return strings.HasSuffix(p, "/responses") || strings.Contains(p, "/responses/")
}

// MapResponsesErrorCode uses an explicit code when supplied, otherwise maps known error types.
// Unknown error types pass through unchanged.
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

// SynthesizeResponseID derives an ID from the request ID, or a UUID when it is empty.
func SynthesizeResponseID(requestID string) string {
	if requestID = strings.TrimSpace(requestID); requestID != "" {
		return "resp_" + strings.ReplaceAll(requestID, "-", "")
	}
	return "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}
