package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
)

// TestNewOpenAIGatewayService_InitializesOpenAIWSResolver constructs a WS
// executor from the shared fixture bindings. Resolving a nil provider returns
// HTTP/SSE and the protocol_http reason, confirming that construction retains
// the bound selector.
func TestNewOpenAIGatewayService_InitializesOpenAIWSResolver(t *testing.T) {
	options := &wsFixtureOptions{}
	fixture := newWSFixture(wsFixtureInputs{options: options})
	svc := NewOpenAIWebSocketExecutor(fixture.OpenAIWSDependencies)

	decision := svc.Selection.ResolveTransport(nil)
	require.Equal(t, egress.OpenAIUpstreamTransportHTTPSSE, decision.Transport)
	require.Equal(t, "protocol_http", decision.Reason)
}
