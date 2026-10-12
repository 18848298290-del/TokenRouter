package qoder

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

func TestQoderHighestContextFlowsThroughAllProtocols(t *testing.T) {
	tests := []struct {
		name  string
		body  []byte
		parse func([]byte) (QoderPayloadRequest, error)
	}{
		{
			name:  "Chat Completions",
			body:  []byte(`{"model":"qwen3.8-max","messages":[{"role":"user","content":"hello"}]}`),
			parse: ParseQoderChatCompletionsPayload,
		},
		{
			name:  "Responses",
			body:  []byte(`{"model":"qwen3.8-max","input":"hello"}`),
			parse: ParseQoderResponsesPayload,
		},
		{
			name:  "Anthropic Messages",
			body:  []byte(`{"model":"qwen3.8-max","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`),
			parse: ParseQoderAnthropicMessagesPayload,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, err := tt.parse(tt.body)
			require.NoError(t, err)
			request.Site = SiteGlobal
			payload, modelKey := BuildQoderPayloadWithOptions(request, "", request.Messages, true, true)

			require.Equal(t, "qmodel_38max", modelKey)
			assertQoderContextCapabilityForTest(t, payload, 1000000, true)
		})
	}
}

func TestQoderContextCapabilityUsesSiteAndRouteFallback(t *testing.T) {
	tests := []struct {
		name        string
		site        Site
		model       string
		wantTokens  int
		wantRuntime bool
	}{
		{name: "国际站固定 Auto", site: SiteGlobal, model: "auto", wantTokens: 180000},
		{name: "国际站 Kimi K2.7", site: SiteGlobal, model: "kimi-k2.7-code", wantTokens: 256000, wantRuntime: true},
		{name: "国际站 MiniMax M3", site: SiteGlobal, model: "minimax-m3", wantTokens: 1000000, wantRuntime: true},
		{name: "国内站 Qwen3.6", site: SiteCN, model: "qwen3.6-flash", wantTokens: 1000000, wantRuntime: true},
		{name: "国内站 MiniMax M2.7", site: SiteCN, model: "minimax-m2.7", wantTokens: 200000, wantRuntime: true},
		{name: "未知 route", site: SiteGlobal, model: "custom-route", wantTokens: FallbackMaxInputTokens},
		{name: "隐藏 route", site: SiteGlobal, model: "cmodel", wantTokens: FallbackMaxInputTokens},
		{name: "已移除 route", site: SiteCN, model: "qmodel_preview", wantTokens: FallbackMaxInputTokens},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, _, err := BuildQoderPayloadFromChatCompletionsForSite([]byte(`{"model":"`+tt.model+`","messages":[{"role":"user","content":"hello"}]}`), "personal_standard", tt.site)
			require.NoError(t, err)
			assertQoderContextCapabilityForTest(t, payload, tt.wantTokens, tt.wantRuntime)
		})
	}
}

func TestQoderContextCapabilityMergesWithThinkingOverride(t *testing.T) {
	payload, _, err := BuildQoderPayloadFromChatCompletionsForSite([]byte(`{
		"model":"deepseek-v4-pro",
		"reasoning_effort":"high",
		"messages":[{"role":"user","content":"hello"}]
	}`), "personal_standard", SiteGlobal)
	require.NoError(t, err)
	assertQoderContextCapabilityForTest(t, payload, 1000000, true)

	parameters := requireQoderPayloadMapForTest(t, payload["parameters"], "parameters")
	require.Equal(t, "max", parameters["reasoning_effort"])
	chatContext := requireQoderPayloadMapForTest(t, payload["chat_context"], "chat_context")
	extra := requireQoderPayloadMapForTest(t, chatContext["extra"], "chat_context.extra")
	runtimeOverride := requireQoderPayloadMapForTest(t, extra["ideModelConfigOverride"], "chat_context.extra.ideModelConfigOverride")
	require.Equal(t, "max", runtimeOverride["reasoning_effort"])
	require.Equal(t, 1000000, runtimeOverride["max_input_tokens"])
}

// assertQoderContextCapabilityForTest checks the input limit and both optional runtime overrides.
func assertQoderContextCapabilityForTest(t *testing.T, payload map[string]any, wantTokens int, wantRuntime bool) {
	t.Helper()
	modelConfig := requireQoderPayloadMapForTest(t, payload["model_config"], "model_config")
	require.EqualValues(t, wantTokens, modelConfig["max_input_tokens"])

	parameters := requireQoderPayloadMapForTest(t, payload["parameters"], "parameters")
	contextLength, hasContextLength := parameters["context_length"]
	chatContext := requireQoderPayloadMapForTest(t, payload["chat_context"], "chat_context")
	extra := requireQoderPayloadMapForTest(t, chatContext["extra"], "chat_context.extra")
	runtimeOverride, hasRuntimeOverride := extra["ideModelConfigOverride"].(map[string]any)
	if wantRuntime {
		require.True(t, hasContextLength)
		require.EqualValues(t, wantTokens, contextLength)
		require.True(t, hasRuntimeOverride)
		require.EqualValues(t, wantTokens, runtimeOverride["max_input_tokens"])
		return
	}

	require.False(t, hasContextLength)
	require.False(t, hasRuntimeOverride)
}

// requireQoderPayloadMapForTest returns a JSON object or fails with its payload path.
func requireQoderPayloadMapForTest(t *testing.T, value any, path string) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	require.True(t, ok, "%s 应为 JSON 对象", path)
	return result
}

