package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// empty_completion.go constructs failover errors and Ops observations for
// empty OpenAI completion streams.
// Chat silent refusal and empty Responses completion share the same failure
// encoding while retaining different Ops explanations. The caller establishes
// that output and usage are absent before invoking these constructors, which
// record failure evidence and retain the trimmed upstream request identifier.

// Empty-completion messages distinguish the two upstream response shapes for Ops
// while their constructed failures use the common silent-refusal classification.
const (
	// openAISilentRefusalUpstreamMessage describes empty stopped Chat output without usage.
	openAISilentRefusalUpstreamMessage = "OpenAI upstream returned an empty completion stream with finish_reason=stop and no usage"
	// openAIResponsesEmptyCompletedMessage describes empty Responses terminal output without usage.
	openAIResponsesEmptyCompletedMessage = "OpenAI upstream returned an empty response.completed stream with no output and no usage"
)

// UpstreamErrorProvider contains the selected provider's ID, name, and platform for error observation.
type UpstreamErrorProvider struct {
	ID             int64  // selected provider identifier
	Name, Platform string // Name: diagnostic display name; Platform: provider protocol family
}

// NewOpenAISilentRefusalFailoverError records Chat silent refusal and returns a failover signal.
// Callers establish the empty response condition before invocation. Nil provider
// uses zero identity and OpenAI platform; an upstream request ID is trimmed
// into the observation and returned response headers for later failure handling.
func NewOpenAISilentRefusalFailoverError(c *gin.Context, provider *UpstreamErrorProvider, upstreamRequestID string) *forwardcore.UpstreamFailoverError {
	providerID := int64(0)
	providerName := ""
	platform := capability.PlatformOpenAI
	if provider != nil {
		providerID = provider.ID
		providerName = provider.Name
		platform = provider.Platform
	}
	SetOpsUpstreamError(c, http.StatusBadGateway, openAISilentRefusalUpstreamMessage, "")
	AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
		Platform:           platform,
		ProviderID:         providerID,
		ProviderName:       providerName,
		UpstreamStatusCode: http.StatusBadGateway,
		UpstreamRequestID:  upstreamRequestID,
		Kind:               "failover",
		Message:            openAISilentRefusalUpstreamMessage,
	})

	headers := http.Header{}
	if strings.TrimSpace(upstreamRequestID) != "" {
		headers.Set("x-request-id", strings.TrimSpace(upstreamRequestID))
	}
	return &forwardcore.UpstreamFailoverError{
		StatusCode:      http.StatusBadGateway,
		ResponseBody:    forwardcore.OpenAISilentRefusalErrorBody(),
		ResponseHeaders: headers,
	}
}

// NewOpenAIResponsesEmptyCompletedFailoverError marks an empty Responses terminal event as a retryable upstream
// failure. Callers establish absent visible output, usage, and error before invocation.
// Nil provider uses zero identity and OpenAI platform. It records the distinct
// empty-completed explanation while returning the same silent-refusal body and
// trimmed upstream request header used by the Chat constructor.
func NewOpenAIResponsesEmptyCompletedFailoverError(c *gin.Context, provider *UpstreamErrorProvider, upstreamRequestID string) *forwardcore.UpstreamFailoverError {
	providerID := int64(0)
	providerName := ""
	platform := capability.PlatformOpenAI
	if provider != nil {
		providerID = provider.ID
		providerName = provider.Name
		platform = provider.Platform
	}
	SetOpsUpstreamError(c, http.StatusBadGateway, openAIResponsesEmptyCompletedMessage, "")
	AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
		Platform:           platform,
		ProviderID:         providerID,
		ProviderName:       providerName,
		UpstreamStatusCode: http.StatusBadGateway,
		UpstreamRequestID:  upstreamRequestID,
		Kind:               "failover",
		Message:            openAIResponsesEmptyCompletedMessage,
	})

	headers := http.Header{}
	if strings.TrimSpace(upstreamRequestID) != "" {
		headers.Set("x-request-id", strings.TrimSpace(upstreamRequestID))
	}
	return &forwardcore.UpstreamFailoverError{
		StatusCode:      http.StatusBadGateway,
		ResponseBody:    forwardcore.OpenAISilentRefusalErrorBody(),
		ResponseHeaders: headers,
	}
}
