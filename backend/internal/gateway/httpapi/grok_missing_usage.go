package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// A successful Grok Chat response needs positive aggregate token usage before
// ordinary billing and client delivery can proceed. Missing usage becomes a
// stable failover error with an upstream request ID, and Ops records the same
// reason for the selected provider.

// These values give missing billable Chat usage a stable client code and Ops
// message, allowing request orchestration to recognize the failover consistently.
const (
	grokMissingUsageErrorCode = "grok_missing_usage"
	grokMissingUsageMessage   = "xAI upstream returned a successful chat completion without billable usage"
)

// NewGrokMissingUsageFailure records missing Grok Chat usage and creates a 502
// failover error. A nil provider contributes zero/empty identity; a trimmed
// upstream request ID is retained in both the Ops event and response headers.
func NewGrokMissingUsageFailure(c *gin.Context, provider *gatewayprovider.ExecutionProvider, upstreamRequestID string) *forwardcore.UpstreamFailoverError {
	providerID := int64(0)
	providerName := ""
	if provider != nil {
		providerID = provider.Record.ID
		providerName = provider.Record.Name
	}
	SetOpsUpstreamError(c, http.StatusBadGateway, grokMissingUsageMessage, "")
	AppendOpsUpstreamError(c, ops.OpsUpstreamErrorEvent{
		Platform:           capability.PlatformGrok,
		ProviderID:         providerID,
		ProviderName:       providerName,
		UpstreamStatusCode: http.StatusBadGateway,
		UpstreamRequestID:  strings.TrimSpace(upstreamRequestID),
		Kind:               "failover",
		Message:            grokMissingUsageMessage,
	})

	// This fixed string-only envelope can be marshalled directly; its bytes carry
	// the stable missing-usage reason into provider failover and final presentation.
	body, _ := json.Marshal(gin.H{
		"error": gin.H{
			"type":    "upstream_error",
			"code":    grokMissingUsageErrorCode,
			"message": grokMissingUsageMessage,
		},
	})
	headers := http.Header{}
	if requestID := strings.TrimSpace(upstreamRequestID); requestID != "" {
		headers.Set("x-request-id", requestID)
	}
	return &forwardcore.UpstreamFailoverError{
		StatusCode:      http.StatusBadGateway,
		ResponseBody:    body,
		ResponseHeaders: headers,
	}
}