func TestBuildQoderPayloadFromChatCompletions(t *testing.T) {
	body := []byte(`{
		"model":"auto",
		"max_tokens":123,
		"messages":[
			{"role":"system","content":"be terse"},
			{"role":"user","content":[{"type":"text","text":"hello"}]},
			{"role":"assistant","content":"hi"},
			{"role":"tool","tool_call_id":"call_1","content":"tool output"}
		],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]
	}`)

	payload, modelKey, err := BuildQoderPayloadFromChatCompletionsForSite(body, "personal_standard", SiteGlobal)
	require.NoError(t, err)
	require.Equal(t, "auto", modelKey)
	require.Equal(t, true, payload["stream"])
	require.Equal(t, "personal_standard", payload["aliyun_user_type"])
	parameters, _ := payload["parameters"].(map[string]any)
	modelConfig, _ := payload["model_config"].(map[string]any)
	chatContext, _ := payload["chat_context"].(map[string]any)
	chatText, _ := chatContext["text"].(map[string]any)
	require.Equal(t, 123, parameters["max_tokens"])
	require.Equal(t, "auto", modelConfig["key"])
	require.Equal(t, 180000, modelConfig["max_input_tokens"])
	require.Equal(t, "hello", chatText["text"])
	business, ok := payload["business"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "1.24.2", business["version"])

	messagesRaw, ok := payload["messages"].([]any)
	require.True(t, ok)
	messages := messagesRaw
	require.Len(t, messages, 4)
	firstMsg, ok := messages[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "system", firstMsg["role"])
	require.Equal(t, "be terse", firstMsg["content"])
	secondMsg, ok := messages[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "user", secondMsg["role"])
	require.Equal(t, "", secondMsg["content"])
	userContents, ok := secondMsg["contents"].([]any)
	require.True(t, ok)
	firstContent, ok := userContents[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "hello", firstContent["text"])
	lastMsg, ok := messages[3].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "tool", lastMsg["role"])
	require.Equal(t, "tool output", lastMsg["content"])
	tools, ok := payload["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
}

func TestBuildQoderPayloadUsesCNModelAndClientVersion(t *testing.T) {
	body := []byte(`{
		"model":"qwen3.6-flash",
		"messages":[{"role":"user","content":"hello"}]
	}`)

	payload, modelKey, err := BuildQoderPayloadFromChatCompletionsForSite(body, "personal_standard", SiteCN)

	require.NoError(t, err)
	require.Equal(t, "q36fmodel", modelKey)
	business, ok := payload["business"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "1.24.2", business["version"])
}

func TestBuildQoderPayloadUserSystemReplacesBuiltInSystem(t *testing.T) {
	body := []byte(`{
		"model":"auto",
		"messages":[
			{"role":"system","content":"custom system"},
			{"role":"user","content":"hello"}
		]
	}`)

	payload, _, err := BuildQoderPayloadFromChatCompletionsForSite(body, "personal_standard", SiteGlobal)
	require.NoError(t, err)

	messages, ok := payload["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 2)
	systemMessages := 0
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		require.True(t, ok)
		if msg["role"] == "system" {
			systemMessages++
			require.Equal(t, "custom system", msg["content"])
		}
	}
	require.Equal(t, 1, systemMessages)
}

func TestBuildQoderPayloadFromChatCompletionsPreservesToolHistory(t *testing.T) {
	body := []byte(`{
		"model":"auto",
		"messages":[
			{"role":"user","content":"run ls"},
			{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":{"cmd":"ls"}}}]},
			{"role":"tool","tool_call_id":"call_1","name":"bash","content":"file.txt"}
		],
		"tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}]
	}`)

	payload, _, err := BuildQoderPayloadFromChatCompletionsForSite(body, "personal_standard", SiteGlobal)
	require.NoError(t, err)

	messages := qoderFixtureValue[[]any](t, payload["messages"])
	assistant := qoderFixtureValue[map[string]any](t, messages[1])
	toolCalls := qoderFixtureValue[[]any](t, assistant["tool_calls"])
	require.Len(t, toolCalls, 1)
	toolCall := qoderFixtureValue[map[string]any](t, toolCalls[0])
	require.Equal(t, "call_1", toolCall["id"])
	require.Equal(t, "function", toolCall["type"])
	function := qoderFixtureValue[map[string]any](t, toolCall["function"])
	require.Equal(t, "bash", function["name"])
	require.JSONEq(t, `{"cmd":"ls"}`, qoderFixtureValue[string](t, function["arguments"]))

	tool := qoderFixtureValue[map[string]any](t, messages[2])
	require.Equal(t, "tool", tool["role"])
	require.Equal(t, "call_1", tool["tool_call_id"])
	require.Equal(t, "call_1", tool["tool_call_call_id"])
	require.Equal(t, "bash", tool["name"])
}

func TestBuildQoderPayloadFromChatCompletionsPreservesLegacyFunctionHistory(t *testing.T) {
	body := []byte(`{
		"model":"auto",
		"messages":[
			{"role":"user","content":"weather"},
			{"role":"assistant","content":null,"function_call":{"name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"}},
			{"role":"function","name":"get_weather","content":"sunny"}
		],
		"functions":[{"name":"get_weather","parameters":{"type":"object"}}]
	}`)

	payload, _, err := BuildQoderPayloadFromChatCompletionsForSite(body, "personal_standard", SiteGlobal)
	require.NoError(t, err)

	messages := qoderFixtureValue[[]any](t, payload["messages"])
	assistant := qoderFixtureValue[map[string]any](t, messages[1])
	toolCalls := qoderFixtureValue[[]any](t, assistant["tool_calls"])
	require.Len(t, toolCalls, 1)
	toolCall := qoderFixtureValue[map[string]any](t, toolCalls[0])
	require.Equal(t, "get_weather", toolCall["id"])
	function := qoderFixtureValue[map[string]any](t, toolCall["function"])
	require.Equal(t, "get_weather", function["name"])
	require.JSONEq(t, `{"city":"Tokyo"}`, qoderFixtureValue[string](t, function["arguments"]))

	tool := qoderFixtureValue[map[string]any](t, messages[2])
	require.Equal(t, "tool", tool["role"])
	require.Equal(t, "get_weather", tool["tool_call_id"])
	require.Equal(t, "get_weather", tool["tool_call_call_id"])
	require.Equal(t, "get_weather", tool["name"])
}

