package provider

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	openaicore "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/protocol/wirejson"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// openAIResponsesInputTextMaxChars is the long-text passthrough fixture size.
const openAIResponsesInputTextMaxChars = 10000000

func TestWebSocketCompatibilityNormalizesTriggerAfterPairedOutputCleanup(t *testing.T) {
	body := []byte(`{"type":"response.create","model":"gpt-5.4","input":[{"type":"compaction_trigger"},{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"ok"},{"type":"message","role":"user","content":"visible"}]}`)
	provider := &providercore.Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}

	normalized, changed, err := NormalizeOpenAIResponsesWebSocketCompatibilityBody(body, provider, false)
	require.NoError(t, err)
	require.True(t, changed)
	items := gjson.GetBytes(normalized, "input").Array()
	require.Len(t, items, 4)
	require.Equal(t, "function_call", items[0].Get("type").String())
	require.Equal(t, "function_call_output", items[1].Get("type").String())
	require.Equal(t, "message", items[2].Get("type").String())
	require.Equal(t, "compaction_trigger", items[3].Get("type").String())
}

// TestNormalizeOpenAIResponsesWebSocketCompatibilityBodyStripsReasoningContentOnlyForOpenAI checks platform-specific reasoning cleanup.
func TestNormalizeOpenAIResponsesWebSocketCompatibilityBodyStripsReasoningContentOnlyForOpenAI(t *testing.T) {
	body := []byte(`{"type":"response.create","model":"gpt-5.6-sol","store":true,"input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"keep"}],"content":[{"type":"reasoning_text","text":"remove"}]}]}`)
	for _, providerType := range []string{capability.ProviderTypeAPIKey, capability.ProviderTypeOAuth} {
		normalized, changed, err := NormalizeOpenAIResponsesWebSocketCompatibilityBody(body, ExecutionProtocolRecord(&ExecutionProvider{
			Record: providercore.Record{
				LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
				Type: providerType,
			},
		}), false)
		require.NoError(t, err)
		require.True(t, changed)
		require.False(t, gjson.GetBytes(normalized, "input.0.content").Exists())
		require.Equal(t, "keep", gjson.GetBytes(normalized, "input.0.summary.0.text").String())
	}

	normalized, changed, err := NormalizeOpenAIResponsesWebSocketCompatibilityBody(body, ExecutionProtocolRecord(&ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformZhipu,
			Type: capability.ProviderTypeAPIKey,
		},
	}), false)
	require.NoError(t, err)
	require.False(t, changed)
	require.JSONEq(t, string(body), string(normalized))
}

func TestNormalizeOpenAIPassthroughOAuthBody_RemovesUnsupportedUser(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello","user":"user_123","metadata":{"user_id":"user_123"},"prompt_cache_retention":"24h","safety_identifier":"sid","stream_options":{"include_usage":true}}`)

	normalized, changed, err := openai.NormalizeOpenAIPassthroughOAuthBody(body, false)
	require.NoError(t, err)
	require.True(t, changed)
	for _, field := range openai.OpenAIChatGPTInternalUnsupportedFields {
		require.False(t, gjson.GetBytes(normalized, field).Exists(), "%s should be stripped", field)
	}
	require.True(t, gjson.GetBytes(normalized, "stream").Bool())
	require.False(t, gjson.GetBytes(normalized, "store").Bool())
}

func TestNormalizeOpenAIPassthroughOAuthBody_NormalizesCompatibilityFields(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","prompt":"hello","commands":["unsupported"],"truncation":"auto","stop_sequences":["END"],"chat_template_kwargs":{"enable_thinking":true}}`)

	normalized, changed, err := openai.NormalizeOpenAIPassthroughOAuthBody(body, false)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "hello", gjson.GetBytes(normalized, "input.0.content").String())
	for _, field := range []string{"prompt", "commands", "truncation", "stop_sequences", "chat_template_kwargs"} {
		require.False(t, gjson.GetBytes(normalized, field).Exists(), field)
	}
}

func TestNormalizeOpenAIPassthroughOAuthBody_NormalizesReasoningMode(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","input":"hello","reasoning":{"mode":"pro"}}`)

	normalized, changed, err := openai.NormalizeOpenAIPassthroughOAuthBody(body, false)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "max", gjson.GetBytes(normalized, "reasoning.effort").String())
	require.False(t, gjson.GetBytes(normalized, "reasoning.mode").Exists())
}

