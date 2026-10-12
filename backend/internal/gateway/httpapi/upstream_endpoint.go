package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	protocolcore "github.com/TokenFlux/TokenRouter/internal/protocol"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

const openAIUpstreamEndpointContextKey = "openai_actual_upstream_endpoint"

// resolveOpenAITextProtocolForAttempt resolves the provider protocol and records this attempt's endpoint before forwarding.
func resolveOpenAITextProtocolForAttempt(
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	preferred providercore.TextProtocol,
) providercore.TextProtocol {
	// API key providers resolve the preferred protocol through configured route mode.
	// Other provider types default to Responses; a resolved Chat route overrides that default below.
	protocol := providercore.TextProtocolResponses
	if provider != nil && provider.Record.Type == capability.ProviderTypeAPIKey {
		protocol = providercore.ResolveUpstreamTextProtocol(provider.Record.Extra, preferred)
	}

	if provider != nil && provider.Route.Protocol() == protocolcore.ProtocolOpenAIChatCompletions {
		protocol = providercore.TextProtocolChatCompletions
	}
	if provider != nil && provider.Route.Protocol() == protocolcore.ProtocolOpenAIResponses {
		protocol = providercore.TextProtocolResponses
	}
	endpoint := "/v1/responses"
	if protocol == providercore.TextProtocolChatCompletions {
		endpoint = "/v1/chat/completions"
	}
	SetActualOpenAIUpstreamEndpoint(c, endpoint)
	return protocol
}

// SetActualOpenAIUpstreamEndpoint records the selected endpoint for error paths
// that have no forwarding result for usage or Ops logging.
func SetActualOpenAIUpstreamEndpoint(c *gin.Context, endpoint string) {
	if c == nil {
		return
	}
	if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
		c.Set(openAIUpstreamEndpointContextKey, endpoint)
	}
}

// ClearActualOpenAIUpstreamEndpoint clears the recorded attempt endpoint.
// Gin context is reused between attempts, so clear the previous endpoint before forwarding.
func ClearActualOpenAIUpstreamEndpoint(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(openAIUpstreamEndpointContextKey, "")
}

// GetActualOpenAIUpstreamEndpoint returns the most recently recorded attempt endpoint.
func GetActualOpenAIUpstreamEndpoint(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, exists := c.Get(openAIUpstreamEndpointContextKey)
	if !exists {
		return ""
	}
	endpoint, _ := value.(string)
	return strings.TrimSpace(endpoint)
}