func TestBuildQoderPayloadFromChatCompletionsMergesParallelToolHistory(t *testing.T) {
	body := []byte(`{
		"model":"deepseek-v4-pro",
		"messages":[
			{"role":"user","content":"run both commands"},
			{"role":"assistant","reasoning_content":"Need two shell checks.","content":"","tool_calls":[
				{"id":"call_a","type":"function","function":{"name":"bash","arguments":"{\"command\":\"printf a\\n\"}"}},
				{"id":"call_b","type":"function","function":{"name":"bash","arguments":"{\"command\":\"printf b\\n\"}"}}
			]},
			{"role":"tool","tool_call_id":"call_a","content":"a\n"},
			{"role":"tool","tool_call_id":"call_b","content":"b\n"}
		],
		"tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}}]
	}`)

	payload, _, err := BuildQoderPayloadFromChatCompletionsForSite(body, "personal_standard", SiteGlobal)
	require.NoError(t, err)

	messages := qoderFixtureValue[[]any](t, payload["messages"])
	require.Len(t, messages, 4)
	assistant := qoderFixtureValue[map[string]any](t, messages[1])
	require.Equal(t, "assistant", assistant["role"])
	require.Empty(t, assistant["contents"], "DeepSeek reasoning_content must not be replayed as visible assistant text before tool_calls")
	toolCalls := qoderFixtureValue[[]any](t, assistant["tool_calls"])
	require.Len(t, toolCalls, 2)
	require.Equal(t, "call_a", qoderFixtureValue[map[string]any](t, toolCalls[0])["id"])
	require.Equal(t, "call_b", qoderFixtureValue[map[string]any](t, toolCalls[1])["id"])

	firstTool := qoderFixtureValue[map[string]any](t, messages[2])
	require.Equal(t, "tool", firstTool["role"])
	require.Equal(t, "call_a", firstTool["tool_call_id"])
	require.Equal(t, "call_a", firstTool["tool_call_call_id"])
	require.Equal(t, "bash", firstTool["name"])
	secondTool := qoderFixtureValue[map[string]any](t, messages[3])
	require.Equal(t, "tool", secondTool["role"])
	require.Equal(t, "call_b", secondTool["tool_call_id"])
	require.Equal(t, "call_b", secondTool["tool_call_call_id"])
	require.Equal(t, "bash", secondTool["name"])
}

func TestBuildQoderPayloadAddsCacheControlToLastEligibleTextBlock(t *testing.T) {
	body := []byte(`{
		"model":"auto",
		"messages":[
			{"role":"user","content":[{"type":"text","text":"first","cache_control":{"type":"ephemeral"}},{"type":"tool_use","id":"ignored","name":"bash","input":{}},{"type":"thinking","thinking":"ignore"}]},
			{"role":"assistant","content":[{"type":"redacted_thinking","data":"ignore"},{"type":"tool_use","id":"call_1","name":"bash","input":{"cmd":"pwd"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"repo"},{"type":"text","text":"last"}]}
		]
	}`)

	payload, _, err := BuildQoderPayloadFromAnthropicMessages(body, "personal_standard")
	require.NoError(t, err)
	messages := qoderFixtureValue[[]any](t, payload["messages"])
	firstContents := qoderFixtureValue[[]any](t, qoderFixtureValue[map[string]any](t, messages[0])["contents"])
	require.Equal(t, "ephemeral", qoderFixtureValue[map[string]any](t, qoderFixtureValue[map[string]any](t, firstContents[0])["cache_control"])["type"])
	lastContents := qoderFixtureValue[[]any](t, qoderFixtureValue[map[string]any](t, messages[len(messages)-1])["contents"])
	lastText := qoderFixtureValue[map[string]any](t, lastContents[len(lastContents)-1])
	require.Equal(t, "last", lastText["text"])
	require.Equal(t, "ephemeral", qoderFixtureValue[map[string]any](t, lastText["cache_control"])["type"])
	for _, rawMessage := range messages {
		for _, rawBlock := range qoderFixtureValue[[]any](t, qoderFixtureValue[map[string]any](t, rawMessage)["contents"]) {
			block := qoderFixtureValue[map[string]any](t, rawBlock)
			if block["type"] != "text" {
				require.NotContains(t, block, "cache_control")
			}
		}
	}
}

