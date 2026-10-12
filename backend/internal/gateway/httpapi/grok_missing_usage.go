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

const (
	grokMissingUsageErrorCode = "grok_missing_usage"
	grokMissingUsageMessage   = "xAI upstream returned a successful chat completion without billable usage"
)

// NewGrokMissingUsageFailure creates the missing-usage failover error and records Grok Ops diagnostics.
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