func TestNormalizeOpenAIOAuthResponsesCompatibilityBody_PreservesExplicitInput(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":"explicit","prompt":"legacy"}`)

	normalized, changed, err := openaicore.NormalizeOpenAIOAuthResponsesCompatibilityBody(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "explicit", gjson.GetBytes(normalized, "input").String())
	require.False(t, gjson.GetBytes(normalized, "prompt").Exists())
}

func TestNormalizeOpenAIResponsesWebSocketCompatibilityBody_OnlyStripsOAuthFields(t *testing.T) {
	body := []byte(`{"type":"response.create","prompt":"hello","commands":{},"truncation":"auto","stop_sequences":["END"],"chat_template_kwargs":{"enable_thinking":true}}`)

	oauthBody, changed, err := NormalizeOpenAIResponsesWebSocketCompatibilityBody(body, &providercore.Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}, false)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "hello", gjson.GetBytes(oauthBody, "input").String())
	for _, field := range []string{"prompt", "commands", "truncation", "stop_sequences", "chat_template_kwargs"} {
		require.False(t, gjson.GetBytes(oauthBody, field).Exists(), field)
	}

	apiKeyBody, changed, err := NormalizeOpenAIResponsesWebSocketCompatibilityBody(body, &providercore.Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}, false)
	require.NoError(t, err)
	require.False(t, changed)
	require.JSONEq(t, string(body), string(apiKeyBody))
}

func TestNormalizeOpenAIResponsesWebSocketCompatibilityBody_SanitizesNativeItemIDs(t *testing.T) {
	body := []byte(`{"type":"response.create","model":"gpt-5.6-sol","input":[` +
		`{"type":"custom_tool_call","id":"fc_wrong_custom","call_id":"call_custom_1","name":"apply_patch","input":"patch"},` +
		`{"type":"custom_tool_call","id":"ctc_valid","call_id":"call_custom_2","name":"apply_patch","input":"patch"},` +
		`{"type":"tool_search_call","id":"fc_wrong_search","call_id":"call_search_1","arguments":{"query":"docs"}},` +
		`{"type":"tool_search_call","id":"tsc_valid","call_id":"call_search_2","arguments":{"query":"docs"}}]}`)

	for _, oauth := range []bool{false, true} {
		providerType := capability.ProviderTypeAPIKey
		if oauth {
			providerType = capability.ProviderTypeOAuth
		}
		normalized, changed, err := NormalizeOpenAIResponsesWebSocketCompatibilityBody(body, &providercore.Record{Platform: capability.PlatformOpenAI, Type: providerType}, false)
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, "response.create", gjson.GetBytes(normalized, "type").String())
		require.False(t, gjson.GetBytes(normalized, "input.0.id").Exists())
		require.Equal(t, "ctc_valid", gjson.GetBytes(normalized, "input.1.id").String())
		require.False(t, gjson.GetBytes(normalized, "input.2.id").Exists())
		require.Equal(t, "tsc_valid", gjson.GetBytes(normalized, "input.3.id").String())
		// Responses call_id links a call to its output.
		require.Equal(t, "call_custom_1", gjson.GetBytes(normalized, "input.0.call_id").String())
		require.Equal(t, "call_search_1", gjson.GetBytes(normalized, "input.2.call_id").String())
	}
}

func TestNormalizeOpenAIResponsesWebSocketCompatibilityBody_APIKeyStoreFalseReplay(t *testing.T) {
	body := []byte(`{"type":"response.create","store":false,"parallel_tool_calls":true,"input":[` +
		`{"type":"reasoning","id":"rs_drop","summary":[]},` +
		`{"type":"reasoning","id":"rs_keep","call_id":"remove","encrypted_content":"cipher"},` +
		`{"type":"message","content":"continue"}` +
		`]}`)

	normalized, changed, err := NormalizeOpenAIResponsesWebSocketCompatibilityBody(body, &providercore.Record{
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
	}, false)

	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(normalized, "parallel_tool_calls").Exists())
	require.Equal(t, int64(2), gjson.GetBytes(normalized, "input.#").Int())
	require.False(t, gjson.GetBytes(normalized, "input.0.id").Exists())
	require.False(t, gjson.GetBytes(normalized, "input.0.call_id").Exists())
	require.True(t, gjson.GetBytes(normalized, "input.0.summary").IsArray())
	require.Equal(t, "message", gjson.GetBytes(normalized, "input.1.type").String())
}