func TestBuildQoderPayloadFromAnthropicMessages(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-4-6",
		"max_tokens":456,
		"system":[{"type":"text","text":"system one"},{"type":"text","text":"system two"}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hello"},{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"tool result"}]}]}
		]
	}`)

	payload, modelKey, err := BuildQoderPayloadFromAnthropicMessages(body, "personal_standard")
	require.NoError(t, err)
	require.Equal(t, "ultimate", modelKey)
	require.Equal(t, 456, qoderFixtureValue[map[string]any](t, payload["parameters"])["max_tokens"])
	require.Equal(t, "ultimate", qoderFixtureValue[map[string]any](t, payload["model_config"])["key"])

	messages := qoderFixtureValue[[]any](t, payload["messages"])
	require.Len(t, messages, 3)
	require.Equal(t, "system", qoderFixtureValue[map[string]any](t, messages[0])["role"])
	require.Equal(t, "system one\nsystem two", qoderFixtureValue[map[string]any](t, messages[0])["content"])
	require.Equal(t, "", qoderFixtureValue[map[string]any](t, messages[1])["content"])
	userContents := qoderFixtureValue[[]any](t, qoderFixtureValue[map[string]any](t, messages[1])["contents"])
	require.Equal(t, "hello", qoderFixtureValue[map[string]any](t, userContents[0])["text"])
	toolResult := qoderFixtureValue[map[string]any](t, messages[2])
	require.Equal(t, "tool", toolResult["role"])
	require.Equal(t, "t1", toolResult["tool_call_id"])
	require.Equal(t, "tool result", toolResult["content"])
}

func TestBuildQoderPayloadFromAnthropicMessagesPreservesToolUseHistory(t *testing.T) {
	body := []byte(`{
		"model":"auto",
		"max_tokens":456,
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"bash","input":{"cmd":"ls"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"file.txt"}]}
		],
		"tools":[{"name":"bash","input_schema":{"type":"object"}}]
	}`)

	payload, _, err := BuildQoderPayloadFromAnthropicMessages(body, "personal_standard")
	require.NoError(t, err)

	messages := qoderFixtureValue[[]any](t, payload["messages"])
	assistant := qoderFixtureValue[map[string]any](t, messages[0])
	toolCalls := qoderFixtureValue[[]any](t, assistant["tool_calls"])
	require.Len(t, toolCalls, 1)
	toolCall := qoderFixtureValue[map[string]any](t, toolCalls[0])
	require.Equal(t, "call_1", toolCall["id"])
	function := qoderFixtureValue[map[string]any](t, toolCall["function"])
	require.Equal(t, "bash", function["name"])
	require.JSONEq(t, `{"cmd":"ls"}`, qoderFixtureValue[string](t, function["arguments"]))

	tool := qoderFixtureValue[map[string]any](t, messages[1])
	require.Equal(t, "tool", tool["role"])
	require.Equal(t, "call_1", tool["tool_call_id"])
	require.Equal(t, "call_1", tool["tool_call_call_id"])
	require.Equal(t, "bash", tool["name"])
	require.Equal(t, "file.txt", tool["content"])
}

func TestBuildQoderPayloadFromAnthropicMessagesIgnoresThinkingToolUseHistory(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-4-6",
		"messages":[
			{"role":"user","content":"inspect files"},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"need to inspect","signature":"sig"},
				{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"README.md"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":"contents"}
			]}
		],
		"tools":[{"name":"Read","input_schema":{"type":"object"}}]
	}`)

	payload, _, err := BuildQoderPayloadFromAnthropicMessages(body, "personal_standard")
	require.NoError(t, err)

	messages := qoderFixtureValue[[]any](t, payload["messages"])
	require.Len(t, messages, 3)
	assistant := qoderFixtureValue[map[string]any](t, messages[1])
	require.Equal(t, "assistant", assistant["role"])
	require.Equal(t, "", assistant["content"])
	toolCalls := qoderFixtureValue[[]any](t, assistant["tool_calls"])
	require.Len(t, toolCalls, 1)
	toolCall := qoderFixtureValue[map[string]any](t, toolCalls[0])
	require.Equal(t, "toolu_1", toolCall["id"])
	function := qoderFixtureValue[map[string]any](t, toolCall["function"])
	require.Equal(t, "Read", function["name"])
	require.JSONEq(t, `{"file_path":"README.md"}`, qoderFixtureValue[string](t, function["arguments"]))
	toolResult := qoderFixtureValue[map[string]any](t, messages[2])
	require.Equal(t, "tool", toolResult["role"])
	require.Equal(t, "toolu_1", toolResult["tool_call_id"])
	require.Equal(t, "toolu_1", toolResult["tool_call_call_id"])
	require.Equal(t, "Read", toolResult["name"])
	require.Equal(t, "contents", toolResult["content"])
}

func TestBuildQoderPayloadFromAnthropicMessagesDoesNotInventMissingToolResultID(t *testing.T) {
	body := []byte(`{
		"model":"auto",
		"messages":[
			{"role":"user","content":[{"type":"tool_result","content":"file.txt"}]}
		]
	}`)

	payload, _, err := BuildQoderPayloadFromAnthropicMessages(body, "personal_standard")
	require.NoError(t, err)

	messages := qoderFixtureValue[[]any](t, payload["messages"])
	require.Len(t, messages, 1)
	tool := qoderFixtureValue[map[string]any](t, messages[0])
	require.Equal(t, "user", tool["role"])
	require.NotContains(t, tool, "tool_calls")
	require.NotContains(t, tool, "tool_call_id")
}

func TestBuildQoderPayloadFromAnthropicMessagesConvertsTools(t *testing.T) {
	body := []byte(`{
		"model":"auto",
		"max_tokens":456,
		"messages":[{"role":"user","content":"hello"}],
		"tools":[{
			"name":"bash",
			"description":"run command",
			"input_schema":{
				"type":"object",
				"properties":{"cmd":{"type":"string"}},
				"required":["cmd"]
			}
		}]
	}`)

	payload, _, err := BuildQoderPayloadFromAnthropicMessages(body, "personal_standard")
	require.NoError(t, err)

	tools := qoderFixtureValue[[]any](t, payload["tools"])
	require.Len(t, tools, 1)
	tool := qoderFixtureValue[map[string]any](t, tools[0])
	require.Equal(t, "function", tool["type"])
	require.NotContains(t, tool, "input_schema")
	function := qoderFixtureValue[map[string]any](t, tool["function"])
	require.Equal(t, "bash", function["name"])
	require.Equal(t, "run command", function["description"])
	parameters := qoderFixtureValue[map[string]any](t, function["parameters"])
	require.Equal(t, "object", parameters["type"])
	require.Contains(t, parameters, "properties")
}

