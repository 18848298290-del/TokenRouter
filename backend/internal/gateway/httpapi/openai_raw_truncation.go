package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// A raw Chat stream ending before a terminal chunk becomes a retry signal only
// while the execution reader still permits another attempt. Ops retains the
// upstream request identifier and transport cause, while the failover body
// uses the same stable read-error classification as failures after output
// begins.

// openAIRawStreamTruncatedUpstreamMessage identifies missing terminal Chat chunks in Ops and
// supplies the common prefix used when a transport cause is appended.
const openAIRawStreamTruncatedUpstreamMessage = "Upstream Chat Completions stream ended before any terminal chunk"

// NewOpenAIRawTruncationFailure records truncation and returns a 502 failover
// error with its read classification. Callers verify that retry remains
// permitted before invoking it; a trimmed request identifier is copied into
// new headers, and this constructor does not grant recovery after published
// business output.
func NewOpenAIRawTruncationFailure(
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	upstreamRequestID string,
	cause error,
) *forwardcore.UpstreamFailoverError {
	RecordOpenAIRawTruncation(c, provider, upstreamRequestID, cause, "failover")

	headers := http.Header{}
	if id := strings.TrimSpace(upstreamRequestID); id != "" {
		headers.Set("x-request-id", id)
	}
	return &forwardcore.UpstreamFailoverError{
		StatusCode:      http.StatusBadGateway,
		ResponseBody:    openAIRawStreamTruncatedErrorBody(cause),
		ResponseHeaders: headers,
	}
}

// RecordOpenAIRawTruncation records the truncated Chat stream and optional
// transport cause in request Ops state. Nil contexts are ignored; a missing
// provider uses OpenAI with zero provider identity, while kind distinguishes
// failover from final HTTP-error recording.
func RecordOpenAIRawTruncation(
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	upstreamRequestID string,
	cause error,
	kind string,
) {
	if c == nil {
		return
	}
	message := openAIRawStreamTruncatedMessage(cause)
	platform := capability.PlatformOpenAI
	providerID := int64(0)
	providerName := ""
	if provider != nil {
		platform = provider.Record.Platform
		providerID = provider.Record.ID
		providerName = provider.Record.Name
	}
	SetOpsUpstreamError(c, http.StatusBadGateway, message, "")
	AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
		Platform:           platform,
		ProviderID:         providerID,
		ProviderName:       providerName,
		UpstreamStatusCode: http.StatusBadGateway,
		UpstreamRequestID:  strings.TrimSpace(upstreamRequestID),
		Kind:               kind,
		Message:            message,
	})
}

// openAIRawStreamTruncatedMessage appends a transport cause to the common
// truncation description. Nil causes and errors matching
// ErrOpenAIUpstreamStreamTruncated use the common message, because that
// sentinel already describes the missing terminal chunk.
func openAIRawStreamTruncatedMessage(cause error) string {
	if cause == nil || errors.Is(cause, openai.ErrOpenAIUpstreamStreamTruncated) {
		return openAIRawStreamTruncatedUpstreamMessage
	}
	return openAIRawStreamTruncatedUpstreamMessage + ": " + cause.Error()
}

// openAIRawStreamTruncatedErrorBody builds a JSON failover envelope from the
// stable stream-read classification. It returns a fixed truncation envelope if
// serialization fails, allowing the outer failure writer to receive usable
// JSON in that path.
func openAIRawStreamTruncatedErrorBody(cause error) []byte {
	code, message := openai.ClassifyUpstreamStreamReadError(cause)
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    "upstream_error",
			"code":    code,
			"message": message,
		},
	})
	// If serialization fails, retain a valid classified truncation response for
	// the outer failure writer even though the computed envelope could not be emitted.
	if err != nil {
		return []byte(`{"error":{"type":"upstream_error","code":"` + openai.OpenAIUpstreamStreamTruncatedCode +
			`","message":"Upstream response stream ended before completion"}}`)
	}
	return body
}