func TestNormalizeOpenAIResponsesWebSocketCompatibilityBody_PreservesResponsesLiteParallelToolCalls(t *testing.T) {
	body := []byte(`{"type":"response.create","input":"hello","parallel_tool_calls":false}`)
	provider := &providercore.Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}

	normalized, changed, err := NormalizeOpenAIResponsesWebSocketCompatibilityBody(body, provider, true)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, gjson.False, gjson.GetBytes(normalized, "parallel_tool_calls").Type)

	normalized, changed, err = NormalizeOpenAIResponsesWebSocketCompatibilityBody(body, provider, false)
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(normalized, "parallel_tool_calls").Exists())
}

func TestNormalizeOpenAIResponsesReasoningMode(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantEffort string
	}{
		{name: "pro maps to max", body: `{"reasoning":{"mode":"pro"}}`, wantEffort: "max"},
		{name: "explicit effort wins", body: `{"reasoning":{"mode":"pro","effort":"high"}}`, wantEffort: "high"},
		{name: "other mode only removed", body: `{"reasoning":{"mode":"standard"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized, changed, err := openai.NormalizeOpenAIResponsesReasoningMode([]byte(tt.body))
			require.NoError(t, err)
			require.True(t, changed)
			require.False(t, gjson.GetBytes(normalized, "reasoning.mode").Exists())
			require.Equal(t, tt.wantEffort, gjson.GetBytes(normalized, "reasoning.effort").String())
		})
	}
}

func TestNormalizeOpenAIResponsesWebSocketCompatibilityBody_ReasoningModeProviderScope(t *testing.T) {
	body := []byte(`{"type":"response.create","reasoning":{"mode":"pro"}}`)
	for _, providerType := range []string{capability.ProviderTypeOAuth, capability.ProviderTypeSetupToken} {
		normalized, changed, err := NormalizeOpenAIResponsesWebSocketCompatibilityBody(body, &providercore.Record{Platform: capability.PlatformOpenAI, Type: providerType}, false)
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, "max", gjson.GetBytes(normalized, "reasoning.effort").String())
		require.False(t, gjson.GetBytes(normalized, "reasoning.mode").Exists())
	}
	apiKeyBody, changed, err := NormalizeOpenAIResponsesWebSocketCompatibilityBody(body, &providercore.Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}, false)
	require.NoError(t, err)
	require.False(t, changed)
	require.JSONEq(t, string(body), string(apiKeyBody))
}

func TestNormalizeOpenAIResponsesWebSocketCompatibilityBody_SanitizesToolSchemas(t *testing.T) {
	body := []byte(`{"type":"response.create","tools":[{"type":"function","name":"search","parameters":{"type":null,"properties":{"q":{"type":"string","pattern":"^(?=.*foo).+$"}}}}]}`)
	for _, providerType := range []string{capability.ProviderTypeAPIKey, capability.ProviderTypeOAuth} {
		normalized, changed, err := NormalizeOpenAIResponsesWebSocketCompatibilityBody(body, &providercore.Record{Platform: capability.PlatformOpenAI, Type: providerType}, false)
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, "object", gjson.GetBytes(normalized, "tools.0.parameters.type").String())
		require.False(t, gjson.GetBytes(normalized, "tools.0.parameters.properties.q.pattern").Exists())
	}
}

func TestNormalizeOpenAIResponseFormatSchemasBody_PreservesNonStrictOptionalFields(t *testing.T) {
	body := []byte(`{"text":{"format":{"type":"json_schema","strict":false,"schema":{"properties":{"tags":{"items":{"type":"string"},"uniqueItems":true}},"minProperties":1,"maxProperties":4}}}}`)

	normalized, changed, err := openai.NormalizeOpenAIResponseFormatSchemasBody(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "object", gjson.GetBytes(normalized, "text.format.schema.type").String())
	require.Equal(t, "array", gjson.GetBytes(normalized, "text.format.schema.properties.tags.type").String())
	require.False(t, gjson.GetBytes(normalized, "text.format.schema.minProperties").Exists())
	require.False(t, gjson.GetBytes(normalized, "text.format.schema.properties.tags.uniqueItems").Exists())
	require.Equal(t, int64(4), gjson.GetBytes(normalized, "text.format.schema.maxProperties").Int())
	require.False(t, gjson.GetBytes(normalized, "text.format.schema.required").Exists())
}

func TestNormalizeOpenAIResponseFormatSchemasBody_DoesNotExpandStrictSchema(t *testing.T) {
	body := []byte(`{"response_format":{"type":"json_schema","json_schema":{"strict":true,"schema":{"properties":{"name":{"type":"string"}}}}}}`)

	normalized, changed, err := openai.NormalizeOpenAIResponseFormatSchemasBody(body)
	require.NoError(t, err)
	require.True(t, changed) // Safe type inference still applies.
	require.Equal(t, "object", gjson.GetBytes(normalized, "response_format.json_schema.schema.type").String())
	require.False(t, gjson.GetBytes(normalized, "response_format.json_schema.schema.required").Exists())
	require.False(t, gjson.GetBytes(normalized, "response_format.json_schema.schema.additionalProperties").Exists())
}

func TestNormalizeOpenAIResponseFormatSchemasBody_TraversesNestedSchemaContainers(t *testing.T) {
	body := []byte(`{
		"text":{"format":{"type":"json_schema","schema":{
			"$defs":{"entry":{"properties":{"name":{"type":"string"}},"minProperties":1,"maxProperties":3}},
			"additionalProperties":{"items":{"type":"string"},"uniqueItems":true},
			"prefixItems":[{"properties":{"id":{"type":"string"}},"minProperties":1}],
			"dependentSchemas":{"kind":{"properties":{"value":{"type":"string"}},"uniqueItems":true}},
			"not":{"items":{"type":"string"},"minProperties":1}
		}}}
	}`)

	normalized, changed, err := openai.NormalizeOpenAIResponseFormatSchemasBody(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "object", gjson.GetBytes(normalized, "text.format.schema.$defs.entry.type").String())
	require.False(t, gjson.GetBytes(normalized, "text.format.schema.$defs.entry.minProperties").Exists())
	require.Equal(t, int64(3), gjson.GetBytes(normalized, "text.format.schema.$defs.entry.maxProperties").Int())
	require.Equal(t, "array", gjson.GetBytes(normalized, "text.format.schema.additionalProperties.type").String())
	require.False(t, gjson.GetBytes(normalized, "text.format.schema.additionalProperties.uniqueItems").Exists())
	require.Equal(t, "object", gjson.GetBytes(normalized, "text.format.schema.prefixItems.0.type").String())
	require.False(t, gjson.GetBytes(normalized, "text.format.schema.prefixItems.0.minProperties").Exists())
	require.Equal(t, "object", gjson.GetBytes(normalized, "text.format.schema.dependentSchemas.kind.type").String())
	require.False(t, gjson.GetBytes(normalized, "text.format.schema.dependentSchemas.kind.uniqueItems").Exists())
	require.Equal(t, "array", gjson.GetBytes(normalized, "text.format.schema.not.type").String())
	require.False(t, gjson.GetBytes(normalized, "text.format.schema.not.minProperties").Exists())
	require.False(t, gjson.GetBytes(normalized, "text.format.schema.required").Exists())
}

func TestNormalizeOpenAIResponseFormatSchemasBody_PreservesExistingTypeValues(t *testing.T) {
	body := []byte(`{"text":{"format":{"type":"json_schema","schema":{"properties":{"union":{"type":["object","null"],"properties":{"name":{"type":"string"}}},"custom":{"type":{"vendor":"shape"},"properties":{"id":{"type":"string"}}},"inferred":{"type":null,"items":{"type":"string"}}}}}}}`)

	normalized, changed, err := openai.NormalizeOpenAIResponseFormatSchemasBody(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "object", gjson.GetBytes(normalized, "text.format.schema.type").String())
	require.Equal(t, "object", gjson.GetBytes(normalized, "text.format.schema.properties.union.type.0").String())
	require.Equal(t, "null", gjson.GetBytes(normalized, "text.format.schema.properties.union.type.1").String())
	require.True(t, gjson.GetBytes(normalized, "text.format.schema.properties.custom.type").IsObject())
	require.Equal(t, "array", gjson.GetBytes(normalized, "text.format.schema.properties.inferred.type").String())
}

func TestNormalizeOpenAIPassthroughOAuthBody_CompactRemovesUnsupportedUser(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello","user":"user_123","metadata":{"user_id":"user_123"},"stream":true,"store":true}`)

	normalized, changed, err := openai.NormalizeOpenAIPassthroughOAuthBody(body, true)
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(normalized, "user").Exists())
	require.False(t, gjson.GetBytes(normalized, "metadata").Exists())
	require.False(t, gjson.GetBytes(normalized, "stream").Exists())
	require.False(t, gjson.GetBytes(normalized, "store").Exists())
	require.True(t, gjson.GetBytes(normalized, "input").IsArray())
}