func TestQoderResponsesPayloadPreservesControlRoleInputAsSystemPrompt(t *testing.T) {
	request, err := ParseQoderResponsesPayload([]byte(`{
		"model":"deepseek-v4-pro",
		"instructions":"top-level instructions",
		"input":[
			{"type":"message","role":"system","content":[{"type":"input_text","text":"system item"}]},
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"developer item"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
		]
	}`))

	require.NoError(t, err)
	require.Contains(t, request.System, "top-level instructions")
	require.Contains(t, request.System, "system item")
	require.Contains(t, request.System, "developer item")
	require.Len(t, request.Messages, 1)
	require.Equal(t, "user", request.Messages[0].Role)
	require.Equal(t, "hello", request.Messages[0].Text)
}

func TestQoderResponsesPayloadSkipsReasoningAndUnknownOutputItems(t *testing.T) {
	request, err := ParseQoderResponsesPayload([]byte(`{
		"model":"deepseek-v4-pro",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"latest sha?"}]},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"need to run curl"}]},
			{"type":"function_call","call_id":"call_a","name":"exec_command","arguments":"{\"cmd\":\"curl x\"}"},
			{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"x"}},
			{"type":"function_call_output","call_id":"call_a","output":"deadbeef"}
		]
	}`))

	require.NoError(t, err)
	require.Len(t, request.Messages, 3)
	require.Equal(t, "user", request.Messages[0].Role)
	require.Equal(t, "latest sha?", request.Messages[0].Text)
	require.Equal(t, "assistant", request.Messages[1].Role)
	require.Len(t, QoderAnySlice(request.Messages[1].Raw["tool_calls"]), 1)
	require.Equal(t, "tool", request.Messages[2].Role)
	require.Equal(t, "call_a", request.Messages[2].ToolCallID)
	require.Equal(t, "deadbeef", request.Messages[2].Text)
}

func TestQoderResponsesPayloadDropsUnansweredParallelFunctionCall(t *testing.T) {
	request, err := ParseQoderResponsesPayload([]byte(`{
		"model":"deepseek-v4-pro",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"run commands"}]},
			{"type":"function_call","call_id":"call_a","name":"bash","arguments":"{\"command\":\"printf A\"}"},
			{"type":"function_call","call_id":"call_b","name":"bash","arguments":"{\"command\":\"printf B\"}"},
			{"type":"function_call_output","call_id":"call_a","output":"A\n"}
		]
	}`))

	require.NoError(t, err)
	require.Len(t, request.Messages, 3)
	toolCalls := QoderAnySlice(request.Messages[1].Raw["tool_calls"])
	require.Len(t, toolCalls, 1)
	toolCall := qoderFixtureValue[map[string]any](t, toolCalls[0])
	require.Equal(t, "call_a", toolCall["id"])
	require.Equal(t, "tool", request.Messages[2].Role)
	require.Equal(t, "call_a", request.Messages[2].ToolCallID)
}

func TestQoderResponsesPayloadDropsOrphanFunctionCallOutput(t *testing.T) {
	request, err := ParseQoderResponsesPayload([]byte(`{
		"model":"deepseek-v4-pro",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{"type":"function_call_output","call_id":"ghost","output":"stale output"}
		]
	}`))

	require.NoError(t, err)
	require.Len(t, request.Messages, 1)
	require.Equal(t, "user", request.Messages[0].Role)
	require.Equal(t, "hello", request.Messages[0].Text)
}

// TestQoderChatSystemTextPreservesDeveloperMessages checks developer text joins the system prompt.
func TestQoderChatSystemTextPreservesDeveloperMessages(t *testing.T) {
	messages := []protocolopenai.ChatMessage{
		{Role: "system", Content: []byte(`"You are helpful"`)},
		{Role: "developer", Content: []byte(`"Be concise"`)},
		{Role: "user", Content: []byte(`"Hello"`)},
	}

	systemText := QoderChatSystemText(messages)

	require.Contains(t, systemText, "You are helpful")
	require.Contains(t, systemText, "Be concise")
	require.Equal(t, "You are helpful\nBe concise", systemText)
}

// TestQoderChatSystemTextOnlySystemMessages checks system text without developer messages.
func TestQoderChatSystemTextOnlySystemMessages(t *testing.T) {
	messages := []protocolopenai.ChatMessage{
		{Role: "system", Content: []byte(`"You are helpful"`)},
		{Role: "user", Content: []byte(`"Hello"`)},
	}

	systemText := QoderChatSystemText(messages)

	require.Equal(t, "You are helpful", systemText)
}

// TestQoderChatSystemTextOnlyDeveloperMessages checks developer text without system messages.
func TestQoderChatSystemTextOnlyDeveloperMessages(t *testing.T) {
	messages := []protocolopenai.ChatMessage{
		{Role: "developer", Content: []byte(`"Be concise"`)},
		{Role: "user", Content: []byte(`"Hello"`)},
	}

	systemText := QoderChatSystemText(messages)

	require.Equal(t, "Be concise", systemText)
}

