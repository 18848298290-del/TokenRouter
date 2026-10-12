package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
)

func (p *OpenAIResponseOutput) BindResponseProvider(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, responseID string) {
	if p == nil || provider == nil || provider.Record.ID <= 0 {
		return
	}
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return
	}
	store := p.Responses
	if store == nil {
		return
	}
	groupID := OpenAIResponseGroupID(c)
	ttl := p.ResponseTTL()
	gatewayprovider.LogOpenAIWSBindResponseProviderWarn(groupID, provider.Record.ID, responseID, store.BindResponseProvider(ctx, groupID, responseID, provider.Record.ID, ttl))
	if owner, ok := ResponseOwnerFromContext(c); ok {
		if err := store.BindHTTPResponseOwner(ctx, groupID, responseID, owner.UserID, owner.APIKeyID, p.ResponseTTL()); err != nil {
			logging.L().Warn(
				"openai.http_bind_response_owner_failed",
				zap.Int64("group_id", groupID),
				zap.Int64("provider_id", provider.Record.ID),
				zap.Int64("user_id", owner.UserID),
				zap.Int64("api_key_id", owner.APIKeyID),
				zap.String("response_id", gatewayprovider.TruncateOpenAIWSLogValue(responseID, gatewayprovider.OpenAIWSIDValueMaxLen)),
				zap.Error(err),
			)
		}
	}
}

func (p *OpenAIResponseOutput) ProtocolError(resp *http.Response, c *gin.Context, message string) error {
	message = logredact.SanitizeUpstreamQueries(strings.TrimSpace(message))
	if message == "" {
		message = "Upstream returned an invalid non-streaming response"
	}
	SetOpsUpstreamError(c, http.StatusBadGateway, message, "")
	// After a body-signaled Compact heartbeat commits 200, terminate errors with response.failed.
	if OpenAICompactClientWantsStream(c) && StopOpenAICompactSSEKeepaliveCommitted(c) {
		WriteOpenAICompactSSEFailureMessage(c, http.StatusBadGateway, "upstream_error", message, MarkOpsStreamError)
		return fmt.Errorf("non-streaming openai protocol error: %s", message)
	}
	egressadapter.WriteFilteredHeaders(c.Writer.Header(), resp.Header, p.Headers)
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.JSON(http.StatusBadGateway, gin.H{
		"error": gin.H{
			"type":    "upstream_error",
			"message": message,
		},
	})
	return fmt.Errorf("non-streaming openai protocol error: %s", message)
}

// OpenAIResponseGroupID reads the updated HTTP request key group used for response ownership.
func OpenAIResponseGroupID(c *gin.Context) int64 {
	if c == nil {
		return 0
	}
	value, exists := c.Get("api_key")
	if !exists {
		return 0
	}
	key, ok := value.(*apikey.APIKey)
	if !ok || key == nil || key.GroupID == nil {
		return 0
	}
	return *key.GroupID
}
