package provider

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	protocolbridge "github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestShouldFlattenOpenAIResponsesNamespaces(t *testing.T) {
	oauth := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	apiKey := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}
	grokOAuth := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}
	flattenOAuth := &ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type:  capability.ProviderTypeOAuth,
			Extra: map[string]any{"openai_responses_flatten_namespaces": true},
		},
	}
	flattenAPIKey := &ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type:  capability.ProviderTypeAPIKey,
			Extra: map[string]any{"openai_responses_flatten_namespaces": true},
		},
	}

	tests := []struct {
		name               string
		provider           *ExecutionProvider
		transport          egress.OpenAIUpstreamTransport
		passthroughEnabled bool
		compactPath        bool
		want               bool
	}{
		{name: "oauth_http_default_preserves", provider: oauth, transport: egress.OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "oauth_http_passthrough_default_preserves", provider: oauth, transport: egress.OpenAIUpstreamTransportHTTPSSE, passthroughEnabled: true, want: false},
		{name: "oauth_wsv2_default_preserves", provider: oauth, transport: egress.OpenAIUpstreamTransportResponsesWebsocketV2, want: false},
		// Compact uses a narrower schema and requires flattened tools.
		{name: "oauth_compact_flattens", provider: oauth, transport: egress.OpenAIUpstreamTransportHTTPSSE, compactPath: true, want: true},
		{name: "oauth_compact_wsv2_preserves", provider: oauth, transport: egress.OpenAIUpstreamTransportResponsesWebsocketV2, compactPath: true, want: false},
		{name: "apikey_compact", provider: apiKey, transport: egress.OpenAIUpstreamTransportHTTPSSE, compactPath: true, want: false},
		{name: "oauth_flatten_enabled_http", provider: flattenOAuth, transport: egress.OpenAIUpstreamTransportHTTPSSE, want: true},
		{name: "oauth_flatten_enabled_http_passthrough", provider: flattenOAuth, transport: egress.OpenAIUpstreamTransportHTTPSSE, passthroughEnabled: true, want: true},
		// WSv2 forwards events without restoring names, so flattening would expose unmatched names to clients.
		{name: "oauth_flatten_enabled_wsv2", provider: flattenOAuth, transport: egress.OpenAIUpstreamTransportResponsesWebsocketV2, want: false},
		// Passthrough selects HTTP before the WSv2 branch, so the flattening setting still applies.
		{name: "oauth_flatten_enabled_wsv2_passthrough", provider: flattenOAuth, transport: egress.OpenAIUpstreamTransportResponsesWebsocketV2, passthroughEnabled: true, want: true},
		{name: "apikey_flatten_enabled_http", provider: flattenAPIKey, transport: egress.OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "apikey_http", provider: apiKey, transport: egress.OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "grok_oauth_http", provider: grokOAuth, transport: egress.OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "nil_provider", provider: nil, transport: egress.OpenAIUpstreamTransportHTTPSSE, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ShouldFlattenOpenAIResponsesNamespaces(
				tt.provider, tt.transport, tt.passthroughEnabled, tt.compactPath,
			))
		})
	}
}