// TestQoderChatSystemTextEmptyWhenNoSystemOrDeveloper excludes ordinary conversation turns.
func TestQoderChatSystemTextEmptyWhenNoSystemOrDeveloper(t *testing.T) {
	messages := []protocolopenai.ChatMessage{
		{Role: "user", Content: []byte(`"Hello"`)},
		{Role: "assistant", Content: []byte(`"Hi"`)},
	}

	systemText := QoderChatSystemText(messages)

	require.Equal(t, "", systemText)
}

// TestQoderChatSystemTextMultipleDeveloperMessages checks that all developer messages contribute.
func TestQoderChatSystemTextMultipleDeveloperMessages(t *testing.T) {
	messages := []protocolopenai.ChatMessage{
		{Role: "system", Content: []byte(`"You are helpful"`)},
		{Role: "developer", Content: []byte(`"Be concise"`)},
		{Role: "developer", Content: []byte(`"Use examples"`)},
		{Role: "user", Content: []byte(`"Hello"`)},
	}

	systemText := QoderChatSystemText(messages)

	require.Contains(t, systemText, "You are helpful")
	require.Contains(t, systemText, "Be concise")
	require.Contains(t, systemText, "Use examples")
}

// TestQoderChatCompletionsRespectsMaxCompletionTokens checks precedence over max_tokens.
func TestQoderChatCompletionsRespectsMaxCompletionTokens(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4-6",
		"messages": [{"role": "user", "content": "hi"}],
		"max_completion_tokens": 1000,
		"max_tokens": 2000
	}`)

	req, err := ParseQoderChatCompletionsPayload(body)

	require.NoError(t, err)
	require.Equal(t, 1000, req.MaxTokens, "max_completion_tokens should take precedence")
}

// TestQoderChatCompletionsFallsBackToMaxTokens checks requests without max_completion_tokens.
func TestQoderChatCompletionsFallsBackToMaxTokens(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4-6",
		"messages": [{"role": "user", "content": "hi"}],
		"max_tokens": 2000
	}`)

	req, err := ParseQoderChatCompletionsPayload(body)

	require.NoError(t, err)
	require.Equal(t, 2000, req.MaxTokens, "should use max_tokens when max_completion_tokens absent")
}

// TestQoderChatCompletionsUsesDefaultWhenBothAbsent checks the default output limit.
func TestQoderChatCompletionsUsesDefaultWhenBothAbsent(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4-6",
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	req, err := ParseQoderChatCompletionsPayload(body)

	require.NoError(t, err)
	require.Equal(t, QoderDefaultMaxTokens, req.MaxTokens, "should use default when both absent")
}

