package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	protocolcore "github.com/TokenFlux/TokenRouter/internal/protocol"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// An attempt can fail before a forwarding result supplies its actual endpoint.
// Recording the selected Chat or Responses endpoint in request-local state lets
// failure capture attribute that attempt; clearing the value keeps subsequent
// attempts from inheriting stale attribution.

// openAIUpstreamEndpointContextKey names the request-local endpoint value read by
// failure capture when execution returns no result carrying upstream metadata.
const openAIUpstreamEndpointContextKey = "openai_actual_upstream_endpoint"

// resolveOpenAITextProtocolForAttempt chooses this attempt's text protocol and
// records its endpoint for errors with no forwarding result. API Key settings
// interpret preferred; an already resolved Chat or Responses route then takes
// precedence over that choice. Other targets begin with Responses.
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

	// Use the resolved route ahead of raw provider preferences, because scheduling
	// may have selected a different protocol conversion path for this attempt.
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

// SetActualOpenAIUpstreamEndpoint records a nonblank trimmed endpoint for this
// attempt. Nil contexts and blank values are ignored; callers clear prior
// attempt state separately before forwarding so omitted updates cannot reuse
// it.
func SetActualOpenAIUpstreamEndpoint(c *gin.Context, endpoint string) {
	if c == nil {
		return
	}
	if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
		c.Set(openAIUpstreamEndpointContextKey, endpoint)
	}
}

// ClearActualOpenAIUpstreamEndpoint clears the endpoint retained across
// attempts in Gin context. Call before starting another forwarding attempt so
// failure capture cannot attribute an unset endpoint to the previous provider
// exchange.
func ClearActualOpenAIUpstreamEndpoint(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(openAIUpstreamEndpointContextKey, "")
}

// GetActualOpenAIUpstreamEndpoint returns the trimmed endpoint retained for the
// current attempt. A missing context, absent value or value of another type
// yields empty, allowing error capture to use its protocol default.
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
