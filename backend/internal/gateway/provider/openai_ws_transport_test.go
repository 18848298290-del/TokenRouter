package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// TestResponsesWSTransportUsesNativeProtocols declares WS and HTTP protocols
// for OAuth and API Key records with conflicting legacy flags. Transport
// follows the protocol declaration, and an empty protocol list receives the
// HTTP decision.
func TestResponsesWSTransportUsesNativeProtocols(t *testing.T) {
	for _, typ := range []string{"oauth", "apikey"} {
		p := &providercore.Record{Platform: "openai", Type: typ, Credentials: map[string]any{"upstream_protocols": []string{"openai_responses_websocket"}}, Extra: map[string]any{"openai_ws_enabled": false, "openai_ws_force_http": true}}
		require.Equal(t, egress.OpenAIUpstreamTransportResponsesWebsocketV2, ResolveOpenAIWSTransport(p).Transport)
		p.Credentials["upstream_protocols"] = []string{"openai_responses"}
		require.Equal(t, egress.OpenAIUpstreamTransportHTTPSSE, ResolveOpenAIWSTransport(p).Transport)
		p.Credentials["upstream_protocols"] = []string{}
		require.Equal(t, egress.OpenAIUpstreamTransportHTTPSSE, ResolveOpenAIWSTransport(p).Transport)
	}
}