// TestQoderChatCompletionsIgnoresZeroMaxTokens checks a positive max_completion_tokens with max_tokens=0.
func TestQoderChatCompletionsIgnoresZeroMaxTokens(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4-6",
		"messages": [{"role": "user", "content": "hi"}],
		"max_completion_tokens": 1000,
		"max_tokens": 0
	}`)

	req, err := ParseQoderChatCompletionsPayload(body)

	require.NoError(t, err)
	require.Equal(t, 1000, req.MaxTokens, "should ignore max_tokens when it's 0")
}

// assertQoderThinkingPayload checks every thinking switch and effort copy consumed by Qoder.
func assertQoderThinkingPayload(t *testing.T, payload map[string]any, enabled bool, effort string, hasEffort bool) {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(raw, "model_config.is_reasoning").Exists())
	require.Equal(t, enabled, gjson.GetBytes(raw, "model_config.is_reasoning").Bool())
	require.True(t, gjson.GetBytes(raw, "chat_context.extra.modelConfig.is_reasoning").Exists())
	require.Equal(t, enabled, gjson.GetBytes(raw, "chat_context.extra.modelConfig.is_reasoning").Bool())

	paths := []string{
		"parameters.reasoning_effort",
		"model_config.reasoning_effort",
		"chat_context.extra.modelConfig.reasoning_effort",
		"chat_context.extra.ideModelConfigOverride.reasoning_effort",
	}
	for _, path := range paths {
		if hasEffort {
			require.Equal(t, effort, gjson.GetBytes(raw, path).String(), path)
			continue
		}
		require.False(t, gjson.GetBytes(raw, path).Exists(), path)
	}
}

func TestQoderThinkingDirectiveFromBody(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		effortPaths []string
		want        QoderThinkingDirective
	}{
		{name: "missing stays disabled", body: `{}`, effortPaths: []string{"reasoning.effort"}},
		{name: "minimal normalizes low", body: `{"reasoning":{"effort":"minimal"}}`, effortPaths: []string{"reasoning.effort"}, want: QoderThinkingDirective{Enabled: true, Effort: "low"}},
		{name: "low stays low", body: `{"reasoning_effort":"LOW"}`, effortPaths: []string{"reasoning_effort"}, want: QoderThinkingDirective{Enabled: true, Effort: "low"}},
		{name: "medium stays medium", body: `{"reasoning":{"effort":"medium"}}`, effortPaths: []string{"reasoning.effort"}, want: QoderThinkingDirective{Enabled: true, Effort: "medium"}},
		{name: "high stays high", body: `{"reasoning":{"effort":"high"}}`, effortPaths: []string{"reasoning.effort"}, want: QoderThinkingDirective{Enabled: true, Effort: "high"}},
		{name: "very high aliases map max", body: `{"reasoning":{"effort":"very_high"}}`, effortPaths: []string{"reasoning.effort"}, want: QoderThinkingDirective{Enabled: true, Effort: "max"}},
		{name: "positive budget maps max", body: `{"thinking":{"budget_tokens":1}}`, effortPaths: []string{"reasoning.effort"}, want: QoderThinkingDirective{Enabled: true, Effort: "max"}},
		{name: "zero budget stays disabled", body: `{"thinking":{"budget_tokens":0}}`, effortPaths: []string{"reasoning.effort"}},
		{name: "enabled without budget maps max", body: `{"thinking":{"type":"enabled"}}`, effortPaths: []string{"reasoning.effort"}, want: QoderThinkingDirective{Enabled: true, Effort: "max"}},
		{name: "adaptive without budget maps max", body: `{"thinking":{"type":"adaptive"}}`, effortPaths: []string{"reasoning.effort"}, want: QoderThinkingDirective{Enabled: true, Effort: "max"}},
		{name: "explicit effort beats budget", body: `{"reasoning":{"effort":"low"},"thinking":{"budget_tokens":32768}}`, effortPaths: []string{"reasoning.effort"}, want: QoderThinkingDirective{Enabled: true, Effort: "low"}},
		{name: "disabled beats effort and budget", body: `{"reasoning":{"effort":"max"},"thinking":{"type":"disabled","budget_tokens":32768}}`, effortPaths: []string{"reasoning.effort"}},
		{name: "none effort beats enabled", body: `{"reasoning":{"effort":"none"},"thinking":{"type":"enabled","budget_tokens":32768}}`, effortPaths: []string{"reasoning.effort"}},
		{name: "invalid effort falls back to budget", body: `{"reasoning":{"effort":"banana"},"thinking":{"budget_tokens":8}}`, effortPaths: []string{"reasoning.effort"}, want: QoderThinkingDirective{Enabled: true, Effort: "max"}},
		{name: "invalid effort alone stays disabled", body: `{"reasoning":{"effort":"banana"}}`, effortPaths: []string{"reasoning.effort"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, QoderThinkingDirectiveFromBody([]byte(tt.body), tt.effortPaths...))
		})
	}
}

func TestQoderThinkingParsersUseProtocolNativeFields(t *testing.T) {
	chat, err := ParseQoderChatCompletionsPayload([]byte(`{
		"model":"deepseek-v4-pro",
		"reasoning_effort":"medium",
		"messages":[{"role":"user","content":"hello"}]
	}`))
	require.NoError(t, err)
	require.Equal(t, QoderThinkingDirective{Enabled: true, Effort: "medium"}, chat.Thinking)

	responses, err := ParseQoderResponsesPayload([]byte(`{
		"model":"deepseek-v4-pro",
		"reasoning":{"effort":"xhigh"},
		"input":"hello"
	}`))
	require.NoError(t, err)
	require.Equal(t, QoderThinkingDirective{Enabled: true, Effort: "max"}, responses.Thinking)

	// Anthropic effort takes precedence over a simultaneously supplied budget.
	messages, err := ParseQoderAnthropicMessagesPayload([]byte(`{
		"model":"deepseek-v4-pro",
		"max_tokens":1024,
		"output_config":{"effort":"low"},
		"thinking":{"type":"enabled","budget_tokens":32768},
		"messages":[{"role":"user","content":"hello"}]
	}`))
	require.NoError(t, err)
	require.Equal(t, QoderThinkingDirective{Enabled: true, Effort: "low"}, messages.Thinking)

	// Qoder tolerates unknown efforts; the shared Anthropic conversion must not reject them first.
	messages, err = ParseQoderAnthropicMessagesPayload([]byte(`{
		"model":"deepseek-v4-pro",
		"max_tokens":1024,
		"output_config":{"effort":"ultra"},
		"messages":[{"role":"user","content":"hello"}]
	}`))
	require.NoError(t, err)
	require.Equal(t, QoderThinkingDirective{}, messages.Thinking)
}

func TestBuildQoderAnthropicThinkingPayloadDefaultsToGlobalSite(t *testing.T) {
	// The constructor without a site applies global thinking capabilities.
	body := []byte(`{
		"model":"deepseek-v4-pro",
		"max_tokens":1024,
		"thinking":{"type":"enabled","budget_tokens":1},
		"messages":[{"role":"user","content":"hello"}]
	}`)

	payload, modelKey, err := BuildQoderPayloadFromAnthropicMessages(body, "personal_standard")
	require.NoError(t, err)
	require.Equal(t, "dmodel", modelKey)
	assertQoderThinkingPayload(t, payload, true, "max", true)
}

func TestBuildQoderThinkingPayloadBySiteAndModelCapability(t *testing.T) {
	tests := []struct {
		name           string
		site           Site
		model          string
		extra          map[string]any
		wantEnabled    bool
		wantEffort     string
		wantEffortPath bool
	}{
		{name: "global qwen 38 public alias on", site: SiteGlobal, model: "qwen3.8-max", extra: map[string]any{"reasoning_effort": "low"}, wantEnabled: true},
		{name: "global qwen 38 raw route on", site: SiteGlobal, model: "qmodel_38max", extra: map[string]any{"reasoning_effort": "max"}, wantEnabled: true},
		{name: "global qwen 38 missing control off", site: SiteGlobal, model: "qwen3.8-max", wantEffort: "none", wantEffortPath: true},
		{name: "global qwen 38 explicit disabled", site: SiteGlobal, model: "qwen3.8-max", extra: map[string]any{"thinking": map[string]any{"type": "disabled", "budget_tokens": 32768}}, wantEffort: "none", wantEffortPath: true},
		{name: "global qwen 38 invalid effort off", site: SiteGlobal, model: "qwen3.8-max", extra: map[string]any{"reasoning_effort": "banana"}, wantEffort: "none", wantEffortPath: true},
		{name: "global qwen 38 invalid effort with budget on", site: SiteGlobal, model: "qwen3.8-max", extra: map[string]any{"reasoning_effort": "banana", "thinking": map[string]any{"budget_tokens": 8}}, wantEnabled: true},
		{name: "global qwen 37 max default off", site: SiteGlobal, model: "qwen3.7-max", wantEffort: "none", wantEffortPath: true},
		{name: "global qwen 37 plus toggle on", site: SiteGlobal, model: "qwen3.7-plus", extra: map[string]any{"reasoning_effort": "max"}, wantEnabled: true},
		{name: "global deepseek pro low to high", site: SiteGlobal, model: "deepseek-v4-pro", extra: map[string]any{"reasoning_effort": "low"}, wantEnabled: true, wantEffort: "high", wantEffortPath: true},
		{name: "global deepseek flash budget to max", site: SiteGlobal, model: "deepseek-v4-flash", extra: map[string]any{"thinking": map[string]any{"budget_tokens": 1}}, wantEnabled: true, wantEffort: "max", wantEffortPath: true},
		{name: "global glm high to max", site: SiteGlobal, model: "glm-5.2", extra: map[string]any{"reasoning_effort": "high"}, wantEnabled: true, wantEffort: "max", wantEffortPath: true},
		{name: "global glm 53 low stays low", site: SiteGlobal, model: "glm-5.3", extra: map[string]any{"reasoning_effort": "low"}, wantEnabled: true, wantEffort: "low", wantEffortPath: true},
		{name: "global glm 53 high stays high", site: SiteGlobal, model: "glm-5.3", extra: map[string]any{"reasoning_effort": "high"}, wantEnabled: true, wantEffort: "high", wantEffortPath: true},
		{name: "cn qwen 38 public alias on", site: SiteCN, model: "qwen3.8-max", extra: map[string]any{"reasoning_effort": "low"}, wantEnabled: true},
		{name: "cn qwen 38 raw route on", site: SiteCN, model: "qmodel_38max", extra: map[string]any{"reasoning_effort": "max"}, wantEnabled: true},
		{name: "cn qwen 37 max default off", site: SiteCN, model: "qwen3.7-max", wantEffort: "none", wantEffortPath: true},
		{name: "cn qwen 37 plus toggle on", site: SiteCN, model: "qwen3.7-plus", extra: map[string]any{"reasoning_effort": "max"}, wantEnabled: true},
		{name: "cn deepseek pro low to high", site: SiteCN, model: "deepseek-v4-pro", extra: map[string]any{"reasoning_effort": "low"}, wantEnabled: true, wantEffort: "high", wantEffortPath: true},
		{name: "cn deepseek flash budget to max", site: SiteCN, model: "deepseek-v4-flash", extra: map[string]any{"thinking": map[string]any{"budget_tokens": 1}}, wantEnabled: true, wantEffort: "max", wantEffortPath: true},
		{name: "cn glm high to max", site: SiteCN, model: "glm-5.2", extra: map[string]any{"reasoning_effort": "high"}, wantEnabled: true, wantEffort: "max", wantEffortPath: true},
		{name: "cn glm 53 medium maps high", site: SiteCN, model: "glm-5.3", extra: map[string]any{"reasoning_effort": "medium"}, wantEnabled: true, wantEffort: "high", wantEffortPath: true},
		{name: "cn glm 53 max stays max", site: SiteCN, model: "gmodel", extra: map[string]any{"reasoning_effort": "max"}, wantEnabled: true, wantEffort: "max", wantEffortPath: true},
		{name: "cn deepseek explicit disabled", site: SiteCN, model: "deepseek-v4-pro", extra: map[string]any{"thinking": map[string]any{"type": "disabled", "budget_tokens": 32768}}, wantEffort: "none", wantEffortPath: true},
		{name: "cn auto ignored", site: SiteCN, model: "auto", extra: map[string]any{"reasoning_effort": "max"}},
		{name: "cn qwen 36 ignored", site: SiteCN, model: "qwen3.6-flash", extra: map[string]any{"reasoning_effort": "max"}},
		{name: "cn kimi ignored", site: SiteCN, model: "kimi-k2.7-code", extra: map[string]any{"reasoning_effort": "max"}},
		{name: "cn minimax ignored", site: SiteCN, model: "minimax-m2.7", extra: map[string]any{"reasoning_effort": "max"}},
		{name: "cn unknown route ignored", site: SiteCN, model: "custom-model", extra: map[string]any{"reasoning_effort": "max"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := map[string]any{
				"model": tt.model,
				"messages": []any{
					map[string]any{"role": "user", "content": "hello"},
				},
			}
			maps.Copy(body, tt.extra)
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			payload, _, err := BuildQoderPayloadFromChatCompletionsForSite(raw, "personal_standard", tt.site)
			require.NoError(t, err)
			assertQoderThinkingPayload(t, payload, tt.wantEnabled, tt.wantEffort, tt.wantEffortPath)
		})
	}
}

func TestBuildQoderThinkingPayloadKeepsGlobalOnlyModelIsolated(t *testing.T) {
	body := []byte(`{
		"model":"kimi-k3",
		"reasoning_effort":"max",
		"messages":[{"role":"user","content":"hello"}]
	}`)

	payload, _, err := BuildQoderPayloadFromChatCompletionsForSite(body, "personal_standard", SiteGlobal)
	require.NoError(t, err)
	assertQoderThinkingPayload(t, payload, false, "", false)
}