func TestShouldKeepOpenAIResponsesToolCallNamespaces(t *testing.T) {
	oauth := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	apiKey := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}
	setupToken := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeSetupToken}}
	flattenOAuth := &ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type:  capability.ProviderTypeOAuth,
			Extra: map[string]any{"openai_responses_flatten_namespaces": true},
		},
	}

	tests := []struct {
		name               string
		provider           *ExecutionProvider
		transport          egress.OpenAIUpstreamTransport
		passthroughEnabled bool
		compactPath        bool
		body               []byte
		want               bool
	}{
		{name: "oauth_http_keeps", provider: oauth, transport: egress.OpenAIUpstreamTransportHTTPSSE, want: true},
		{name: "oauth_http_passthrough_keeps", provider: oauth, transport: egress.OpenAIUpstreamTransportHTTPSSE, passthroughEnabled: true, want: true},
		{name: "oauth_compact_strips", provider: oauth, transport: egress.OpenAIUpstreamTransportHTTPSSE, compactPath: true, want: false},
		{name: "oauth_flatten_enabled_strips", provider: flattenOAuth, transport: egress.OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "oauth_wsv2_keeps", provider: oauth, transport: egress.OpenAIUpstreamTransportResponsesWebsocketV2, want: true},
		{name: "oauth_compact_wsv2_strips", provider: oauth, transport: egress.OpenAIUpstreamTransportResponsesWebsocketV2, compactPath: true, want: false},
		// API Key requests normally use the standard schema. Declared namespace tools
		// require matching namespaces on historical calls for custom upstreams.
		{name: "apikey_without_namespace_tool_strips", provider: apiKey, transport: egress.OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "apikey_with_namespace_tool_keeps", provider: apiKey, transport: egress.OpenAIUpstreamTransportHTTPSSE, body: []byte(`{"tools":[{"type":"namespace","name":"mcp__codex_app","tools":[]}]}`), want: true},
		{name: "apikey_with_mixed_case_namespace_tool_keeps", provider: apiKey, transport: egress.OpenAIUpstreamTransportHTTPSSE, body: []byte(`{"tools":[{"type":" Namespace ","name":"mcp__codex_app","tools":[]}]}`), want: true},
		{name: "apikey_function_tool_with_namespace_field_strips", provider: apiKey, transport: egress.OpenAIUpstreamTransportHTTPSSE, body: []byte(`{"tools":[{"type":"function","name":"automation_update","namespace":"mcp__codex_app"}]}`), want: false},
		{name: "apikey_compact_with_namespace_tool_strips", provider: apiKey, transport: egress.OpenAIUpstreamTransportHTTPSSE, compactPath: true, body: []byte(`{"tools":[{"type":"namespace","name":"mcp__codex_app","tools":[]}]}`), want: false},
		{name: "setup_token_keeps", provider: setupToken, transport: egress.OpenAIUpstreamTransportHTTPSSE, want: true},
		{name: "nil_provider", provider: nil, transport: egress.OpenAIUpstreamTransportHTTPSSE, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ShouldKeepOpenAIResponsesToolCallNamespaces(
				tt.provider, tt.transport, tt.passthroughEnabled, tt.compactPath, tt.body,
			))
		})
	}
}

func TestShouldStripOpenAIResponsesInputNamespaces(t *testing.T) {
	oauth := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}
	apiKey := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}
	setupToken := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeSetupToken}}
	grokOAuth := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}}

	tests := []struct {
		name               string
		provider           *ExecutionProvider
		transport          egress.OpenAIUpstreamTransport
		passthroughEnabled bool
		want               bool
	}{
		{name: "oauth_http", provider: oauth, transport: egress.OpenAIUpstreamTransportHTTPSSE, want: true},
		{name: "apikey_http", provider: apiKey, transport: egress.OpenAIUpstreamTransportHTTPSSE, want: true},
		{name: "oauth_wsv2", provider: oauth, transport: egress.OpenAIUpstreamTransportResponsesWebsocketV2, want: false},
		{name: "apikey_wsv2", provider: apiKey, transport: egress.OpenAIUpstreamTransportResponsesWebsocketV2, want: false},
		{name: "oauth_wsv2_passthrough", provider: oauth, transport: egress.OpenAIUpstreamTransportResponsesWebsocketV2, passthroughEnabled: true, want: true},
		{name: "apikey_wsv2_passthrough", provider: apiKey, transport: egress.OpenAIUpstreamTransportResponsesWebsocketV2, passthroughEnabled: true, want: true},
		{name: "setup_token_http", provider: setupToken, transport: egress.OpenAIUpstreamTransportHTTPSSE, want: true},
		{name: "grok_oauth_http", provider: grokOAuth, transport: egress.OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "nil_provider", provider: nil, transport: egress.OpenAIUpstreamTransportHTTPSSE, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ShouldStripOpenAIResponsesInputNamespaces(tt.provider, tt.transport, tt.passthroughEnabled))
		})
	}
}

func TestStripOpenAIResponsesInputNamespaces(t *testing.T) {
	body := []byte(`{
		"meta":9007199254740993,
		"scientific":1.25e+42,
		"escaped":"line\\n\\u003ctag\\u003e",
		"tools":[{"type":"function","name":"keep","namespace":"tool-namespace"}],
		"input":[
			{"type":"function_call","namespace":"n0","name":"one","content":{"namespace":"nested"},"large":9007199254740993},
			{"type":"message","namespace":"n1","content":[{"type":"input_text","text":"hello","namespace":"nested-content"}]},
			{"type":"custom_tool_call","namespace":"n2","input":"{}"},
			{"type":"function_call_output","namespace":"n3","output":"ok"},
			{"type":"item","namespace":"n4"},
			{"type":"item","namespace":"n5"},
			{"type":"item","namespace":"n6"},
			{"type":"item","namespace":"n7"}
		]
	}`)

	stripped, err := protocolbridge.StripOpenAIResponsesInputNamespaces(body, false)
	require.NoError(t, err)
	for index := range 8 {
		require.False(t, gjson.GetBytes(stripped, "input."+strconv.Itoa(index)+".namespace").Exists())
	}
	require.Equal(t, "nested", gjson.GetBytes(stripped, "input.0.content.namespace").String())
	require.Equal(t, "nested-content", gjson.GetBytes(stripped, "input.1.content.0.namespace").String())
	require.Equal(t, "tool-namespace", gjson.GetBytes(stripped, "tools.0.namespace").String())
	require.Equal(t, gjson.GetBytes(body, "meta").Raw, gjson.GetBytes(stripped, "meta").Raw)
	require.Equal(t, gjson.GetBytes(body, "scientific").Raw, gjson.GetBytes(stripped, "scientific").Raw)
	require.Equal(t, gjson.GetBytes(body, "escaped").Raw, gjson.GetBytes(stripped, "escaped").Raw)
	require.Equal(t, gjson.GetBytes(body, "input.0.large").Raw, gjson.GetBytes(stripped, "input.0.large").Raw)
}

