package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestResponsesWSMigrationUsesSavedModeAndPreservesProtocols(t *testing.T) {
	for _, typ := range []string{"oauth", "apikey"} {
		for _, mode := range []string{"ctx_pool", "shared", "dedicated", "passthrough", "http_bridge", "off"} {
			t.Run(typ+"/"+mode, func(t *testing.T) {
				key := "openai_" + typ + "_responses_websockets_v2_mode"
				value := &Record{Platform: "openai", Type: typ, Extra: map[string]any{key: mode, "openai_ws_enabled": false}, Credentials: map[string]any{UpstreamProtocolsKey: []string{"openai_responses", "openai_responses_websocket"}}}
				require.NoError(t, NormalizeProviderProtocols(value))
				want := ResponsesWSPooled
				if mode == "passthrough" {
					want = ResponsesWSPerSession
				}
				require.Equal(t, want, value.ResponsesWSConnectionMode())
				require.Equal(t, mode != "http_bridge", value.SupportsResponsesWS())
				require.NotContains(t, value.Extra, key)
				require.NotContains(t, value.Extra, "openai_ws_enabled")
				require.NoError(t, NormalizeProviderProtocols(value))
				require.Equal(t, want, value.ResponsesWSConnectionMode())
			})
		}
	}
	value := &Record{Platform: "openai", Type: "apikey", Credentials: map[string]any{UpstreamProtocolsKey: []capability.ProtocolID{}}, Extra: map[string]any{ResponsesWSConnectionModeKey: ResponsesWSPerSession, "openai_ws_force_http": true}}
	require.NoError(t, NormalizeProviderProtocols(value))
	require.Empty(t, value.UpstreamProtocols())
	require.Equal(t, ResponsesWSPerSession, value.ResponsesWSConnectionMode())
	value.Extra[ResponsesWSConnectionModeKey] = "unknown"
	require.Error(t, NormalizeProviderProtocols(value))
}

// TestResponsesWSLegacyBridgeValidatesOriginalProtocols checks that legacy bridge settings cannot turn invalid
// protocol input into a valid empty set.
func TestResponsesWSLegacyBridgeValidatesOriginalProtocols(t *testing.T) {
	for _, extra := range []map[string]any{{"openai_apikey_responses_websockets_v2_mode": "http_bridge"}, {"openai_ws_force_http": true}} {
		for _, raw := range []any{"openai_responses", nil, false, []any{1}, []string{"unknown"}, []string{"openai_responses_websocket", "openai_responses_websocket"}} {
			value := &Record{Platform: "openai", Type: "apikey", Credentials: map[string]any{UpstreamProtocolsKey: raw}, Extra: extra}
			require.Error(t, NormalizeProviderProtocols(value), "protocols=%#v", raw)
			require.Equal(t, raw, value.Credentials[UpstreamProtocolsKey])
		}
	}
}