func TestNormalizeOpenAIPassthroughOAuthBody_StringInputWrappedAsArray(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello world"}`)

	normalized, changed, err := openai.NormalizeOpenAIPassthroughOAuthBody(body, false)
	require.NoError(t, err)
	require.True(t, changed)

	input := gjson.GetBytes(normalized, "input")
	require.True(t, input.IsArray(), "string input should be converted to array")
	items := input.Array()
	require.Len(t, items, 1)
	require.Equal(t, "message", items[0].Get("type").String())
	require.Equal(t, "user", items[0].Get("role").String())
	require.Equal(t, "hello world", items[0].Get("content").String())
}

func TestNormalizeOpenAIPassthroughOAuthBody_EmptyStringInputWrappedAsEmptyArray(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"  "}`)

	normalized, changed, err := openai.NormalizeOpenAIPassthroughOAuthBody(body, false)
	require.NoError(t, err)
	require.True(t, changed)

	input := gjson.GetBytes(normalized, "input")
	require.True(t, input.IsArray())
	require.Len(t, input.Array(), 0, "whitespace-only input should become empty array")
}

func TestNormalizeOpenAIPassthroughOAuthBody_ObjectInputWrappedAsArray(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":{"type":"message","role":"user","content":"hi"}}`)

	normalized, changed, err := openai.NormalizeOpenAIPassthroughOAuthBody(body, false)
	require.NoError(t, err)
	require.True(t, changed)

	input := gjson.GetBytes(normalized, "input")
	require.True(t, input.IsArray(), "object input should be wrapped in array")
	items := input.Array()
	require.Len(t, items, 1)
	require.Equal(t, "message", items[0].Get("type").String())
}

