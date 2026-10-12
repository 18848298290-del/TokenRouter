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

// openAIRawStreamTruncatedUpstreamMessage identifies truncated raw Chat Completions streams in Ops.
const openAIRawStreamTruncatedUpstreamMessage = "Upstream Chat Completions stream ended before any terminal chunk"

// NewOpenAIRawTruncationFailure builds a retryable failure for truncation before client output.
// The caller must verify that response headers remain uncommitted before retrying another provider.
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

// RecordOpenAIRawTruncation records truncation in Ops for error logs and provider health statistics.
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

// openAIRawStreamTruncatedMessage includes the transport cause when available.
// A clean EOF has no underlying error to append.
func openAIRawStreamTruncatedMessage(cause error) string {
	if cause == nil || errors.Is(cause, openai.ErrOpenAIUpstreamStreamTruncated) {
		return openAIRawStreamTruncatedUpstreamMessage
	}
	return openAIRawStreamTruncatedUpstreamMessage + ": " + cause.Error()
}

// openAIRawStreamTruncatedErrorBody builds a failover body using the same code and message
// classification as stream read failures after output has started.
func openAIRawStreamTruncatedErrorBody(cause error) []byte {
	code, message := openai.ClassifyUpstreamStreamReadError(cause)
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    "upstream_error",
			"code":    code,
			"message": message,
		},
	})
	if err != nil {
		return []byte(`{"error":{"type":"upstream_error","code":"` + openai.OpenAIUpstreamStreamTruncatedCode +
			`","message":"Upstream response stream ended before completion"}}`)
	}
	return body
}