func TestStripOpenAIResponsesInputNamespacesLeavesOtherShapesByteExact(t *testing.T) {
	tests := [][]byte{
		[]byte(`{"input":"text","namespace":"top-level"}`),
		[]byte(`{"input":{"namespace":"single-object"}}`),
		[]byte(`{"input":[{"content":{"namespace":"nested-only"}}],"tools":[{"namespace":"keep"}]}`),
	}
	for _, body := range tests {
		for _, keepToolCallNamespaces := range []bool{false, true} {
			stripped, err := protocolbridge.StripOpenAIResponsesInputNamespaces(body, keepToolCallNamespaces)
			require.NoError(t, err)
			require.Equal(t, body, stripped)
		}
	}
}

// TestStripOpenAIResponsesInputNamespacesKeepsToolCallNamespaces checks that preservation applies to calls while other history is cleaned.
func TestStripOpenAIResponsesInputNamespacesKeepsToolCallNamespaces(t *testing.T) {
	body := []byte(`{
		"meta":9007199254740993,
		"input":[
			{"type":"function_call","namespace":"collaboration","name":"spawn_agent","arguments":"{}","large":9007199254740993},
			{"type":"custom_tool_call","namespace":"codex_app","name":"exec","input":"{}"},
			{"type":"tool_call","namespace":"mcp__codex_apps__gmail","name":"send"},
			{"type":"mcp_tool_call","namespace":"mcp__codex_apps__gmail","name":"list"},
			{"type":"message","namespace":"leftover","role":"assistant","content":[]},
			{"type":"function_call_output","namespace":"leftover","output":"ok"},
			{"type":"reasoning","namespace":"leftover"},
			{"type":"item","namespace":"leftover"}
		]
	}`)

	stripped, err := protocolbridge.StripOpenAIResponsesInputNamespaces(body, true)
	require.NoError(t, err)

	require.Equal(t, "collaboration", gjson.GetBytes(stripped, "input.0.namespace").String())
	require.Equal(t, "codex_app", gjson.GetBytes(stripped, "input.1.namespace").String())
	require.Equal(t, "mcp__codex_apps__gmail", gjson.GetBytes(stripped, "input.2.namespace").String())
	require.Equal(t, "mcp__codex_apps__gmail", gjson.GetBytes(stripped, "input.3.namespace").String())
	for index := 4; index < 8; index++ {
		require.False(t, gjson.GetBytes(stripped, "input."+strconv.Itoa(index)+".namespace").Exists())
	}
	require.Equal(t, gjson.GetBytes(body, "meta").Raw, gjson.GetBytes(stripped, "meta").Raw)
	require.Equal(t, gjson.GetBytes(body, "input.0.large").Raw, gjson.GetBytes(stripped, "input.0.large").Raw)

	// Type matching ignores case and surrounding whitespace.
	mixedCase := []byte(`{"input":[{"type":" Function_Call ","namespace":"collaboration","name":"spawn_agent"}]}`)
	keptMixedCase, err := protocolbridge.StripOpenAIResponsesInputNamespaces(mixedCase, true)
	require.NoError(t, err)
	require.Equal(t, mixedCase, keptMixedCase)

	// An input containing only calls can pass through byte for byte.
	callsOnly := []byte(`{"input":[{"type":"function_call","namespace":"collaboration","name":"spawn_agent"}]}`)
	unchanged, err := protocolbridge.StripOpenAIResponsesInputNamespaces(callsOnly, true)
	require.NoError(t, err)
	require.Equal(t, callsOnly, unchanged)

	strippedAll, err := protocolbridge.StripOpenAIResponsesInputNamespaces(body, false)
	require.NoError(t, err)
	for index := range 8 {
		require.False(t, gjson.GetBytes(strippedAll, "input."+strconv.Itoa(index)+".namespace").Exists())
	}
}