func TestNormalizeOpenAIPassthroughOAuthBody_ArrayInputUnchanged(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"hi"}]}`)

	normalized, _, err := openai.NormalizeOpenAIPassthroughOAuthBody(body, false)
	require.NoError(t, err)

	input := gjson.GetBytes(normalized, "input")
	require.True(t, input.IsArray())
	require.Len(t, input.Array(), 1)
	require.Equal(t, "message", input.Array()[0].Get("type").String())
}

func TestDetectOpenAIPassthroughInstructionsRejectReason(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{name: "missing is optional", body: `{"model":"gpt-5.1-codex"}`, want: ""},
		{name: "non string remains rejected", body: `{"instructions":{"text":"invalid"}}`, want: "instructions_not_string"},
		{name: "empty remains rejected", body: `{"instructions":"  "}`, want: "instructions_empty"},
		{name: "non empty remains accepted", body: `{"instructions":"client guidance"}`, want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, openai.DetectOpenAIPassthroughInstructionsRejectReason("gpt-5.1-codex", []byte(tt.body)))
		})
	}
}

func TestOpenAIResponsesInputTextIsNeverSilentlyTruncated(t *testing.T) {
	atLimit := strings.Repeat("z", openAIResponsesInputTextMaxChars)
	oversized := strings.Repeat("a", openAIResponsesInputTextMaxChars) + "中"
	input := []any{
		map[string]any{"type": "function_call_output", "call_id": "limit", "output": atLimit},
		map[string]any{"type": "function_call_output", "call_id": "a", "output": oversized},
		map[string]any{"type": "tool_search_output", "call_id": "b", "output": oversized},
		map[string]any{"type": "custom_tool_call_output", "call_id": "c", "output": oversized},
		map[string]any{"type": "mcp_tool_call_output", "call_id": "d", "output": oversized},
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "short"},
				map[string]any{"type": "input_text", "text": oversized},
			},
		},
	}
	reqBody := map[string]any{"input": input}

	encoded, err := wirejson.Marshal(reqBody)
	require.NoError(t, err)
	preserved, changed, err := NormalizeOpenAIResponsesWebSocketCompatibilityBody(encoded, &providercore.Record{Platform: providercore.PlatformOpenAI, Type: providercore.ProviderTypeAPIKey}, false)
	require.NoError(t, err)
	require.False(t, changed)
	var decoded map[string]any
	require.NoError(t, wirejson.DecodeUseNumber(preserved, &decoded))
	var inputOK bool
	input, inputOK = decoded["input"].([]any)
	require.True(t, inputOK)
	first, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, atLimit, first["output"])
	for _, rawItem := range input[1:5] {
		item, ok := rawItem.(map[string]any)
		require.True(t, ok)
		require.Equal(t, oversized, item["output"])
	}
	last, ok := input[5].(map[string]any)
	require.True(t, ok)
	content, ok := last["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 2)
	shortPart, ok := content[0].(map[string]any)
	require.True(t, ok)
	oversizedPart, ok := content[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "short", shortPart["text"])
	require.Equal(t, oversized, oversizedPart["text"])
}

func TestOpenAIResponsesInputNeverRequestsPreemptiveTruncation(t *testing.T) {
	short := []byte(`{"input":[{"type":"function_call_output","output":"ok"}]}`)
	largeUnrelated := []byte(`{"input":"` + strings.Repeat("x", openAIResponsesInputTextMaxChars+1) + `"}`)
	largeOutput := []byte(`{"input":[{"type":"function_call_output","output":"` + strings.Repeat("x", openAIResponsesInputTextMaxChars+1) + `"}]}`)

	{
		preserved, changed, err := NormalizeOpenAIResponsesWebSocketCompatibilityBody(short, &providercore.Record{Platform: providercore.PlatformOpenAI, Type: providercore.ProviderTypeAPIKey}, false)
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, short, preserved)
	}
	{
		preserved, changed, err := NormalizeOpenAIResponsesWebSocketCompatibilityBody(largeUnrelated, &providercore.Record{Platform: providercore.PlatformOpenAI, Type: providercore.ProviderTypeAPIKey}, false)
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, largeUnrelated, preserved)
	}
	{
		preserved, changed, err := NormalizeOpenAIResponsesWebSocketCompatibilityBody(largeOutput, &providercore.Record{Platform: providercore.PlatformOpenAI, Type: providercore.ProviderTypeAPIKey}, false)
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, largeOutput, preserved)
	}
}

func TestNormalizeOpenAIResponsesLitePayloadForProvider_APIKeyOnlyDisablesParallelToolCalls(t *testing.T) {
	provider := &providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}
	body := []byte(`{
		"model":"gpt-5.6-terra",
		"parallel_tool_calls":true,
		"reasoning":{"context":"current_turn"},
		"tools":[{"type":"web_search"}],
		"input":[{"type":"message","nonce":9007199254740993}]
	}`)

	updated, changed, err := NormalizeResponsesLiteForProvider(provider, body)

	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(updated, "parallel_tool_calls").Bool())
	require.Equal(t, "current_turn", gjson.GetBytes(updated, "reasoning.context").String())
	require.Equal(t, "web_search", gjson.GetBytes(updated, "tools.0.type").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(updated, "input.0.nonce").Raw)
}

func TestNormalizeOpenAIResponsesLitePayloadForProvider_IgnoresNonOpenAIProvider(t *testing.T) {
	provider := &providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey}
	body := []byte(`{"parallel_tool_calls":true}`)

	updated, changed, err := NormalizeResponsesLiteForProvider(provider, body)

	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, updated)
}

func TestNormalizeOpenAIResponsesLitePayloadForProvider_RejectsNullAPIKeyBody(t *testing.T) {
	provider := &providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}
	body := []byte(`null`)

	updated, changed, err := NormalizeResponsesLiteForProvider(provider, body)

	require.ErrorContains(t, err, "request body must be a JSON object")
	require.False(t, changed)
	require.Equal(t, body, updated)
}
