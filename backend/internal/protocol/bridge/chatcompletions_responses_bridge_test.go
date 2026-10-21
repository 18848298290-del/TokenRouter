package bridge

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// These image locations distinguish inline and remote fixture payloads when
	// assertions compare tool markers with the attributed image parts emitted in
	// user content.
	testToolImageDataURL = "data:image/png;base64,AQID"
	testToolImageRemote  = "https://example.com/tool-output.png"
)

// TestResponsesToChatCompletionsRequestReplaysUndeclaredNamespaces reproduces
// compaction input after tool declarations disappear. Completed history remains
// paired, while the outgoing callable list and returned restoration map exclude it.
func TestResponsesToChatCompletionsRequestReplaysUndeclaredNamespaces(t *testing.T) {
	for _, tools := range [][]ResponsesTool{
		nil, {}, {{Type: "tool_search"}}, {{Type: "function", Name: "current"}},
	} {
		request := &ResponsesRequest{
			Model: "deepseek-v4.1-flash", Stream: true,
			Tools: tools,
			Input: json.RawMessage(`[
				{"type":"function_call","call_id":"call_1","name":"js","namespace":"mcp__cua_proxy","arguments":"{\"code\":\"inspect\"}"},
				{"type":"function_call_output","call_id":"call_1","output":"ok"},
				{"type":"function_call","call_id":"call_2","name":"open_in_codex","namespace":"mcp__codex_app","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_2","output":"opened"}
			]`),
		}
		converted, mapping, err := ResponsesToChatCompletionsRequestWithMapping(request, nil)
		require.NoError(t, err)
		require.Len(t, converted.Messages, 4)
		require.Equal(t, "mcp__cua_proxy__js", converted.Messages[0].ToolCalls[0].Function.Name)
		require.JSONEq(t, `{"code":"inspect"}`, converted.Messages[0].ToolCalls[0].Function.Arguments)
		require.Equal(t, "call_1", converted.Messages[1].ToolCallID)
		require.Equal(t, "mcp__codex_app__open_in_codex", converted.Messages[2].ToolCalls[0].Function.Name)
		require.Equal(t, "call_2", converted.Messages[3].ToolCallID)
		require.Len(t, converted.Tools, len(tools))
		require.Empty(t, mapping.NamespaceTools)
		require.NotContains(t, mapping.CallNames, NamespacedToolName{Namespace: "mcp__cua_proxy", Name: "js"})
	}
}

// TestResponsesToChatCompletionsRequestRetainsUndeclaredToolMedia replays an
// undeclared screenshot call and a second unanswered call. The image stays
// attributed to its answered call, and existing pairing drops the unanswered one.
func TestResponsesToChatCompletionsRequestRetainsUndeclaredToolMedia(t *testing.T) {
	request := &ResponsesRequest{
		Model: "deepseek-test",
		Input: json.RawMessage(`[{"type":"function_call","namespace":"browser","name":"screenshot","call_id":"image_call","arguments":"{}"},{"type":"function_call_output","call_id":"image_call","output":[{"type":"input_image","image_url":"` + testToolImageDataURL + `"}]},{"type":"function_call","namespace":"browser","name":"pending","call_id":"pending_call","arguments":"{}"}]`),
	}
	converted, _, err := ResponsesToChatCompletionsRequestWithMapping(request, nil)
	require.NoError(t, err)
	require.Len(t, converted.Messages, 3)
	require.Equal(t, "browser__screenshot", converted.Messages[0].ToolCalls[0].Function.Name)
	require.Equal(t, "image_call", converted.Messages[1].ToolCallID)
	require.Equal(t, "user", converted.Messages[2].Role)
	require.Contains(t, string(converted.Messages[2].Content), testToolImageDataURL)
	require.Contains(t, string(converted.Messages[2].Content), "image_call")
}

// TestResponsesChatBridgeSeparatesHistoricalCustomAliases combines an active
// custom tool with undeclared history under a neighboring namespace. Replayed
// names stay ordinary functions in buffered and streamed replies, while the
// current custom tool's valid namespace-prefixed alias still restores correctly.
func TestResponsesChatBridgeSeparatesHistoricalCustomAliases(t *testing.T) {
	request := &ResponsesRequest{
		Model: "deepseek-test",
		Tools: []ResponsesTool{
			{Type: "custom", Name: "exec"},
			{Type: "namespace", Name: "browser", Tools: []ResponsesTool{{Type: "function", Name: "inspect"}}},
		},
		Input: json.RawMessage(`[{"type":"function_call","namespace":"browser","name":"exec","call_id":"old","arguments":"{}"},{"type":"function_call_output","call_id":"old","output":"old result"}]`),
	}
	converted, mapping, err := ResponsesToChatCompletionsRequestWithMapping(request, nil)
	require.NoError(t, err)
	historicalName := converted.Messages[0].ToolCalls[0].Function.Name
	require.NotEqual(t, "browser__exec", historicalName)
	response := &ChatCompletionsResponse{Choices: []ChatChoice{{Message: ChatMessage{ToolCalls: []ChatToolCall{
		{ID: "historical", Function: ChatFunctionCall{Name: historicalName, Arguments: "{}"}},
		{ID: "current", Function: ChatFunctionCall{Name: "browser__exec", Arguments: `{"input":"pwd"}`}},
	}}}}}
	output := ChatCompletionsResponseToResponses(testRuntime(), response, request.Model, mapping.CustomTools, mapping.FunctionTools, mapping.ToolSearch, mapping.NamespaceTools)
	require.Len(t, output.Output, 2)
	require.Equal(t, "function_call", output.Output[0].Type)
	require.Equal(t, historicalName, output.Output[0].Name)
	require.Equal(t, "custom_tool_call", output.Output[1].Type)
	require.Equal(t, "exec", output.Output[1].Name)
	require.Equal(t, "pwd", output.Output[1].Input)

	state := NewChatCompletionsToResponsesStreamState(testRuntime(), request.Model)
	state.CustomTools = mapping.CustomTools
	state.FunctionTools = mapping.FunctionTools
	state.NamespaceTools = mapping.NamespaceTools
	calls := response.Choices[0].Message.ToolCalls
	indices := []int{0, 1}
	for i := range calls {
		calls[i].Index = &indices[i]
	}
	events := ChatCompletionsChunkToResponsesEvents(testRuntime(), &ChatCompletionsChunk{
		Choices: []ChatChunkChoice{{Delta: ChatDelta{ToolCalls: calls}}},
	}, state)
	events = append(events, FinalizeChatCompletionsResponsesStream(testRuntime(), state)...)
	historicalEvents := 0
	for _, event := range events {
		if event.Item != nil && event.Item.CallID == "historical" {
			require.Equal(t, "function_call", event.Item.Type)
			require.Equal(t, historicalName, event.Item.Name)
			historicalEvents++
		}
		if event.Response != nil && event.Type == "response.completed" {
			require.Len(t, event.Response.Output, 2)
			require.Equal(t, "function_call", event.Response.Output[0].Type)
			require.Equal(t, "custom_tool_call", event.Response.Output[1].Type)
		}
	}
	require.Equal(t, 2, historicalEvents)
}

// TestResponsesToChatCompletionsRequestIsolatesHistoricalChoice creates an
// active flat name matching an undeclared namespace join. History receives a
// distinct name, and a forced undeclared selector cannot target the active tool.
func TestResponsesToChatCompletionsRequestIsolatesHistoricalChoice(t *testing.T) {
	request := &ResponsesRequest{
		Model: "deepseek-test",
		Tools: []ResponsesTool{{Type: "function", Name: "browser__js"}},
		Input: json.RawMessage(`[{"type":"function_call","namespace":"browser","name":"js","call_id":"c1","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]`),
	}
	converted, mapping, err := ResponsesToChatCompletionsRequestWithMapping(request, nil)
	require.NoError(t, err)
	require.NotEqual(t, "browser__js", converted.Messages[0].ToolCalls[0].Function.Name)
	require.Equal(t, "browser__js", converted.Tools[0].Function.Name)
	require.Empty(t, mapping.NamespaceTools)
	for _, choice := range []string{
		`{"type":"function","namespace":"browser","name":"js"}`,
		`{"type":"namespace","name":"browser"}`,
	} {
		request.ToolChoice = json.RawMessage(choice)
		_, _, err := ResponsesToChatCompletionsRequestWithMapping(request, nil)
		require.Error(t, err)
	}
}

// TestResponsesInputToChatMessages_DeveloperRoleMapsToSystem supplies scalar
// developer content. Conversion must return one system message with JSON-
// equivalent text, checking the target role and retained instruction value.
func TestResponsesInputToChatMessages_DeveloperRoleMapsToSystem(t *testing.T) {
	messages, err := responsesInputToChatMessagesWithOptions("", json.RawMessage(`[{"role":"developer","content":"follow project instructions"}]`), nil)
	require.NoError(t, err)
	require.Len(t, messages, 1)

	assert.Equal(t, "system", messages[0].Role)
	assert.JSONEq(t, `"follow project instructions"`, string(messages[0].Content))
}

// TestResponsesInputToChatMessages_SkipsInvalidHistoricalFunctionCall mixes
// malformed and valid call/result pairs before user continuation. The three
// returned turns must retain the valid pair's correlation and exclude the
// malformed pair.
func TestResponsesInputToChatMessages_SkipsInvalidHistoricalFunctionCall(t *testing.T) {
	input := json.RawMessage(`[
		{"type":"function_call","call_id":"call_bad","name":"exec_command","arguments":"{\"cmd\": \"ssh root@HOST"},
		{"type":"function_call_output","call_id":"call_bad","output":"failed to parse function arguments"},
		{"type":"function_call","call_id":"call_ok","name":"exec_command","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_ok","output":"ok"},
		{"role":"user","content":"continue"}
	]`)

	messages, err := responsesInputToChatMessagesWithOptions("", input, nil)
	require.NoError(t, err)
	require.Len(t, messages, 3)
	require.Equal(t, "assistant", messages[0].Role)
	require.Len(t, messages[0].ToolCalls, 1)
	require.Equal(t, "call_ok", messages[0].ToolCalls[0].ID)
	require.Equal(t, "tool", messages[1].Role)
	require.Equal(t, "call_ok", messages[1].ToolCallID)
	require.Equal(t, "user", messages[2].Role)
}

// TestResponsesInputToChatMessages_SkipsInvalidEmptyCallIDOutput supplies
// malformed ordinary arguments and a subsequent empty-ID result. Only the user
// continuation remains, checking the uncorrelated output-count skip path.
func TestResponsesInputToChatMessages_SkipsInvalidEmptyCallIDOutput(t *testing.T) {
	input := json.RawMessage(`[
		{"type":"function_call","call_id":"","name":"exec_command","arguments":"{\"cmd\": \"ssh root@HOST"},
		{"type":"function_call_output","call_id":"","output":"failed to parse function arguments"},
		{"role":"user","content":"continue"}
	]`)

	messages, err := responsesInputToChatMessagesWithOptions("", input, nil)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Equal(t, "user", messages[0].Role)
}

// TestChatCompletionsResponseToResponses_SkipsInvalidFunctionArguments
// supplies malformed and valid calls with length finish. The response remains
// incomplete while retaining one valid function output with its asserted ID
// and arguments.
func TestChatCompletionsResponseToResponses_SkipsInvalidFunctionArguments(t *testing.T) {
	resp := &ChatCompletionsResponse{
		Model: "deepseek-v4-flash",
		Choices: []ChatChoice{{
			Message: ChatMessage{
				Role: "assistant",
				ToolCalls: []ChatToolCall{
					{ID: "call_bad", Type: "function", Function: ChatFunctionCall{Name: "exec_command", Arguments: `{"cmd": "ssh root@HOST`}},
					{ID: "call_ok", Type: "function", Function: ChatFunctionCall{Name: "exec_command", Arguments: `{}`}},
				},
			},
			FinishReason: "length",
		}},
	}

	out := ChatCompletionsResponseToResponses(testRuntime(), resp, "deepseek-v4-flash", nil, nil, false, nil)
	require.Equal(t, "incomplete", out.Status)
	require.Len(t, out.Output, 1)
	require.Equal(t, "function_call", out.Output[0].Type)
	require.Equal(t, "call_ok", out.Output[0].CallID)
	require.Equal(t, `{}`, out.Output[0].Arguments)
}

// TestResponsesInputToChatMessages_KeepsChatCompletionRoles supplies four
// scalar messages using existing Chat roles. The returned count and role
// sequence must retain system, user, assistant, and ID-less tool ordering.
func TestResponsesInputToChatMessages_KeepsChatCompletionRoles(t *testing.T) {
	input := json.RawMessage(`[
		{"role":"system","content":"system message"},
		{"role":"user","content":"user message"},
		{"role":"assistant","content":"assistant message"},
		{"role":"tool","content":"tool message"}
	]`)

	messages, err := responsesInputToChatMessagesWithOptions("", input, nil)
	require.NoError(t, err)
	require.Len(t, messages, 4)

	assert.Equal(t, []string{"system", "user", "assistant", "tool"}, chatMessageRoles(messages))
}

// TestResponsesInputToChatMessages_EmptyRoleFallsBackToUser supplies one
// scalar message with an empty role. Conversion must return one message using
// user, checking the missing-role target selection.
func TestResponsesInputToChatMessages_EmptyRoleFallsBackToUser(t *testing.T) {
	messages, err := responsesInputToChatMessagesWithOptions("", json.RawMessage(`[{"role":"","content":"hello"}]`), nil)
	require.NoError(t, err)
	require.Len(t, messages, 1)

	assert.Equal(t, "user", messages[0].Role)
}

// TestResponsesInputToChatMessages_DeveloperRoleTrimAndCaseInsensitive
// supplies spaced and uppercase developer spellings. Both returned messages
// must use system, checking normalization of those two role fixtures.
func TestResponsesInputToChatMessages_DeveloperRoleTrimAndCaseInsensitive(t *testing.T) {
	input := json.RawMessage(`[
		{"role":" Developer ","content":"one"},
		{"role":"\tDEVELOPER\n","content":"two"}
	]`)

	messages, err := responsesInputToChatMessagesWithOptions("", input, nil)
	require.NoError(t, err)
	require.Len(t, messages, 2)

	assert.Equal(t, []string{"system", "system"}, chatMessageRoles(messages))
}

// TestResponsesToChatCompletionsRequest_InstructionsAndInputDeveloperRole
// combines top-level instructions, developer text parts, and user content.
// Assertions check three ordered roles and each converted scalar content
// value.
func TestResponsesToChatCompletionsRequest_InstructionsAndInputDeveloperRole(t *testing.T) {
	req := &ResponsesRequest{
		Model:        "gpt-4o",
		Instructions: "Use concise answers.",
		Input: json.RawMessage(`[
			{"role":"developer","content":[{"type":"input_text","text":"Prefer JSON."}]},
			{"role":"user","content":"Hello"}
		]`),
	}

	out, err := ResponsesToChatCompletionsRequestWithOptions(req, nil)
	require.NoError(t, err)
	require.Len(t, out.Messages, 3)

	assert.Equal(t, []string{"system", "system", "user"}, chatMessageRoles(out.Messages))
	assert.JSONEq(t, `"Use concise answers."`, string(out.Messages[0].Content))
	assert.JSONEq(t, `"Prefer JSON."`, string(out.Messages[1].Content))
	assert.JSONEq(t, `"Hello"`, string(out.Messages[2].Content))
}

// TestResponsesToChatCompletionsRequest_TextFormatJsonObject supplies a
// json_object text-format request with ordinary user input. JSON equality
// checks the selected response_format shape after successful conversion of
// that fixture.
func TestResponsesToChatCompletionsRequest_TextFormatJsonObject(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(`[
			{"role":"user","content":"Return JSON"}
		]`),
		Text: &ResponsesText{
			Format: json.RawMessage(`{"type":"json_object"}`),
		},
	}

	out, err := ResponsesToChatCompletionsRequestWithOptions(req, nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"json_object"}`, string(out.ResponseFormat))
}

// TestResponsesToChatCompletionsRequest_TextFormatJsonSchema supplies named
// strict schema format with properties and required members. JSON equality
// checks its nested Chat schema shape and the retained supplied constraints.
func TestResponsesToChatCompletionsRequest_TextFormatJsonSchema(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(`[
			{"role":"user","content":"Return structured JSON"}
		]`),
		Text: &ResponsesText{
			Format: json.RawMessage(`{
				"type":"json_schema",
				"name":"answer",
				"schema":{
					"type":"object",
					"properties":{"ok":{"type":"boolean"}},
					"required":["ok"],
					"additionalProperties":false
				},
				"strict":true
			}`),
		},
	}

	out, err := ResponsesToChatCompletionsRequestWithOptions(req, nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"type":"json_schema",
		"json_schema":{
			"name":"answer",
			"schema":{
				"type":"object",
				"properties":{"ok":{"type":"boolean"}},
				"required":["ok"],
				"additionalProperties":false
			},
			"strict":true
		}
	}`, string(out.ResponseFormat))
}

// TestResponsesToChatCompletionsRequest_ParallelToolCalls supplies an explicit
// false pointer. The returned pointer must stay nonnil/false, and encoded
// request JSON must contain parallel_tool_calls false.
func TestResponsesToChatCompletionsRequest_ParallelToolCalls(t *testing.T) {
	parallel := false
	req := &ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(`[
			{"role":"user","content":"Use tools"}
		]`),
		ParallelToolCalls: &parallel,
	}

	out, err := ResponsesToChatCompletionsRequestWithOptions(req, nil)
	require.NoError(t, err)
	require.NotNil(t, out.ParallelToolCalls)
	assert.False(t, *out.ParallelToolCalls)

	payload, err := json.Marshal(out)
	require.NoError(t, err)
	assert.Contains(t, string(payload), `"parallel_tool_calls":false`)
}

// TestResponsesToChatCompletionsRequest_CustomToolBecomesFunctionTool mixes
// custom and function declarations. Assertions check the lowered custom
// kind/name/description/wrapper schema and the retained ordinary function name
// in declaration order.
func TestResponsesToChatCompletionsRequest_CustomToolBecomesFunctionTool(t *testing.T) {
	req := &ResponsesRequest{
		Model: "glm-5.2",
		Input: json.RawMessage(`"run dir"`),
		Tools: []ResponsesTool{
			{Type: "custom", Name: "exec", Description: "Run JavaScript code"},
			{Type: "function", Name: "wait", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)},
		},
	}

	out, err := ResponsesToChatCompletionsRequestWithOptions(req, nil)
	require.NoError(t, err)
	require.Len(t, out.Tools, 2)

	assert.Equal(t, "function", out.Tools[0].Type)
	assert.Equal(t, "exec", out.Tools[0].Function.Name)
	assert.Equal(t, "Run JavaScript code", out.Tools[0].Function.Description)
	assert.JSONEq(t, customToolInputSchema, string(out.Tools[0].Function.Parameters))

	assert.Equal(t, "wait", out.Tools[1].Function.Name)
}

// TestResponsesChatBridge_MixedCustomAndNamespaceToolNames lowers one custom
// plus a namespace child, then converts direct and inferred-alias calls back.
// It checks declarations and restored kinds, names, namespace, and custom
// input values.
func TestResponsesChatBridge_MixedCustomAndNamespaceToolNames(t *testing.T) {
	req := &ResponsesRequest{
		Model: "deepseek-test",
		Input: json.RawMessage(`"run pwd"`),
		Tools: []ResponsesTool{
			{Type: "custom", Name: "exec", Description: "Runs a command"},
			{Type: "namespace", Name: "functions", Tools: []ResponsesTool{
				{Type: "function", Name: "wait", Parameters: json.RawMessage(`{"type":"object"}`)},
			}},
		},
	}

	chatReq, mapping, err := ResponsesToChatCompletionsRequestWithMapping(req, nil)
	require.NoError(t, err)
	require.Len(t, chatReq.Tools, 2)
	assert.Equal(t, "exec", chatReq.Tools[0].Function.Name)
	assert.Equal(t, "wait", chatReq.Tools[1].Function.Name)

	customTools := CustomToolNames(req.Tools)
	namespaceTools := mapping.NamespaceTools
	resp := &ChatCompletionsResponse{Choices: []ChatChoice{{Message: ChatMessage{ToolCalls: []ChatToolCall{
		{ID: "call_exact", Function: ChatFunctionCall{Name: "exec", Arguments: `{"input":"pwd"}`}},
		{ID: "call_wait", Function: ChatFunctionCall{Name: "wait", Arguments: `{"cell_id":"1"}`}},
		{ID: "call_alias", Function: ChatFunctionCall{Name: "exec", Arguments: `not-json`}},
	}}}}}

	out := ChatCompletionsResponseToResponses(testRuntime(), resp, req.Model, customTools, FunctionToolNames(req.Tools), false, namespaceTools)
	require.Len(t, out.Output, 3)
	assert.Equal(t, "custom_tool_call", out.Output[0].Type)
	assert.Equal(t, "exec", out.Output[0].Name)
	assert.Equal(t, "pwd", out.Output[0].Input)
	assert.Equal(t, "function_call", out.Output[1].Type)
	assert.Empty(t, out.Output[1].Namespace)
	assert.Equal(t, "wait", out.Output[1].Name)
	assert.Equal(t, "custom_tool_call", out.Output[2].Type)
	assert.Equal(t, "exec", out.Output[2].Name)
	assert.Equal(t, "not-json", out.Output[2].Input)
}

// TestChatCompletionsResponseToResponses_ExplicitFunctionOwnsCustomAliasCollision
// supplies an explicit function label that resembles an inferred custom alias.
// The single output must stay function_call with its original name and
// argument text.
func TestChatCompletionsResponseToResponses_ExplicitFunctionOwnsCustomAliasCollision(t *testing.T) {
	resp := &ChatCompletionsResponse{Choices: []ChatChoice{{Message: ChatMessage{ToolCalls: []ChatToolCall{{
		ID: "call_function", Function: ChatFunctionCall{Name: "functions__exec", Arguments: `{"path":"/tmp"}`},
	}}}}}}

	out := ChatCompletionsResponseToResponses(testRuntime(), resp, "deepseek-test",
		map[string]bool{"exec": true}, map[string]bool{"functions__exec": true}, false,
		map[string]NamespacedToolName{"functions__wait": {Namespace: "functions", Name: "wait"}})

	require.Len(t, out.Output, 1)
	assert.Equal(t, "function_call", out.Output[0].Type)
	assert.Equal(t, "functions__exec", out.Output[0].Name)
	assert.Equal(t, `{"path":"/tmp"}`, out.Output[0].Arguments)
}

// TestResponsesToChatCompletionsRequest_AdditionalToolsItem carries three
// declarations inside additional_tools beside user content. It checks
// collected mappings, ordered lowered names, auto choice, and exclusion of
// that declaration item from messages.
func TestResponsesToChatCompletionsRequest_AdditionalToolsItem(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-test",
		Input: json.RawMessage(`[
			{"type":"additional_tools","role":"developer","tools":[
				{"type":"custom","name":"exec","description":"Run PowerShell","format":{"type":"text"}},
				{"type":"function","name":"wait","parameters":{"type":"object","properties":{}}},
				{"type":"namespace","name":"collaboration","tools":[
					{"type":"function","name":"send_message","parameters":{"type":"object","properties":{}}}
				]}
			]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"run Get-Location"}]}
		]`),
		ToolChoice: json.RawMessage(`"auto"`),
	}

	effective, err := EffectiveResponsesTools(req)
	require.NoError(t, err)
	require.Len(t, effective, 3)
	assert.True(t, CustomToolNames(effective)["exec"])
	assert.Equal(t, NamespacedToolName{Namespace: "collaboration", Name: "send_message"}, NamespaceToolNames(effective)["collaboration__send_message"])

	out, err := ResponsesToChatCompletionsRequestWithOptions(req, nil)
	require.NoError(t, err)
	require.Len(t, out.Tools, 3)
	assert.Equal(t, "exec", out.Tools[0].Function.Name)
	assert.Equal(t, "wait", out.Tools[1].Function.Name)
	assert.Equal(t, "collaboration__send_message", out.Tools[2].Function.Name)
	assert.JSONEq(t, `"auto"`, string(out.ToolChoice))

	require.Len(t, out.Messages, 1, "additional_tools must not become a chat message")
	assert.Equal(t, "user", out.Messages[0].Role)
}

// TestEffectiveResponsesTools_SkipsStringInputItems places scalar history
// before an additional-tools object. Effective collection must return only the
// inspected custom declaration, ignoring the scalar as declaration input.
func TestEffectiveResponsesTools_SkipsStringInputItems(t *testing.T) {
	req := &ResponsesRequest{
		Input: json.RawMessage(`["plain input",{"type":"additional_tools","tools":[{"type":"custom","name":"exec"}]}]`),
	}

	tools, err := EffectiveResponsesTools(req)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "exec", tools[0].Name)
}

// TestEffectiveResponsesTools_IgnoresMalformedToolsOnNonAdditionalItem
// supplies unrelated malformed tools on a message beside a valid additional
// item. Collection succeeds with the one inspected custom declaration from the
// latter.
func TestEffectiveResponsesTools_IgnoresMalformedToolsOnNonAdditionalItem(t *testing.T) {
	req := &ResponsesRequest{
		Input: json.RawMessage(`[
			{"type":"message","role":"user","tools":"not-an-array","content":[{"type":"input_text","text":"hello"}]},
			{"type":"additional_tools","tools":[{"type":"custom","name":"exec"}]}
		]`),
	}

	tools, err := EffectiveResponsesTools(req)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "exec", tools[0].Name)
}

// TestEffectiveResponsesTools_RejectsMalformedAdditionalTools supplies a
// nonarray tools value on exact additional_tools. It checks an error naming
// that decode phase and an empty returned list.
func TestEffectiveResponsesTools_RejectsMalformedAdditionalTools(t *testing.T) {
	req := &ResponsesRequest{
		Input: json.RawMessage(`[{"type":"additional_tools","tools":"not-an-array"}]`),
	}

	tools, err := EffectiveResponsesTools(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse responses additional tools item")
	assert.Empty(t, tools)
}

// TestResponsesToChatCompletionsRequest_DropsToolChoiceWhenNoConvertibleTools
// supplies unsupported hosted/image declarations with auto choice. Conversion
// must return empty tools and choice after no declaration survives this route.
func TestResponsesToChatCompletionsRequest_DropsToolChoiceWhenNoConvertibleTools(t *testing.T) {
	req := &ResponsesRequest{
		Model: "glm-5.2",
		Input: json.RawMessage(`"hi"`),
		Tools: []ResponsesTool{
			{Type: "web_search"},
			{Type: "image_generation"},
		},
		ToolChoice: json.RawMessage(`"auto"`),
	}

	out, err := ResponsesToChatCompletionsRequestWithOptions(req, nil)
	require.NoError(t, err)

	assert.Empty(t, out.Tools)
	assert.Empty(t, out.ToolChoice, "tools 为空时转发 tool_choice 会被上游 400 拒绝")
}

// TestResponsesToChatCompletionsRequest_CustomToolChoiceMapsToFunctionChoice
// selects a declared custom tool by name. JSON equality checks that the
// request uses the corresponding nested Chat function selector.
func TestResponsesToChatCompletionsRequest_CustomToolChoiceMapsToFunctionChoice(t *testing.T) {
	req := &ResponsesRequest{
		Model:      "glm-5.2",
		Input:      json.RawMessage(`"run dir"`),
		Tools:      []ResponsesTool{{Type: "custom", Name: "exec"}},
		ToolChoice: json.RawMessage(`{"type":"custom","name":"exec"}`),
	}

	out, err := ResponsesToChatCompletionsRequestWithOptions(req, nil)
	require.NoError(t, err)

	assert.JSONEq(t, `{"type":"function","function":{"name":"exec"}}`, string(out.ToolChoice))
}

// TestResponsesInputToChatMessages_CustomToolCallHistory replays user text
// plus custom call/output. It checks user/assistant/tool order, wrapper
// arguments, correlation/name, and the scalar result's encoded content.
func TestResponsesInputToChatMessages_CustomToolCallHistory(t *testing.T) {
	input := json.RawMessage(`[
		{"role":"user","content":"list files"},
		{"type":"custom_tool_call","call_id":"call_1","name":"exec","input":"dir"},
		{"type":"custom_tool_call_output","call_id":"call_1","output":"main.go"}
	]`)

	messages, err := responsesInputToChatMessagesWithOptions("", input, nil)
	require.NoError(t, err)
	require.Len(t, messages, 3)

	assert.Equal(t, []string{"user", "assistant", "tool"}, chatMessageRoles(messages))

	require.Len(t, messages[1].ToolCalls, 1)
	toolCall := messages[1].ToolCalls[0]
	assert.Equal(t, "call_1", toolCall.ID)
	assert.Equal(t, "exec", toolCall.Function.Name)
	assert.JSONEq(t, `{"input":"dir"}`, toolCall.Function.Arguments)

	assert.Equal(t, "call_1", messages[2].ToolCallID)
	assert.JSONEq(t, `"main.go"`, string(messages[2].Content))
}

// TestChatCompletionsResponseToResponses_CustomToolCallOutputItem returns
// custom-wrapper and ordinary function calls together. Assertions inspect
// custom kind/correlation/name/input with empty Arguments, then retained
// ordinary name and argument text.
func TestChatCompletionsResponseToResponses_CustomToolCallOutputItem(t *testing.T) {
	resp := &ChatCompletionsResponse{
		ID: "cc-1",
		Choices: []ChatChoice{{
			Message: ChatMessage{
				Role: "assistant",
				ToolCalls: []ChatToolCall{
					{ID: "call_1", Function: ChatFunctionCall{Name: "exec", Arguments: `{"input": "dir"}`}},
					{ID: "call_2", Function: ChatFunctionCall{Name: "wait", Arguments: `{"cell_id": 3}`}},
				},
			},
		}},
	}

	out := ChatCompletionsResponseToResponses(testRuntime(), resp, "glm-5.2", map[string]bool{"exec": true}, nil, false, nil)
	require.Len(t, out.Output, 2)

	assert.Equal(t, "custom_tool_call", out.Output[0].Type)
	assert.Equal(t, "call_1", out.Output[0].CallID)
	assert.Equal(t, "exec", out.Output[0].Name)
	assert.Equal(t, "dir", out.Output[0].Input)
	assert.Empty(t, out.Output[0].Arguments)

	assert.Equal(t, "function_call", out.Output[1].Type)
	assert.Equal(t, "wait", out.Output[1].Name)
	assert.Equal(t, `{"cell_id": 3}`, out.Output[1].Arguments)
}

// TestExtractCustomToolCallInput_FallsBackToRawArguments compares a valid
// wrapper, raw script, unrelated object, empty object, and empty text. Each
// result must match the inspected unwrapped or retained string.
func TestExtractCustomToolCallInput_FallsBackToRawArguments(t *testing.T) {
	assert.Equal(t, "dir", extractCustomToolCallInput(`{"input": "dir"}`))
	assert.Equal(t, "console.log(1)", extractCustomToolCallInput(`console.log(1)`))
	assert.Equal(t, `{"other": "x"}`, extractCustomToolCallInput(`{"other": "x"}`))
	assert.Equal(t, "", extractCustomToolCallInput(`{}`))
	assert.Equal(t, "", extractCustomToolCallInput(""))
}

// TestChatCompletionsChunkToResponsesEvents_CustomToolCallStream supplies
// complete custom wrapper input then finalizes. It checks added/input-
// done/item-done and terminal custom fields, while rejecting ordinary function
// argument events.
func TestChatCompletionsChunkToResponsesEvents_CustomToolCallStream(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState(testRuntime(), "glm-5.2")
	state.CustomTools = map[string]bool{"exec": true}

	idx := 0
	chunk := &ChatCompletionsChunk{
		ID: "cc-1",
		Choices: []ChatChunkChoice{{
			Delta: ChatDelta{
				ToolCalls: []ChatToolCall{{
					Index:    &idx,
					ID:       "call_1",
					Function: ChatFunctionCall{Name: "exec", Arguments: `{"input": "dir"}`},
				}},
			},
		}},
	}

	events := ChatCompletionsChunkToResponsesEvents(testRuntime(), chunk, state)
	events = append(events, FinalizeChatCompletionsResponsesStream(testRuntime(), state)...)

	var added, inputDone, itemDone *ResponsesStreamEvent
	for i := range events {
		evt := &events[i]
		switch evt.Type {
		case "response.output_item.added":
			if evt.Item != nil && evt.Item.Type != "message" && evt.Item.Type != "reasoning" {
				added = evt
			}
		case "response.custom_tool_call_input.done":
			inputDone = evt
		case "response.output_item.done":
			if evt.Item != nil && evt.Item.Type == "custom_tool_call" {
				itemDone = evt
			}
		case "response.function_call_arguments.delta", "response.function_call_arguments.done":
			t.Fatalf("custom 工具调用不应产出 function_call 参数事件: %s", evt.Type)
		}
	}

	require.NotNil(t, added, "缺少 custom_tool_call 的 output_item.added")
	assert.Equal(t, "custom_tool_call", added.Item.Type)
	assert.Equal(t, "exec", added.Item.Name)

	require.NotNil(t, inputDone, "缺少 response.custom_tool_call_input.done")
	assert.Equal(t, "dir", inputDone.Input)
	assert.Equal(t, "call_1", inputDone.CallID)

	require.NotNil(t, itemDone, "缺少 custom_tool_call 的 output_item.done")
	assert.Equal(t, "call_1", itemDone.Item.CallID)
	assert.Equal(t, "exec", itemDone.Item.Name)
	assert.Equal(t, "dir", itemDone.Item.Input)
	assert.Empty(t, itemDone.Item.Arguments)

	// The terminal response must include the custom_tool_call item too.
	final := events[len(events)-1]
	require.Equal(t, "response.completed", final.Type)
	require.NotNil(t, final.Response)
	foundCustom := false
	for _, item := range final.Response.Output {
		if item.Type == "custom_tool_call" {
			foundCustom = true
			assert.Equal(t, "exec", item.Name)
			assert.Equal(t, "dir", item.Input)
		}
	}
	assert.True(t, foundCustom, "response.completed 缺少 custom_tool_call 输出项")
}

// TestChatCompletionsChunkToResponsesEvents_MixedCustomNamespaceAliasStream
// supplies a namespace-looking custom alias with raw non-JSON input. Inspected
// custom events and the single terminal item must regain the original custom
// name/input.
func TestChatCompletionsChunkToResponsesEvents_MixedCustomNamespaceAliasStream(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState(testRuntime(), "deepseek-test")
	state.CustomTools = map[string]bool{"exec": true}
	state.NamespaceTools = map[string]NamespacedToolName{
		"functions__wait": {Namespace: "functions", Name: "wait"},
	}

	idx := 0
	chunk := &ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{ToolCalls: []ChatToolCall{{
		Index: &idx, ID: "call_alias", Function: ChatFunctionCall{Name: "functions__exec", Arguments: `not-json`},
	}}}}}}
	events := ChatCompletionsChunkToResponsesEvents(testRuntime(), chunk, state)
	events = append(events, FinalizeChatCompletionsResponsesStream(testRuntime(), state)...)

	for _, evt := range events {
		if evt.Type == "response.output_item.added" && evt.Item != nil && evt.Item.Type == "custom_tool_call" {
			assert.Equal(t, "exec", evt.Item.Name)
		}
		if evt.Type == "response.custom_tool_call_input.done" {
			assert.Equal(t, "exec", evt.Name)
			assert.Equal(t, "not-json", evt.Input)
		}
		if evt.Type == "response.output_item.done" && evt.Item != nil && evt.Item.Type == "custom_tool_call" {
			assert.Equal(t, "exec", evt.Item.Name)
			assert.Equal(t, "not-json", evt.Item.Input)
		}
	}

	final := events[len(events)-1]
	require.Equal(t, "response.completed", final.Type)
	require.Len(t, final.Response.Output, 1)
	assert.Equal(t, "custom_tool_call", final.Response.Output[0].Type)
	assert.Equal(t, "exec", final.Response.Output[0].Name)
	assert.Equal(t, "not-json", final.Response.Output[0].Input)
}

// TestChatCompletionsChunkToResponsesEvents_ExplicitFunctionOwnsCustomAliasCollision
// combines custom/namespace maps with an explicit conflicting function. It
// rejects custom-input done events and checks one ordinary terminal output
// retaining name/arguments.
func TestChatCompletionsChunkToResponsesEvents_ExplicitFunctionOwnsCustomAliasCollision(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState(testRuntime(), "deepseek-test")
	state.CustomTools = map[string]bool{"exec": true}
	state.FunctionTools = map[string]bool{"functions__exec": true}
	state.NamespaceTools = map[string]NamespacedToolName{
		"functions__wait": {Namespace: "functions", Name: "wait"},
	}

	idx := 0
	chunk := &ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{ToolCalls: []ChatToolCall{{
		Index: &idx, ID: "call_function", Function: ChatFunctionCall{Name: "functions__exec", Arguments: `{"path":"/tmp"}`},
	}}}}}}
	events := ChatCompletionsChunkToResponsesEvents(testRuntime(), chunk, state)
	events = append(events, FinalizeChatCompletionsResponsesStream(testRuntime(), state)...)

	for _, evt := range events {
		assert.NotEqual(t, "response.custom_tool_call_input.done", evt.Type)
	}
	final := events[len(events)-1]
	require.Equal(t, "response.completed", final.Type)
	require.Len(t, final.Response.Output, 1)
	assert.Equal(t, "function_call", final.Response.Output[0].Type)
	assert.Equal(t, "functions__exec", final.Response.Output[0].Name)
	assert.Equal(t, `{"path":"/tmp"}`, final.Response.Output[0].Arguments)
}

// TestResponsesToChatCompletionsRequest_ToolSearchToolBecomesProxyFunction
// declares client search with scalar input. The one retained Chat tool must be
// a named function whose inspected parameter schema contains query.
func TestResponsesToChatCompletionsRequest_ToolSearchToolBecomesProxyFunction(t *testing.T) {
	req := &ResponsesRequest{
		Model: "glm-5.2",
		Input: json.RawMessage(`"hi"`),
		Tools: []ResponsesTool{{Type: "tool_search"}},
	}

	out, err := ResponsesToChatCompletionsRequestWithOptions(req, nil)
	require.NoError(t, err)
	require.Len(t, out.Tools, 1)

	assert.Equal(t, "function", out.Tools[0].Type)
	assert.Equal(t, "tool_search", out.Tools[0].Function.Name)
	assert.Contains(t, string(out.Tools[0].Function.Parameters), `"query"`)
}

// TestResponsesToChatCompletionsRequest_DropsDeferredFlagWithToolSearch
// decodes client search beside a deferred ordinary function. Encoded Chat
// request must omit defer_loading and contain the search proxy name.
func TestResponsesToChatCompletionsRequest_DropsDeferredFlagWithToolSearch(t *testing.T) {
	var req ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"glm-5.2","input":"hi","tools":[{"type":"tool_search"},{"type":"function","name":"shell","defer_loading":true}]}`), &req))

	out, err := ResponsesToChatCompletionsRequestWithOptions(&req, nil)
	require.NoError(t, err)
	encoded, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "defer_loading")
	require.Contains(t, string(encoded), `"name":"tool_search"`)
}

// TestChatCompletionsResponseToResponses_ToolSearchCallOutputItem restores one
// enabled discovery proxy. It checks kind/correlation and encoded client
// execution plus object-valued arguments whose inspected query matches the
// source.
func TestChatCompletionsResponseToResponses_ToolSearchCallOutputItem(t *testing.T) {
	resp := &ChatCompletionsResponse{
		ID: "cc-1",
		Choices: []ChatChoice{{
			Message: ChatMessage{
				Role: "assistant",
				ToolCalls: []ChatToolCall{
					{ID: "call_s", Function: ChatFunctionCall{Name: "tool_search", Arguments: `{"query":"gmail","limit":2}`}},
				},
			},
		}},
	}

	out := ChatCompletionsResponseToResponses(testRuntime(), resp, "glm-5.2", nil, nil, true, nil)
	require.Len(t, out.Output, 1)

	item := out.Output[0]
	assert.Equal(t, "tool_search_call", item.Type)
	assert.Equal(t, "call_s", item.CallID)

	// Inspect the specialized codec's client execution and object-valued argument
	// shape. This fixture checks query after decoding, while limit is supplied
	// but unasserted.
	b, err := json.Marshal(item)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	assert.Equal(t, "client", m["execution"])
	args, ok := m["arguments"].(map[string]any)
	require.True(t, ok, "arguments 必须序列化为 JSON 对象")
	assert.Equal(t, "gmail", args["query"])
}

// TestChatCompletionsResponseToResponses_ToolSearchNotDeclaredKeepsFunctionCall
// returns the reserved name with search disabled. The one output must retain
// function_call kind instead of becoming a client-search item.
func TestChatCompletionsResponseToResponses_ToolSearchNotDeclaredKeepsFunctionCall(t *testing.T) {
	resp := &ChatCompletionsResponse{
		Choices: []ChatChoice{{
			Message: ChatMessage{
				Role: "assistant",
				ToolCalls: []ChatToolCall{
					{ID: "call_s", Function: ChatFunctionCall{Name: "tool_search", Arguments: `{"query":"gmail"}`}},
				},
			},
		}},
	}

	// Without a tool_search declaration, a normal function with that name keeps
	// its type.
	out := ChatCompletionsResponseToResponses(testRuntime(), resp, "glm-5.2", nil, nil, false, nil)
	require.Len(t, out.Output, 1)
	assert.Equal(t, "function_call", out.Output[0].Type)
}

// TestChatCompletionsChunkToResponsesEvents_ToolSearchCallStream feeds an
// enabled discovery call and finalizes. Added/item-done and terminal output
// must identify search; encoded SSE retains arguments/correlation/execution
// and no argument/input events appear.
func TestChatCompletionsChunkToResponsesEvents_ToolSearchCallStream(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState(testRuntime(), "glm-5.2")
	state.ToolSearchDeclared = true

	idx := 0
	chunk := &ChatCompletionsChunk{
		ID: "cc-1",
		Choices: []ChatChunkChoice{{
			Delta: ChatDelta{
				ToolCalls: []ChatToolCall{{
					Index:    &idx,
					ID:       "call_s",
					Function: ChatFunctionCall{Name: "tool_search", Arguments: `{"query":"gmail"}`},
				}},
			},
		}},
	}

	events := ChatCompletionsChunkToResponsesEvents(testRuntime(), chunk, state)
	events = append(events, FinalizeChatCompletionsResponsesStream(testRuntime(), state)...)

	var added, itemDone *ResponsesStreamEvent
	for i := range events {
		evt := &events[i]
		switch evt.Type {
		case "response.output_item.added":
			if evt.Item != nil && evt.Item.Type != "message" && evt.Item.Type != "reasoning" {
				added = evt
			}
		case "response.output_item.done":
			if evt.Item != nil && evt.Item.Type == "tool_search_call" {
				itemDone = evt
			}
		case "response.function_call_arguments.delta", "response.function_call_arguments.done",
			"response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
			t.Fatalf("tool_search 调用不应产出 %s", evt.Type)
		}
	}

	require.NotNil(t, added, "缺少 tool_search_call 的 output_item.added")
	assert.Equal(t, "tool_search_call", added.Item.Type)

	require.NotNil(t, itemDone, "缺少 tool_search_call 的 output_item.done")
	assert.Equal(t, "call_s", itemDone.Item.CallID)

	// Check serialized SSE because responsesItemWire reconstructs fields from an
	// allowlist.
	sse, err := ResponsesEventToSSE(*itemDone)
	require.NoError(t, err)
	assert.Contains(t, sse, `"execution":"client"`)
	assert.Contains(t, sse, `"arguments":{"query":"gmail"}`)
	assert.Contains(t, sse, `"call_id":"call_s"`)

	// The terminal output must also include the tool_search_call item.
	final := events[len(events)-1]
	require.Equal(t, "response.completed", final.Type)
	require.NotNil(t, final.Response)
	found := false
	for _, item := range final.Response.Output {
		if item.Type == "tool_search_call" {
			found = true
			assert.Equal(t, "call_s", item.CallID)
		}
	}
	assert.True(t, found, "response.completed 缺少 tool_search_call 输出项")
}

// TestHasToolSearchTool compares a search declaration, an ordinary function
// using that name, and nil tools. Only the declaration fixture must report
// true from the predicate.
func TestHasToolSearchTool(t *testing.T) {
	assert.True(t, HasToolSearchTool([]ResponsesTool{{Type: "tool_search"}}))
	assert.False(t, HasToolSearchTool([]ResponsesTool{{Type: "function", Name: "tool_search"}}))
	assert.False(t, HasToolSearchTool(nil))
}

// TestResponsesToChatCompletionsRequest_NamespaceToolFlattensChildren supplies
// named function and custom children in one namespace. Only the function
// survives, retaining its inspected flat name and description in one
// declaration.
func TestResponsesToChatCompletionsRequest_NamespaceToolFlattensChildren(t *testing.T) {
	req := &ResponsesRequest{
		Model: "glm-5.2",
		Input: json.RawMessage(`"hi"`),
		Tools: []ResponsesTool{{
			Type: "namespace",
			Name: "gmail",
			Tools: []ResponsesTool{
				{Type: "function", Name: "send", Description: "Send mail", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)},
				{Type: "custom", Name: "ignored_child"},
			},
		}},
	}

	out, err := ResponsesToChatCompletionsRequestWithOptions(req, nil)
	require.NoError(t, err)
	require.Len(t, out.Tools, 1, "namespace 子工具中仅 function 类型被摊平")

	assert.Equal(t, "gmail__send", out.Tools[0].Function.Name)
	assert.Equal(t, "Send mail", out.Tools[0].Function.Description)
}

// TestResponsesToolsParsing_StringToolBecomesCustom decodes a string
// declaration beside an ordinary function. Assertions check custom type/name,
// retained function type, and custom-name collection for the decoded string.
func TestResponsesToolsParsing_StringToolBecomesCustom(t *testing.T) {
	var req ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"glm-5.2","input":"hi","tools":["exec",{"type":"function","name":"wait"}]}`), &req))

	require.Len(t, req.Tools, 2)
	assert.Equal(t, "custom", req.Tools[0].Type)
	assert.Equal(t, "exec", req.Tools[0].Name)
	assert.Equal(t, "function", req.Tools[1].Type)

	assert.True(t, CustomToolNames(req.Tools)["exec"])
}

// TestFlattenNamespaceToolName_CapsAt64WithHashSuffix compares a short join
// with one oversized pair. Assertions check the short spelling, byte cap,
// separator presence, and repeated-input stability for that long fixture.
func TestFlattenNamespaceToolName_CapsAt64WithHashSuffix(t *testing.T) {
	assert.Equal(t, "gmail__send", flattenNamespaceToolName("gmail", "send"))

	long := flattenNamespaceToolName("very_long_namespace_prefix_for_testing_purposes", "and_a_rather_long_tool_name_too")
	assert.LessOrEqual(t, len(long), 64)
	assert.Contains(t, long, "__")
	// Identical input produces a stable name.
	assert.Equal(t, long, flattenNamespaceToolName("very_long_namespace_prefix_for_testing_purposes", "and_a_rather_long_tool_name_too"))
}

// TestResponsesInputToChatMessages_ToolSearchCallHistory replays discovery
// arguments and a structured output after user text. It checks proxy
// name/argument JSON, tool role/correlation, and the encoded scalar result
// text.
func TestResponsesInputToChatMessages_ToolSearchCallHistory(t *testing.T) {
	input := json.RawMessage(`[
		{"role":"user","content":"find tools"},
		{"type":"tool_search_call","call_id":"call_s","arguments":{"query":"gmail"}},
		{"type":"tool_search_output","call_id":"call_s","output":{"groups":["gmail"]}}
	]`)

	messages, err := responsesInputToChatMessagesWithOptions("", input, nil)
	require.NoError(t, err)
	require.Len(t, messages, 3)

	require.Len(t, messages[1].ToolCalls, 1)
	assert.Equal(t, "tool_search", messages[1].ToolCalls[0].Function.Name)
	assert.JSONEq(t, `{"query":"gmail"}`, messages[1].ToolCalls[0].Function.Arguments)

	assert.Equal(t, "tool", messages[2].Role)
	assert.Equal(t, "call_s", messages[2].ToolCallID)
	assert.JSONEq(t, `"{\"groups\":[\"gmail\"]}"`, string(messages[2].Content))
}

// TestResponsesToChatCompletionsRequest_PromotesCompletedToolSearchDiscoveries
// mixes static and discovered function/custom/namespace tools. It checks
// deduplication, declaration order/mappings, discovery result text, and
// restored namespace/custom response fields.
func TestResponsesToChatCompletionsRequest_PromotesCompletedToolSearchDiscoveries(t *testing.T) {
	var req ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"model":"gpt-5","tools":[
			{"type":"tool_search"},
			{"type":"function","name":"inspect","parameters":{"type":"object"}}
		],"input":[
			{"type":"tool_search_call","call_id":"search_1","arguments":{"query":"workspace"}},
			{"type":"tool_search_output","call_id":"search_1","status":"completed","execution":"client","tools":[
				{"type":"function","name":"inspect","parameters":{"type":"object"}},
				{"type":"custom","name":"exec"},
				{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object"}}]}
			]}
		]}`), &req))

	effective, err := EffectiveResponsesTools(&req)
	require.NoError(t, err)
	require.Len(t, effective, 4, "the identical static discovery must be deduplicated")
	assert.True(t, CustomToolNames(effective)["exec"])
	namespaces := NamespaceToolNames(effective)
	assert.Equal(t, NamespacedToolName{Namespace: "collaboration", Name: "spawn_agent"}, namespaces["collaboration__spawn_agent"])

	chatReq, err := ResponsesToChatCompletionsRequestWithOptions(&req, nil)
	require.NoError(t, err)
	require.Len(t, chatReq.Tools, 4)
	assert.Equal(t, []string{"tool_search", "inspect", "exec", "collaboration__spawn_agent"}, []string{
		chatReq.Tools[0].Function.Name, chatReq.Tools[1].Function.Name,
		chatReq.Tools[2].Function.Name, chatReq.Tools[3].Function.Name,
	})
	require.Len(t, chatReq.Messages, 2)
	assert.Equal(t, "tool", chatReq.Messages[1].Role)
	var discoveryPayload []map[string]any
	var serialized string
	require.NoError(t, json.Unmarshal(chatReq.Messages[1].Content, &serialized))
	require.NoError(t, json.Unmarshal([]byte(serialized), &discoveryPayload))
	require.Len(t, discoveryPayload, 3)

	responses := ChatCompletionsResponseToResponses(testRuntime(), &ChatCompletionsResponse{Choices: []ChatChoice{{
		Message: ChatMessage{Role: "assistant", ToolCalls: []ChatToolCall{
			{ID: "call_1", Type: "function", Function: ChatFunctionCall{Name: "collaboration__spawn_agent", Arguments: `{}`}},
			{ID: "call_2", Type: "function", Function: ChatFunctionCall{Name: "exec", Arguments: `{"input":"pwd"}`}},
		}},
	}}}, req.Model, CustomToolNames(effective), FunctionToolNames(effective), HasToolSearchTool(effective), namespaces)
	require.Len(t, responses.Output, 2)
	assert.Equal(t, "function_call", responses.Output[0].Type)
	assert.Equal(t, "spawn_agent", responses.Output[0].Name)
	assert.Equal(t, "collaboration", responses.Output[0].Namespace)
	assert.Equal(t, "custom_tool_call", responses.Output[1].Type)
	assert.Equal(t, "exec", responses.Output[1].Name)
	assert.Equal(t, "pwd", responses.Output[1].Input)
}

// TestEffectiveResponsesTools_RejectsDiscoveredConflict discovers a different
// schema at an existing function name. Effective collection must return a
// conflict error for that supplied static/discovered identity combination.
func TestEffectiveResponsesTools_RejectsDiscoveredConflict(t *testing.T) {
	var req ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(`{"tools":[{"type":"tool_search"},{"type":"function","name":"inspect","parameters":{"type":"object"}}],"input":[{"type":"tool_search_output","status":"completed","tools":[{"type":"function","name":"inspect","parameters":{"type":"string"}}]}]}`), &req))
	_, err := EffectiveResponsesTools(&req)
	require.ErrorContains(t, err, "conflicts")
}

// TestResponsesInputToChatMessages_NamespacedFunctionCallHistory replays one
// namespaced call and its output. The two returned turns must retain one
// assistant call whose name uses the derived namespace join.
func TestResponsesInputToChatMessages_NamespacedFunctionCallHistory(t *testing.T) {
	input := json.RawMessage(`[
		{"type":"function_call","call_id":"call_n","name":"send","namespace":"gmail","arguments":"{\"to\":\"a\"}"},
		{"type":"function_call_output","call_id":"call_n","output":"ok"}
	]`)

	messages, err := responsesInputToChatMessagesWithOptions("", input, nil)
	require.NoError(t, err)
	require.Len(t, messages, 2)

	require.Len(t, messages[0].ToolCalls, 1)
	assert.Equal(t, "gmail__send", messages[0].ToolCalls[0].Function.Name)
}

// TestResponsesChatBridge_DefaultNamespaceUsesBareNames replays a function
// declared in the default functions namespace. Chat declarations and historical
// calls use the same bare name for either accepted namespace representation.
func TestResponsesChatBridge_DefaultNamespaceUsesBareNames(t *testing.T) {
	for _, namespace := range []string{"", "functions"} {
		t.Run(namespace, func(t *testing.T) {
			var request ResponsesRequest
			require.NoError(t, json.Unmarshal([]byte(`{"model":"gpt-5.6-sol","tools":[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"js","parameters":{"type":"object"}}]}],"input":[{"type":"function_call","name":"js","namespace":"`+namespace+`","call_id":"call_1","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`), &request))
			converted, err := ResponsesToChatCompletionsRequestWithOptions(&request, nil)
			require.NoError(t, err)
			require.Len(t, converted.Tools, 1)
			require.Equal(t, "js", converted.Tools[0].Function.Name)
			require.Equal(t, "js", converted.Messages[0].ToolCalls[0].Function.Name)
		})
	}
}

// TestResponsesChatBridge_DiscoveredFunctionUsesDeclaredName supplies a plain
// function loaded by client search and its namespaced history. Chat sends the
// declared bare name instead of inventing a doubled namespace prefix.
func TestResponsesChatBridge_DiscoveredFunctionUsesDeclaredName(t *testing.T) {
	var request ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"gpt-5.6-sol","tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_output","call_id":"search_1","tools":[{"type":"function","name":"js","parameters":{"type":"object"}}]},{"type":"function_call","name":"js","namespace":"js","call_id":"call_1","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`), &request))
	converted, err := ResponsesToChatCompletionsRequestWithOptions(&request, nil)
	require.NoError(t, err)
	require.Equal(t, "js", converted.Tools[1].Function.Name)
	for _, message := range converted.Messages {
		for _, call := range message.ToolCalls {
			require.Equal(t, "js", call.Function.Name)
		}
	}
}

// TestChatCompletionsChunkToResponsesEvents_CustomToolNameArrivesLate splits
// wrapper text before its custom name arrives. It checks exactly one custom
// announcement, inspected input completion, and absence of ordinary argument
// events.
func TestChatCompletionsChunkToResponsesEvents_CustomToolNameArrivesLate(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState(testRuntime(), "glm-5.2")
	state.CustomTools = map[string]bool{"exec": true}

	idx := 0
	chunk1 := &ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{
		ToolCalls: []ChatToolCall{{Index: &idx, ID: "call_1", Function: ChatFunctionCall{Arguments: `{"inp`}}},
	}}}}
	chunk2 := &ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{
		ToolCalls: []ChatToolCall{{Index: &idx, Function: ChatFunctionCall{Name: "exec", Arguments: `ut": "dir"}`}}},
	}}}}

	var events []ResponsesStreamEvent
	events = append(events, ChatCompletionsChunkToResponsesEvents(testRuntime(), chunk1, state)...)
	events = append(events, ChatCompletionsChunkToResponsesEvents(testRuntime(), chunk2, state)...)
	events = append(events, FinalizeChatCompletionsResponsesStream(testRuntime(), state)...)

	addedCount := 0
	for _, evt := range events {
		switch evt.Type {
		case "response.output_item.added":
			if evt.Item != nil && evt.Item.Type != "reasoning" && evt.Item.Type != "message" {
				addedCount++
				assert.Equal(t, "custom_tool_call", evt.Item.Type, "迟到的名字命中 custom 工具时按 custom_tool_call 宣告")
				assert.Equal(t, "exec", evt.Item.Name)
			}
		case "response.function_call_arguments.delta", "response.function_call_arguments.done":
			t.Fatalf("custom 调用不应产出 function 参数事件: %s", evt.Type)
		case "response.custom_tool_call_input.done":
			assert.Equal(t, "dir", evt.Input)
		}
	}
	assert.Equal(t, 1, addedCount, "工具调用只宣告一次")
}

// TestChatCompletionsChunkToResponsesEvents_FunctionToolNameArrivesLate delays
// an ordinary function name while custom classification is enabled. Joined
// delta and argument-done text must include the buffered prefix, with no
// custom-input done event.
func TestChatCompletionsChunkToResponsesEvents_FunctionToolNameArrivesLate(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState(testRuntime(), "glm-5.2")
	state.CustomTools = map[string]bool{"exec": true}

	idx := 0
	chunk1 := &ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{
		ToolCalls: []ChatToolCall{{Index: &idx, ID: "call_9", Function: ChatFunctionCall{Arguments: `{"cell`}}},
	}}}}
	chunk2 := &ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{
		ToolCalls: []ChatToolCall{{Index: &idx, Function: ChatFunctionCall{Name: "wait", Arguments: `_id": 3}`}}},
	}}}}

	var events []ResponsesStreamEvent
	events = append(events, ChatCompletionsChunkToResponsesEvents(testRuntime(), chunk1, state)...)
	events = append(events, ChatCompletionsChunkToResponsesEvents(testRuntime(), chunk2, state)...)
	events = append(events, FinalizeChatCompletionsResponsesStream(testRuntime(), state)...)

	deltas := ""
	argsDone := ""
	for _, evt := range events {
		switch evt.Type {
		case "response.function_call_arguments.delta":
			deltas += evt.Delta
		case "response.function_call_arguments.done":
			argsDone = evt.Arguments
		case "response.custom_tool_call_input.done":
			t.Fatal("function 调用不应产出 custom 事件")
		}
	}
	assert.Equal(t, `{"cell_id": 3}`, deltas, "宣告前累积的参数需在宣告时补发")
	assert.Equal(t, `{"cell_id": 3}`, argsDone)
}

// TestNamespaceToolNames_MapsFlattenedNames covers primary/alternate namespace
// children, unsupported custom children, and a long hashed pair. It checks
// recorded original identities, long-name restoration, and nil result for nil
// declarations.
func TestNamespaceToolNames_MapsFlattenedNames(t *testing.T) {
	tools := []ResponsesTool{
		{Type: "namespace", Name: "gmail", Tools: []ResponsesTool{
			{Type: "function", Name: "send"},
			{Type: "custom", Name: "skip_me"},
		}},
		{Type: "namespace", Name: "crm", Children: []ResponsesTool{
			{Type: "function", Name: "query"},
		}},
		{Type: "function", Name: "wait"},
	}

	m := NamespaceToolNames(tools)
	require.Len(t, m, 2)
	assert.Equal(t, NamespacedToolName{Namespace: "gmail", Name: "send"}, m["gmail__send"])
	assert.Equal(t, NamespacedToolName{Namespace: "crm", Name: "query"}, m["crm__query"])

	// Long flattened names are truncated and hashed; the map restores the full
	// identity.
	longNS := "very_long_namespace_prefix_for_testing_purposes"
	longChild := "and_a_rather_long_tool_name_too"
	m2 := NamespaceToolNames([]ResponsesTool{{
		Type: "namespace", Name: longNS,
		Tools: []ResponsesTool{{Type: "function", Name: longChild}},
	}})
	assert.Equal(t, NamespacedToolName{Namespace: longNS, Name: longChild},
		m2[flattenNamespaceToolName(longNS, longChild)])

	assert.Nil(t, NamespaceToolNames(nil))
}

// TestResponsesToChatCompletionsRequest_RejectsToolSearchNameConflict supplies
// function/custom collisions with the reserved search name. Both must error,
// while repeated search declarations successfully produce one inspected proxy
// tool.
func TestResponsesToChatCompletionsRequest_RejectsToolSearchNameConflict(t *testing.T) {
	// A top-level function conflicts with the proxy name.
	_, err := ResponsesToChatCompletionsRequestWithOptions(&ResponsesRequest{
		Model: "glm-5.2",
		Input: json.RawMessage(`"hi"`),
		Tools: []ResponsesTool{
			{Type: "tool_search"},
			{Type: "function", Name: "tool_search"},
		},
	}, nil)
	require.Error(t, err, "与内置 tool_search 代理撞名的 function 工具必须拒绝")
	assert.Contains(t, err.Error(), "tool_search")

	// A top-level custom tool conflicts with the proxy name.
	_, err = ResponsesToChatCompletionsRequestWithOptions(&ResponsesRequest{
		Model: "glm-5.2",
		Input: json.RawMessage(`"hi"`),
		Tools: []ResponsesTool{
			{Type: "custom", Name: "tool_search"},
			{Type: "tool_search"},
		},
	}, nil)
	require.Error(t, err, "与内置 tool_search 代理撞名的 custom 工具必须拒绝")

	// Repeated tool_search declarations produce one proxy.
	out, err := ResponsesToChatCompletionsRequestWithOptions(&ResponsesRequest{
		Model: "glm-5.2",
		Input: json.RawMessage(`"hi"`),
		Tools: []ResponsesTool{{Type: "tool_search"}, {Type: "tool_search"}},
	}, nil)
	require.NoError(t, err)
	require.Len(t, out.Tools, 1)
	assert.Equal(t, "tool_search", out.Tools[0].Function.Name)
}

// TestResponsesToChatCompletionsRequest_RejectsDuplicateTopLevelExecutableNames
// compares mixed and same-kind duplicate declarations. Every set must error
// with the selected name and disambiguation diagnostic in the asserted
// message.
func TestResponsesToChatCompletionsRequest_RejectsDuplicateTopLevelExecutableNames(t *testing.T) {
	for _, tools := range [][]ResponsesTool{
		{{Type: "custom", Name: "exec"}, {Type: "function", Name: "exec"}},
		{{Type: "function", Name: "exec"}, {Type: "function", Name: "exec"}},
		{{Type: "custom", Name: "exec"}, {Type: "custom", Name: "exec"}},
	} {
		_, err := ResponsesToChatCompletionsRequestWithOptions(&ResponsesRequest{
			Model: "glm-5.2",
			Input: json.RawMessage(`"hi"`),
			Tools: tools,
		}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exec")
		assert.Contains(t, err.Error(), "cannot disambiguate")
	}
}

// TestResponsesToChatCompletionsRequest_DropsToolChoiceForDroppedTool covers
// omitted hosted targets, missing named functions, scalar auto, and retained
// function choice. It checks surviving list sizes and the inspected omitted or
// converted selectors.
func TestResponsesToChatCompletionsRequest_DropsToolChoiceForDroppedTool(t *testing.T) {
	// Drop the forced web_search choice along with the tool.
	out, err := ResponsesToChatCompletionsRequestWithOptions(&ResponsesRequest{
		Model: "glm-5.2",
		Input: json.RawMessage(`"hi"`),
		Tools: []ResponsesTool{
			{Type: "function", Name: "wait", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)},
			{Type: "web_search"},
		},
		ToolChoice: json.RawMessage(`{"type":"web_search"}`),
	}, nil)
	require.NoError(t, err)
	require.Len(t, out.Tools, 1)
	assert.Empty(t, out.ToolChoice, "指向被丢弃服务端工具的 tool_choice 必须丢弃")

	out, err = ResponsesToChatCompletionsRequestWithOptions(&ResponsesRequest{
		Model: "glm-5.2",
		Input: json.RawMessage(`"hi"`),
		Tools: []ResponsesTool{
			{Type: "function", Name: "wait", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)},
			{Type: "web_search"},
			{Type: "x_search"},
		},
		ToolChoice: json.RawMessage(`{"type":"function","name":"web_search"}`),
	}, nil)
	require.NoError(t, err)
	require.Len(t, out.Tools, 2)
	assert.Empty(t, out.ToolChoice, "surviving x_search must not keep a function tool_choice named web_search")

	// The named choice targets an absent tool.
	out, err = ResponsesToChatCompletionsRequestWithOptions(&ResponsesRequest{
		Model:      "glm-5.2",
		Input:      json.RawMessage(`"hi"`),
		Tools:      []ResponsesTool{{Type: "function", Name: "wait"}},
		ToolChoice: json.RawMessage(`{"type":"function","name":"missing"}`),
	}, nil)
	require.NoError(t, err)
	assert.Empty(t, out.ToolChoice, "指向不存在工具名的 tool_choice 必须丢弃")

	// Retain string choices and convert choices targeting surviving tools.
	out, err = ResponsesToChatCompletionsRequestWithOptions(&ResponsesRequest{
		Model:      "glm-5.2",
		Input:      json.RawMessage(`"hi"`),
		Tools:      []ResponsesTool{{Type: "function", Name: "wait"}},
		ToolChoice: json.RawMessage(`"auto"`),
	}, nil)
	require.NoError(t, err)
	assert.JSONEq(t, `"auto"`, string(out.ToolChoice))

	out, err = ResponsesToChatCompletionsRequestWithOptions(&ResponsesRequest{
		Model:      "glm-5.2",
		Input:      json.RawMessage(`"hi"`),
		Tools:      []ResponsesTool{{Type: "function", Name: "wait"}},
		ToolChoice: json.RawMessage(`{"type":"function","name":"wait"}`),
	}, nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"function","function":{"name":"wait"}}`, string(out.ToolChoice))
}

// TestResponsesToChatCompletionsRequest_ToolSearchToolChoiceMapsToProxy forces
// client search with and without its declaration. JSON equality checks the
// declared proxy selector; the undeclared search target must produce empty
// choice.
func TestResponsesToChatCompletionsRequest_ToolSearchToolChoiceMapsToProxy(t *testing.T) {
	out, err := ResponsesToChatCompletionsRequestWithOptions(&ResponsesRequest{
		Model:      "glm-5.2",
		Input:      json.RawMessage(`"hi"`),
		Tools:      []ResponsesTool{{Type: "tool_search"}},
		ToolChoice: json.RawMessage(`{"type":"tool_search"}`),
	}, nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"function","function":{"name":"tool_search"}}`, string(out.ToolChoice))

	// Without a tool_search declaration, the choice has no proxy target and is omitted.
	out, err = ResponsesToChatCompletionsRequestWithOptions(&ResponsesRequest{
		Model:      "glm-5.2",
		Input:      json.RawMessage(`"hi"`),
		Tools:      []ResponsesTool{{Type: "function", Name: "wait"}},
		ToolChoice: json.RawMessage(`{"type":"tool_search"}`),
	}, nil)
	require.NoError(t, err)
	assert.Empty(t, out.ToolChoice)
}

// TestResponsesToChatCompletionsRequest_RejectsAmbiguousFlattenedNames
// supplies a top-level join collision and two different namespace pairs
// sharing one join. Both conversions must error with the inspected conflicting
// label.
func TestResponsesToChatCompletionsRequest_RejectsAmbiguousFlattenedNames(t *testing.T) {
	// The flattened name conflicts with a top-level function.
	_, err := ResponsesToChatCompletionsRequestWithOptions(&ResponsesRequest{
		Model: "glm-5.2",
		Input: json.RawMessage(`"hi"`),
		Tools: []ResponsesTool{
			{Type: "function", Name: "gmail__send"},
			{Type: "namespace", Name: "gmail", Tools: []ResponsesTool{{Type: "function", Name: "send"}}},
		},
	}, nil)
	require.Error(t, err, "与顶层工具撞名的摊平必须拒绝")
	assert.Contains(t, err.Error(), "gmail__send")

	// Two namespace/name pairs flatten to the same name.
	_, err = ResponsesToChatCompletionsRequestWithOptions(&ResponsesRequest{
		Model: "glm-5.2",
		Input: json.RawMessage(`"hi"`),
		Tools: []ResponsesTool{
			{Type: "namespace", Name: "a", Tools: []ResponsesTool{{Type: "function", Name: "b__c"}}},
			{Type: "namespace", Name: "a__b", Tools: []ResponsesTool{{Type: "function", Name: "c"}}},
		},
	}, nil)
	require.Error(t, err, "跨 namespace 撞名的摊平必须拒绝")
	assert.Contains(t, err.Error(), "a__b__c")
}

// TestResponsesToChatCompletionsRequest_DedupesIdenticalNamespaceChildren
// repeats one named function child within a namespace. Conversion succeeds
// with one tool whose inspected name is the expected flat label.
func TestResponsesToChatCompletionsRequest_DedupesIdenticalNamespaceChildren(t *testing.T) {
	out, err := ResponsesToChatCompletionsRequestWithOptions(&ResponsesRequest{
		Model: "glm-5.2",
		Input: json.RawMessage(`"hi"`),
		Tools: []ResponsesTool{
			{Type: "namespace", Name: "gmail", Tools: []ResponsesTool{
				{Type: "function", Name: "send"},
				{Type: "function", Name: "send"},
			}},
		},
	}, nil)
	require.NoError(t, err)
	require.Len(t, out.Tools, 1, "重复声明的同一子工具只声明一次")
	assert.Equal(t, "gmail__send", out.Tools[0].Function.Name)
}

// TestChatCompletionsResponseToResponses_NamespacedToolCallRestored returns
// mapped and ordinary calls together. It checks restored
// pair/correlation/arguments and namespace JSON presence, while the unmapped
// function keeps its name and omits namespace.
func TestChatCompletionsResponseToResponses_NamespacedToolCallRestored(t *testing.T) {
	resp := &ChatCompletionsResponse{
		ID: "cc-1",
		Choices: []ChatChoice{{
			Message: ChatMessage{
				Role: "assistant",
				ToolCalls: []ChatToolCall{
					{ID: "call_n", Function: ChatFunctionCall{Name: "mcp__svc__echo", Arguments: `{"text":"hi"}`}},
					{ID: "call_9", Function: ChatFunctionCall{Name: "wait", Arguments: `{"cell_id": 3}`}},
				},
			},
		}},
	}
	nsTools := map[string]NamespacedToolName{
		"mcp__svc__echo": {Namespace: "mcp__svc", Name: "echo"},
	}

	out := ChatCompletionsResponseToResponses(testRuntime(), resp, "glm-5.2", nil, nil, false, nsTools)
	require.Len(t, out.Output, 2)

	item := out.Output[0]
	assert.Equal(t, "function_call", item.Type)
	assert.Equal(t, "echo", item.Name)
	assert.Equal(t, "mcp__svc", item.Namespace)
	assert.Equal(t, "call_n", item.CallID)
	assert.Equal(t, `{"text":"hi"}`, item.Arguments)

	// The serialized non-streaming item must include namespace.
	b, err := json.Marshal(item)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"namespace":"mcp__svc"`)
	assert.Contains(t, string(b), `"name":"echo"`)

	// A normal function outside the map keeps its name and omits namespace.
	assert.Equal(t, "wait", out.Output[1].Name)
	assert.Empty(t, out.Output[1].Namespace)
	b2, err := json.Marshal(out.Output[1])
	require.NoError(t, err)
	assert.NotContains(t, string(b2), `"namespace"`)
}

// TestChatCompletionsChunkToResponsesEvents_NamespacedToolCallStream finalizes
// one mapped function. It checks added/item-done/terminal restoration,
// complete arguments and correlation, encoded namespace fields, and absence of
// custom-input events.
func TestChatCompletionsChunkToResponsesEvents_NamespacedToolCallStream(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState(testRuntime(), "glm-5.2")
	state.NamespaceTools = map[string]NamespacedToolName{
		"mcp__svc__echo": {Namespace: "mcp__svc", Name: "echo"},
	}

	idx := 0
	chunk := &ChatCompletionsChunk{
		ID: "cc-1",
		Choices: []ChatChunkChoice{{
			Delta: ChatDelta{
				ToolCalls: []ChatToolCall{{
					Index:    &idx,
					ID:       "call_n",
					Function: ChatFunctionCall{Name: "mcp__svc__echo", Arguments: `{"text":"hi"}`},
				}},
			},
		}},
	}

	events := ChatCompletionsChunkToResponsesEvents(testRuntime(), chunk, state)
	events = append(events, FinalizeChatCompletionsResponsesStream(testRuntime(), state)...)

	var added, itemDone *ResponsesStreamEvent
	for i := range events {
		evt := &events[i]
		switch evt.Type {
		case "response.output_item.added":
			if evt.Item != nil && evt.Item.Type != "message" && evt.Item.Type != "reasoning" {
				added = evt
			}
		case "response.output_item.done":
			if evt.Item != nil && evt.Item.Type == "function_call" {
				itemDone = evt
			}
		case "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
			t.Fatalf("namespace 子工具调用不应产出 custom 事件: %s", evt.Type)
		}
	}

	require.NotNil(t, added, "缺少 namespace 调用的 output_item.added")
	assert.Equal(t, "function_call", added.Item.Type)
	assert.Equal(t, "echo", added.Item.Name)
	assert.Equal(t, "mcp__svc", added.Item.Namespace)

	require.NotNil(t, itemDone, "缺少 namespace 调用的 output_item.done")
	assert.Equal(t, "call_n", itemDone.Item.CallID)
	assert.Equal(t, "echo", itemDone.Item.Name)
	assert.Equal(t, "mcp__svc", itemDone.Item.Namespace)
	assert.Equal(t, `{"text":"hi"}`, itemDone.Item.Arguments)

	// Check namespace after responsesItemWire serializes the SSE item.
	sse, err := ResponsesEventToSSE(*itemDone)
	require.NoError(t, err)
	assert.Contains(t, sse, `"namespace":"mcp__svc"`)
	assert.Contains(t, sse, `"name":"echo"`)
	assert.Contains(t, sse, `"call_id":"call_n"`)

	// The terminal response must include the restored namespace call too.
	final := events[len(events)-1]
	require.Equal(t, "response.completed", final.Type)
	require.NotNil(t, final.Response)
	found := false
	for _, item := range final.Response.Output {
		if item.Type == "function_call" {
			found = true
			assert.Equal(t, "echo", item.Name)
			assert.Equal(t, "mcp__svc", item.Namespace)
		}
	}
	assert.True(t, found, "response.completed 缺少还原后的 namespace 调用项")
}

// TestChatCompletionsChunkToResponsesEvents_NamespacedToolNameArrivesLate
// splits arguments before mapped name arrival. Exactly one announcement must
// restore name/namespace, and accumulated ordinary deltas must include the
// complete buffered prefix.
func TestChatCompletionsChunkToResponsesEvents_NamespacedToolNameArrivesLate(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState(testRuntime(), "glm-5.2")
	state.NamespaceTools = map[string]NamespacedToolName{
		"mcp__svc__echo": {Namespace: "mcp__svc", Name: "echo"},
	}

	idx := 0
	chunk1 := &ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{
		ToolCalls: []ChatToolCall{{Index: &idx, ID: "call_n", Function: ChatFunctionCall{Arguments: `{"te`}}},
	}}}}
	chunk2 := &ChatCompletionsChunk{Choices: []ChatChunkChoice{{Delta: ChatDelta{
		ToolCalls: []ChatToolCall{{Index: &idx, Function: ChatFunctionCall{Name: "mcp__svc__echo", Arguments: `xt":"hi"}`}}},
	}}}}

	var events []ResponsesStreamEvent
	events = append(events, ChatCompletionsChunkToResponsesEvents(testRuntime(), chunk1, state)...)
	events = append(events, ChatCompletionsChunkToResponsesEvents(testRuntime(), chunk2, state)...)
	events = append(events, FinalizeChatCompletionsResponsesStream(testRuntime(), state)...)

	addedCount := 0
	deltas := ""
	for _, evt := range events {
		switch evt.Type {
		case "response.output_item.added":
			if evt.Item != nil && evt.Item.Type != "reasoning" && evt.Item.Type != "message" {
				addedCount++
				assert.Equal(t, "echo", evt.Item.Name, "迟到的名字命中 namespace 映射时按还原名宣告")
				assert.Equal(t, "mcp__svc", evt.Item.Namespace)
			}
		case "response.function_call_arguments.delta":
			deltas += evt.Delta
		}
	}
	assert.Equal(t, 1, addedCount, "工具调用只宣告一次")
	assert.Equal(t, `{"text":"hi"}`, deltas, "宣告前累积的参数需在宣告时补发")
}

// TestChatCompletionsChunkToResponsesEvents_FunctionToolStreamUnaffected
// enables unrelated custom mapping before an ordinary call. The collected
// events must include a function-argument delta and exclude custom-input done
// events.
func TestChatCompletionsChunkToResponsesEvents_FunctionToolStreamUnaffected(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState(testRuntime(), "glm-5.2")
	state.CustomTools = map[string]bool{"exec": true}

	idx := 0
	chunk := &ChatCompletionsChunk{
		Choices: []ChatChunkChoice{{
			Delta: ChatDelta{
				ToolCalls: []ChatToolCall{{
					Index:    &idx,
					ID:       "call_9",
					Function: ChatFunctionCall{Name: "wait", Arguments: `{"cell_id": 3}`},
				}},
			},
		}},
	}

	events := ChatCompletionsChunkToResponsesEvents(testRuntime(), chunk, state)
	events = append(events, FinalizeChatCompletionsResponsesStream(testRuntime(), state)...)

	sawArgsDelta := false
	for _, evt := range events {
		if evt.Type == "response.function_call_arguments.delta" {
			sawArgsDelta = true
		}
		if evt.Type == "response.custom_tool_call_input.done" {
			t.Fatal("function 工具不应产出 custom_tool_call 事件")
		}
	}
	assert.True(t, sawArgsDelta, "function 工具应保持原有参数增量事件")
}

// TestResponsesToChatReasoningCacheLookupRestoresEncryptedOnlyItem supplies an
// encrypted-only reasoning ID and a matching lookup callback. Three returned
// turns must place the cached text on the inspected assistant call message.
func TestResponsesToChatReasoningCacheLookupRestoresEncryptedOnlyItem(t *testing.T) {
	req := &ResponsesRequest{
		Model: "deepseek-reasoner",
		Input: json.RawMessage(`[
			{"type":"reasoning","id":"item_enc1","summary":[],"encrypted_content":"opaque"},
			{"type":"function_call","call_id":"call_1","name":"get_value","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}
		]`),
	}

	out, err := ResponsesToChatCompletionsRequestWithOptions(req, &ResponsesToChatOptions{
		ReasoningContentByID: func(itemID string) string {
			if itemID == "item_enc1" {
				return "cached thinking"
			}
			return ""
		},
	})
	require.NoError(t, err)
	require.Len(t, out.Messages, 3)
	require.Equal(t, "cached thinking", out.Messages[0].ReasoningContent)
	require.Len(t, out.Messages[0].ToolCalls, 1)
}

// TestResponsesToChatChainedToolCallsReplayTurnReasoning places two
// call/results within one reasoning turn, then user input and another turn.
// Call-indexed assertions check retained first-turn text and replacement
// second-turn reasoning.
func TestResponsesToChatChainedToolCallsReplayTurnReasoning(t *testing.T) {
	req := &ResponsesRequest{
		Model: "deepseek-reasoner",
		Input: json.RawMessage(`[
			{"type":"reasoning","id":"item_r1","summary":[{"type":"summary_text","text":"turn thinking"}]},
			{"type":"function_call","call_id":"call_a","name":"exec_command","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_a","output":"ok"},
			{"type":"function_call","call_id":"call_b","name":"exec_command","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_b","output":"ok"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]},
			{"type":"reasoning","id":"item_r2","summary":[{"type":"summary_text","text":"second turn"}]},
			{"type":"function_call","call_id":"call_c","name":"exec_command","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_c","output":"ok"}
		]`),
	}

	out, err := ResponsesToChatCompletionsRequestWithOptions(req, nil)
	require.NoError(t, err)
	byCallID := map[string]ChatMessage{}
	for _, message := range out.Messages {
		for _, call := range message.ToolCalls {
			byCallID[call.ID] = message
		}
	}
	require.Equal(t, "turn thinking", byCallID["call_a"].ReasoningContent)
	require.Equal(t, "turn thinking", byCallID["call_b"].ReasoningContent)
	require.Equal(t, "second turn", byCallID["call_c"].ReasoningContent)
}

// TestExtractResponsesReasoningItem compares one plaintext reasoning object
// with a message object. It checks selected ID/text and true for reasoning,
// then false for the nonreasoning fixture.
func TestExtractResponsesReasoningItem(t *testing.T) {
	id, text, ok := ExtractResponsesReasoningItem(json.RawMessage(
		`{"type":"reasoning","id":"item_a","summary":[{"type":"summary_text","text":"think"}]}`))
	require.True(t, ok)
	require.Equal(t, "item_a", id)
	require.Equal(t, "think", text)

	_, _, ok = ExtractResponsesReasoningItem(json.RawMessage(
		`{"type":"message","role":"user","content":"hi"}`))
	require.False(t, ok)
}

// TestGolden_SingleToolCall converts user/reasoning/call/result history and
// runs fixture invariants. An assistant call message must exist and carry the
// selected reasoning text in its replay field.
func TestGolden_SingleToolCall(t *testing.T) {
	msgs := convertGolden(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"latest sha?"}]},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"need to run curl"}]},
		{"type":"function_call","call_id":"call_a","name":"exec_command","arguments":"{\"cmd\":\"curl x\"}"},
		{"type":"function_call_output","call_id":"call_a","output":"deadbeef"}
	]`)
	assertChatInvariants(t, msgs)
	// Reasoning belongs on the assistant tool-call message.
	var asst *ChatMessage
	for i := range msgs {
		if len(msgs[i].ToolCalls) > 0 {
			asst = &msgs[i]
		}
	}
	require.NotNil(t, asst)
	require.Equal(t, "need to run curl", asst.ReasoningContent)
}

// TestGolden_ParallelToolCalls converts two calls/results after reasoning and
// checks common adjacency invariants. It requires two tool messages and checks
// IDs in any encountered two-call message, without requiring that message's
// presence separately.
func TestGolden_ParallelToolCalls(t *testing.T) {
	msgs := convertGolden(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"features?"}]},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"inspect repo"}]},
		{"type":"function_call","call_id":"c0","name":"exec_command","arguments":"{\"cmd\":\"git log\"}"},
		{"type":"function_call","call_id":"c1","name":"exec_command","arguments":"{\"cmd\":\"git tag\"}"},
		{"type":"function_call_output","call_id":"c0","output":"log"},
		{"type":"function_call_output","call_id":"c1","output":"tags"}
	]`)
	assertChatInvariants(t, msgs)
	// Inspect IDs when a message contains both calls, then count tool replies
	// separately. This loop does not require the two-call message to occur.
	var toolMsgs int
	for _, m := range msgs {
		if len(m.ToolCalls) == 2 {
			require.Equal(t, "c0", m.ToolCalls[0].ID)
			require.Equal(t, "c1", m.ToolCalls[1].ID)
		}
		if m.Role == "tool" {
			toolMsgs++
		}
	}
	require.Equal(t, 2, toolMsgs)
}

// TestGolden_UnknownItemBetweenToolCallAndOutput inserts an unsupported search
// item between a call and result. The converted fixture must satisfy tool-
// reply adjacency and assistant-shape invariants from the shared checker.
func TestGolden_UnknownItemBetweenToolCallAndOutput(t *testing.T) {
	msgs := convertGolden(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"search"}]},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"let me search"}]},
		{"type":"function_call","call_id":"c0","name":"exec_command","arguments":"{}"},
		{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"x"}},
		{"type":"function_call_output","call_id":"c0","output":"result"}
	]`)
	assertChatInvariants(t, msgs)
}

// TestRequest_SequentialToolCallsStaySeparate alternates two call/result
// pairs. Common invariants must pass, and two messages must each retain one
// call, checking that replies separate those assistant groups.
func TestRequest_SequentialToolCallsStaySeparate(t *testing.T) {
	msgs := convertGolden(t, `[
		{"type":"function_call","call_id":"c1","name":"exec","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"r1"},
		{"type":"function_call","call_id":"c2","name":"exec","arguments":"{}"},
		{"type":"function_call_output","call_id":"c2","output":"r2"}
	]`)
	assertChatInvariants(t, msgs)
	assistants := 0
	for _, m := range msgs {
		if len(m.ToolCalls) == 1 {
			assistants++
		}
	}
	require.Equal(t, 2, assistants)
}

// TestGolden_MessageBetweenToolCallAndOutput inserts developer approval before
// a tool result. Common invariants and per-call assertions require the
// matching tool reply immediately after its call message in the converted
// history.
func TestGolden_MessageBetweenToolCallAndOutput(t *testing.T) {
	msgs := convertGolden(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"do it"}]},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"run cmd"}]},
		{"type":"function_call","call_id":"A","name":"exec","arguments":"{}"},
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"Approved command prefix saved"}]},
		{"type":"function_call_output","call_id":"A","output":"ok"}
	]`)
	assertChatInvariants(t, msgs)
	// The corresponding tool reply immediately follows the assistant call message.
	for i, m := range msgs {
		if len(m.ToolCalls) > 0 {
			require.Equal(t, "tool", msgs[i+1].Role)
			require.Equal(t, "A", msgs[i+1].ToolCallID)
		}
	}
}

// TestGolden_PartialParallelDropsUnansweredCall supplies two parallel calls
// but one output. Common invariants must pass and no retained tool call may
// use the inspected unanswered ID.
func TestGolden_PartialParallelDropsUnansweredCall(t *testing.T) {
	msgs := convertGolden(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"r"}]},
		{"type":"function_call","call_id":"A","name":"exec","arguments":"{}"},
		{"type":"function_call","call_id":"B","name":"exec","arguments":"{}"},
		{"type":"function_call_output","call_id":"A","output":"oa"}
	]`)
	assertChatInvariants(t, msgs)
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			require.NotEqual(t, "B", tc.ID, "unanswered tool_call B should have been dropped")
		}
	}
}

// TestGolden_DanglingToolCallDropped leaves a trailing call unanswered after
// user/reasoning content. Common fixture checks must pass, and every retained
// message must have an empty tool-call list.
func TestGolden_DanglingToolCallDropped(t *testing.T) {
	msgs := convertGolden(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"r"}]},
		{"type":"function_call","call_id":"A","name":"exec","arguments":"{}"}
	]`)
	assertChatInvariants(t, msgs)
	for _, m := range msgs {
		require.Empty(t, m.ToolCalls, "dangling unanswered tool_call should have been dropped")
	}
}

// TestNormalize_DropsOrphanToolReply supplies a result whose call is absent
// beside user content. Every returned message must have a role other than tool
// for that orphan fixture.
func TestNormalize_DropsOrphanToolReply(t *testing.T) {
	msgs := convertGolden(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]},
		{"type":"function_call_output","call_id":"ghost","output":"orphan"}
	]`)
	for _, m := range msgs {
		require.NotEqualf(t, "tool", m.Role, "orphan tool reply should have been dropped")
	}
}

// TestStream_ReasoningOpensItemBeforeDelta starts with an empty reasoning
// fragment then reasoning/text. Each inspected reasoning/text delta must
// address an output index whose matching item kind was already announced.
func TestStream_ReasoningOpensItemBeforeDelta(t *testing.T) {
	events := collectStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":""}}]}`,
		`{"choices":[{"index":0,"delta":{"reasoning_content":"think"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"hello"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
	})

	open := map[int]string{} // output index to item type
	for _, e := range events {
		switch e.Type {
		case "response.output_item.added":
			require.NotNil(t, e.Item)
			open[e.OutputIndex] = e.Item.Type
		case "response.reasoning_summary_text.delta":
			require.Equalf(t, "reasoning", open[e.OutputIndex], "reasoning delta before its item was opened")
		case "response.output_text.delta":
			require.Equalf(t, "message", open[e.OutputIndex], "text delta before its item was opened")
		}
	}
}

// TestStream_ReasoningOnlySynthesizesVisibleText finalizes nonblank reasoning
// with length and no text/calls. It checks opened fallback text, completed
// text/message fields, nested incomplete details, and ordered terminal
// reasoning/message content.
func TestStream_ReasoningOnlySynthesizesVisibleText(t *testing.T) {
	events := collectStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":""}}]}`,
		`{"choices":[{"index":0,"delta":{"reasoning_content":"thinking before final"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
	})

	open := map[int]string{}
	var sawTextDelta, sawTextDone, sawMessageDone bool
	for _, e := range events {
		switch e.Type {
		case "response.output_item.added":
			require.NotNil(t, e.Item)
			open[e.OutputIndex] = e.Item.Type
		case "response.output_text.delta":
			sawTextDelta = true
			require.Equalf(t, "message", open[e.OutputIndex], "fallback text delta before its item was opened")
			require.Equal(t, "thinking before final", e.Delta)
		case "response.output_text.done":
			sawTextDone = true
			require.Equal(t, "thinking before final", e.Text)
		case "response.output_item.done":
			if e.Item != nil && e.Item.Type == "message" {
				sawMessageDone = true
				require.Equal(t, "thinking before final", e.Item.Content[0].Text)
			}
		case "response.completed":
			require.NotNil(t, e.Response)
			require.Equal(t, "incomplete", e.Response.Status)
			require.NotNil(t, e.Response.IncompleteDetails)
			require.Equal(t, "max_output_tokens", e.Response.IncompleteDetails.Reason)
			require.Len(t, e.Response.Output, 2)
			require.Equal(t, "reasoning", e.Response.Output[0].Type)
			require.Equal(t, "message", e.Response.Output[1].Type)
			require.Equal(t, "thinking before final", e.Response.Output[1].Content[0].Text)
		}
	}
	require.True(t, sawTextDelta, "reasoning-only stream must produce visible text delta")
	require.True(t, sawTextDone, "reasoning-only stream must close visible text part")
	require.True(t, sawMessageDone, "reasoning-only stream must close synthesized message item")
}

// TestStream_ReasoningOnlyBlankDoesNotSynthesizeVisibleText finalizes
// whitespace-only reasoning. No visible text delta may appear, while an
// inspected terminal keeps reasoning plus an empty message in its asserted
// output shape.
func TestStream_ReasoningOnlyBlankDoesNotSynthesizeVisibleText(t *testing.T) {
	events := collectStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"reasoning_content":"   "}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	})

	for _, e := range events {
		require.NotEqual(t, "response.output_text.delta", e.Type)
		if e.Type == "response.completed" {
			require.NotNil(t, e.Response)
			require.Len(t, e.Response.Output, 2)
			require.Equal(t, "reasoning", e.Response.Output[0].Type)
			require.Equal(t, "message", e.Response.Output[1].Type)
			require.Equal(t, "", e.Response.Output[1].Content[0].Text)
		}
	}
}

// TestStream_ReasoningThenContentDoesNotDuplicateFallbackText follows
// reasoning with an actual answer. Terminal output retains both values, and
// the complete visible delta list contains only the supplied answer fragment.
func TestStream_ReasoningThenContentDoesNotDuplicateFallbackText(t *testing.T) {
	events := collectStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"reasoning_content":"private plan"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"final answer"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	})

	var textDeltas []string
	for _, e := range events {
		switch e.Type {
		case "response.output_text.delta":
			textDeltas = append(textDeltas, e.Delta)
		case "response.completed":
			require.NotNil(t, e.Response)
			require.Len(t, e.Response.Output, 2)
			require.Equal(t, "private plan", e.Response.Output[0].Summary[0].Text)
			require.Equal(t, "final answer", e.Response.Output[1].Content[0].Text)
		}
	}
	require.Equal(t, []string{"final answer"}, textDeltas)
}

// TestStream_ReasoningThenToolCallDoesNotSynthesizeVisibleText follows
// reasoning with a function call. It rejects visible text deltas and checks
// the inspected terminal reasoning/function output kinds in order.
func TestStream_ReasoningThenToolCallDoesNotSynthesizeVisibleText(t *testing.T) {
	events := collectStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"reasoning_content":"call a tool"}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"exec","arguments":"{}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	})

	for _, e := range events {
		require.NotEqual(t, "response.output_text.delta", e.Type)
		if e.Type == "response.completed" {
			require.NotNil(t, e.Response)
			require.Len(t, e.Response.Output, 2)
			require.Equal(t, "reasoning", e.Response.Output[0].Type)
			require.Equal(t, "function_call", e.Response.Output[1].Type)
		}
	}
}

// TestStream_ToolCallLifecycleComplete splits an ordinary function's
// announcement and arguments before finalization. Added, arguments-done, and
// item-done events must appear, with inspected complete arguments and call
// correlation.
func TestStream_ToolCallLifecycleComplete(t *testing.T) {
	events := collectStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"plan"}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"exec","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":\"ls\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
	})

	var sawAdded, sawArgsDone, sawItemDone bool
	for _, e := range events {
		switch e.Type {
		case "response.output_item.added":
			if e.Item != nil && e.Item.Type == "function_call" {
				sawAdded = true
			}
		case "response.function_call_arguments.done":
			sawArgsDone = true
			require.Equal(t, `{"cmd":"ls"}`, e.Arguments)
		case "response.output_item.done":
			if e.Item != nil && e.Item.Type == "function_call" {
				sawItemDone = true
				require.Equal(t, `{"cmd":"ls"}`, e.Item.Arguments)
				require.Equal(t, "call_a", e.Item.CallID)
			}
		}
	}
	require.True(t, sawAdded, "function_call output_item.added missing")
	require.True(t, sawArgsDone, "function_call_arguments.done missing")
	require.True(t, sawItemDone, "function_call output_item.done missing")
}

// TestStream_ToolCallArgumentsInFirstChunkNotDoubled puts correlation, name,
// and arguments in one tool fragment. Joined delta text and both completion
// payloads must contain the source arguments once.
func TestStream_ToolCallArgumentsInFirstChunkNotDoubled(t *testing.T) {
	events := collectStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"exec","arguments":"{\"cmd\":\"ls\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	})

	var argsDelta strings.Builder
	var sawArgsDone, sawItemDone bool
	for _, e := range events {
		switch e.Type {
		case "response.function_call_arguments.delta":
			// Collect actual emitted argument fragments for the no-duplication check.
			// This local Builder is not copied and WriteString's discarded error is nil.
			_, _ = argsDelta.WriteString(e.Delta)
		case "response.function_call_arguments.done":
			sawArgsDone = true
			require.Equal(t, `{"cmd":"ls"}`, e.Arguments)
		case "response.output_item.done":
			if e.Item != nil && e.Item.Type == "function_call" {
				sawItemDone = true
				require.Equal(t, `{"cmd":"ls"}`, e.Item.Arguments)
			}
		}
	}
	require.True(t, sawArgsDone, "function_call_arguments.done missing")
	require.True(t, sawItemDone, "function_call output_item.done missing")
	require.Equal(t, `{"cmd":"ls"}`, argsDelta.String(), "arguments delta 不应重复累加")
}

// TestStream_InvalidToolArgumentsAreRejectedBeforeFinalize records one
// truncated ordinary function argument string. The explicit validator must
// return an invalid-JSON error; this fixture does not invoke finalization.
func TestStream_InvalidToolArgumentsAreRejectedBeforeFinalize(t *testing.T) {
	idx := 0
	state := NewChatCompletionsToResponsesStreamState(testRuntime(), "deepseek-v4-flash")
	chunk := &ChatCompletionsChunk{
		Choices: []ChatChunkChoice{
			{
				Index: 0,
				Delta: ChatDelta{
					ToolCalls: []ChatToolCall{
						{
							Index: &idx,
							ID:    "call_bad",
							Type:  "function",
							Function: ChatFunctionCall{
								Name:      "exec_command",
								Arguments: `{"cmd": "ssh root@HOST`,
							},
						},
					},
				},
			},
		},
	}
	ChatCompletionsChunkToResponsesEvents(testRuntime(), chunk, state)

	err := state.ValidateToolCallArguments()
	require.ErrorContains(t, err, "invalid JSON")
}

// TestStream_ValidToolCallAtOutputLimitKeepsIncompleteResponse records valid
// arguments and selects length before finalization. Validation succeeds,
// complete argument-done appears, and nested terminal status must remain
// incomplete.
func TestStream_ValidToolCallAtOutputLimitKeepsIncompleteResponse(t *testing.T) {
	idx := 0
	state := NewChatCompletionsToResponsesStreamState(testRuntime(), "deepseek-v4-flash")
	chunk := &ChatCompletionsChunk{
		Choices: []ChatChunkChoice{
			{
				Index: 0,
				Delta: ChatDelta{
					ToolCalls: []ChatToolCall{
						{
							Index: &idx,
							ID:    "call_at_limit",
							Type:  "function",
							Function: ChatFunctionCall{
								Name:      "exec_command",
								Arguments: `{}`,
							},
						},
					},
				},
			},
		},
	}
	ChatCompletionsChunkToResponsesEvents(testRuntime(), chunk, state)
	state.FinishReason = "length"

	require.NoError(t, state.ValidateToolCallArguments())
	events := FinalizeChatCompletionsResponsesStream(testRuntime(), state)
	var sawArgsDone, sawIncomplete bool
	for _, event := range events {
		switch event.Type {
		case "response.function_call_arguments.done":
			sawArgsDone = true
			require.Equal(t, `{}`, event.Arguments)
		case "response.completed":
			require.NotNil(t, event.Response)
			sawIncomplete = event.Response.Status == "incomplete"
		}
	}
	require.True(t, sawArgsDone)
	require.True(t, sawIncomplete)
}

// TestChatCompletionsResponseToResponses_DeepSeekReasoningOnlyFallsBackToMessageText
// supplies a named-model fixture with reasoning but empty visible text.
// Returned reasoning/message items must include the inspected reasoning string
// as visible fallback text.
func TestChatCompletionsResponseToResponses_DeepSeekReasoningOnlyFallsBackToMessageText(t *testing.T) {
	content := json.RawMessage(`""`)
	resp := &ChatCompletionsResponse{
		ID:     "chatcmpl_deepseek_reasoning_only",
		Object: "chat.completion",
		Model:  "deepseek-reasoner",
		Choices: []ChatChoice{{
			Index: 0,
			Message: ChatMessage{
				Role:             "assistant",
				Content:          content,
				ReasoningContent: "reasoning-only answer",
			},
			FinishReason: "stop",
		}},
	}

	out := ChatCompletionsResponseToResponses(testRuntime(), resp, "deepseek-reasoner", nil, nil, false, nil)

	require.Len(t, out.Output, 2)
	require.Equal(t, "reasoning", out.Output[0].Type)
	require.Equal(t, "message", out.Output[1].Type)
	require.Len(t, out.Output[1].Content, 1)
	assert.Equal(t, "reasoning-only answer", out.Output[1].Content[0].Text)
}

// TestChatCompletionsResponseToResponses_DeepSeekReasoningToolCallDoesNotFallbackToMessageText
// adds a function call to textless reasoning. Returned output must contain
// reasoning then function, with the inspected call name and no fallback
// message.
func TestChatCompletionsResponseToResponses_DeepSeekReasoningToolCallDoesNotFallbackToMessageText(t *testing.T) {
	content := json.RawMessage(`""`)
	resp := &ChatCompletionsResponse{
		ID:     "chatcmpl_deepseek_reasoning_tool",
		Object: "chat.completion",
		Model:  "deepseek-reasoner",
		Choices: []ChatChoice{{
			Index: 0,
			Message: ChatMessage{
				Role:             "assistant",
				Content:          content,
				ReasoningContent: "call a tool",
				ToolCalls: []ChatToolCall{{
					ID:   "call_a",
					Type: "function",
					Function: ChatFunctionCall{
						Name:      "exec",
						Arguments: `{}`,
					},
				}},
			},
			FinishReason: "tool_calls",
		}},
	}

	out := ChatCompletionsResponseToResponses(testRuntime(), resp, "deepseek-reasoner", nil, nil, false, nil)

	require.Len(t, out.Output, 2)
	require.Equal(t, "reasoning", out.Output[0].Type)
	require.Equal(t, "function_call", out.Output[1].Type)
	assert.Equal(t, "exec", out.Output[1].Name)
}

// TestResponsesToolOutputMedia_ExtractsSupportedShapes covers eight
// array/object/wrapper/string/custom/search result fixtures. It checks paired
// turns, marker text, moved URL exclusion, optional retained sibling text, and
// the attributed user image part.
func TestResponsesToolOutputMedia_ExtractsSupportedShapes(t *testing.T) {
	tests := []struct {
		name string // media fixture case label
		// call, outputType, and output construct one correlated history pair.
		// The fixture inserts each raw JSON value under call_image and compares
		// rewritten scalar text with the attributed user image representation.
		call       string // source call JSON object
		outputType string // source result kind label
		output     string // source output JSON value
		// imageURL is absent from rewritten tool text and appears in the returned
		// user image part. Each case checks that same expected location pair.
		imageURL string // expected extracted image location
		// toolText adds a case-specific retained sibling substring check. Empty text
		// disables that extra assertion while common marker/image checks still run.
		toolText string // optional retained sibling substring
	}{
		{
			name:       "image-only array",
			call:       `{"type":"function_call","call_id":"call_image","name":"view_image","arguments":"{}"}`,
			outputType: "function_call_output",
			output:     `[{"type":"input_image","image_url":"data:image/png;base64,AQID"}]`,
			imageURL:   testToolImageDataURL,
		},
		{
			name:       "text and nested image URL",
			call:       `{"type":"function_call","call_id":"call_image","name":"view_image","arguments":"{}"}`,
			outputType: "function_call_output",
			output:     `[{"type":"input_text","text":"render complete"},{"type":"image_url","image_url":{"url":"https://example.com/tool-output.png"}}]`,
			imageURL:   testToolImageRemote,
			toolText:   "render complete",
		},
		{
			name:       "top-level image object",
			call:       `{"type":"function_call","call_id":"call_image","name":"view_image","arguments":"{}"}`,
			outputType: "function_call_output",
			output:     `{"type":"input_image","image_url":"data:image/png;base64,AQID"}`,
			imageURL:   testToolImageDataURL,
		},
		{
			name:       "content wrapper",
			call:       `{"type":"function_call","call_id":"call_image","name":"view_image","arguments":"{}"}`,
			outputType: "function_call_output",
			output:     `{"status":"ok","content":[{"type":"input_image","image_url":"data:image/png;base64,AQID"}],"unknown":{"score":0.9,"large":9007199254740993}}`,
			imageURL:   testToolImageDataURL,
			toolText:   `"large":9007199254740993`,
		},
		{
			name:       "JSON string output",
			call:       `{"type":"function_call","call_id":"call_image","name":"view_image","arguments":"{}"}`,
			outputType: "function_call_output",
			output:     `"[{\"type\":\"input_image\",\"image_url\":\"data:image/png;base64,AQID\"}]"`,
			imageURL:   testToolImageDataURL,
		},
		{
			name:       "bare data URL",
			call:       `{"type":"function_call","call_id":"call_image","name":"view_image","arguments":"{}"}`,
			outputType: "function_call_output",
			output:     `"data:image/png;base64,AQID"`,
			imageURL:   testToolImageDataURL,
		},
		{
			name:       "custom tool output",
			call:       `{"type":"custom_tool_call","call_id":"call_image","name":"view_image","input":"{}"}`,
			outputType: "custom_tool_call_output",
			output:     `[{"type":"input_image","image_url":"data:image/png;base64,AQID"}]`,
			imageURL:   testToolImageDataURL,
		},
		{
			name:       "tool search output",
			call:       `{"type":"tool_search_call","call_id":"call_image","arguments":{"query":"image"}}`,
			outputType: "tool_search_output",
			output:     `[{"type":"image_url","image_url":{"url":"https://example.com/tool-output.png"}}]`,
			imageURL:   testToolImageRemote,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := fmt.Sprintf(`[%s,{"type":%q,"call_id":"call_image","output":%s}]`, tt.call, tt.outputType, tt.output)
			messages := convertToolOutputMedia(t, input)

			require.Len(t, messages, 3)
			require.Equal(t, []string{"assistant", "tool", "user"}, chatMessageRoles(messages))
			require.Equal(t, "call_image", messages[1].ToolCallID)

			toolText := chatToolContentString(t, messages[1])
			require.Contains(t, toolText, "[Tool output media moved to the following user message]")
			require.NotContains(t, toolText, tt.imageURL)
			if tt.toolText != "" {
				require.Contains(t, toolText, tt.toolText)
			}

			parts := chatContentParts(t, messages[2])
			require.Len(t, parts, 2)
			require.Equal(t, "text", parts[0].Type)
			require.Equal(t, "[Tool output media for call call_image]", parts[0].Text)
			require.Equal(t, "image_url", parts[1].Type)
			require.NotNil(t, parts[1].ImageURL)
			require.Equal(t, tt.imageURL, parts[1].ImageURL.URL)
		})
	}
}

// TestResponsesToolOutputMedia_ParallelBatchUsesCallOrder supplies results in
// reverse call order. Assertions check ordered tool correlations and an
// attributed image batch whose labels/URLs follow the original call list.
func TestResponsesToolOutputMedia_ParallelBatchUsesCallOrder(t *testing.T) {
	messages := convertToolOutputMedia(t, `[
		{"type":"function_call","call_id":"call_A","name":"view_image","arguments":"{}"},
		{"type":"function_call","call_id":"call_B","name":"view_image","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_B","output":[{"type":"input_image","image_url":{"url":"https://example.com/b.png"}}]},
		{"type":"function_call_output","call_id":"call_A","output":[{"type":"input_image","image_url":{"url":"https://example.com/a.png"}}]}
	]`)

	require.Len(t, messages, 4)
	require.Equal(t, []string{"assistant", "tool", "tool", "user"}, chatMessageRoles(messages))
	require.Equal(t, []string{"call_A", "call_B"}, []string{messages[1].ToolCallID, messages[2].ToolCallID})

	parts := chatContentParts(t, messages[3])
	require.Len(t, parts, 4)
	require.Equal(t, "[Tool output media for call call_A]", parts[0].Text)
	require.Equal(t, "https://example.com/a.png", parts[1].ImageURL.URL)
	require.Equal(t, "[Tool output media for call call_B]", parts[2].Text)
	require.Equal(t, "https://example.com/b.png", parts[3].ImageURL.URL)
}

// TestResponsesToolOutputMedia_PreservesRichSiblingWhenRewriting places a rich
// nonimage object beside inline media. JSON equality checks the retained
// sibling fields and complete replacement marker object in scalar tool output.
func TestResponsesToolOutputMedia_PreservesRichSiblingWhenRewriting(t *testing.T) {
	messages := convertToolOutputMedia(t, `[
		{"type":"function_call","call_id":"call_image","name":"view_image","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_image","output":[
			{"type":"result","url":"https://example.com/result","score":0.9,"text":"complete","extra":{"count":2}},
			{"type":"input_image","image_url":"data:image/png;base64,AQID"}
		]}
	]`)

	toolText := chatToolContentString(t, messages[1])
	require.JSONEq(t, `[
		{"type":"result","url":"https://example.com/result","score":0.9,"text":"complete","extra":{"count":2}},
		{"type":"input_text","text":"[Tool output media moved to the following user message]"}
	]`, toolText)
}

// TestResponsesToolOutputMedia_InterleavedMessagesFollowMediaBatch inserts
// developer/user messages before a correlated image result. It checks repaired
// role order, the attributed media label, and retained later scalar
// instruction/continuation values.
func TestResponsesToolOutputMedia_InterleavedMessagesFollowMediaBatch(t *testing.T) {
	messages := convertToolOutputMedia(t, `[
		{"type":"function_call","call_id":"call_A","name":"view_image","arguments":"{}"},
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"approval saved"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]},
		{"type":"function_call_output","call_id":"call_A","output":[{"type":"input_image","image_url":"data:image/png;base64,AQID"}]}
	]`)

	require.Equal(t, []string{"assistant", "tool", "user", "system", "user"}, chatMessageRoles(messages))
	require.Equal(t, "[Tool output media for call call_A]", chatContentParts(t, messages[2])[0].Text)
	require.JSONEq(t, `"approval saved"`, string(messages[3].Content))
	require.JSONEq(t, `"continue"`, string(messages[4].Content))
}

// TestResponsesToolOutputMedia_DropsOrphanAndUnansweredCallMedia covers orphan
// output and partially answered parallel calls. Assertions require empty
// orphan history or one retained answered call/media pair with no unanswered
// attribution.
func TestResponsesToolOutputMedia_DropsOrphanAndUnansweredCallMedia(t *testing.T) {
	t.Run("orphan", func(t *testing.T) {
		messages := convertToolOutputMedia(t, `[
			{"type":"function_call_output","call_id":"call_ghost","output":[{"type":"input_image","image_url":"data:image/png;base64,AQID"}]}
		]`)
		require.Empty(t, messages)
	})

	t.Run("unanswered parallel call", func(t *testing.T) {
		messages := convertToolOutputMedia(t, `[
			{"type":"function_call","call_id":"call_A","name":"view_image","arguments":"{}"},
			{"type":"function_call","call_id":"call_B","name":"view_image","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_A","output":[{"type":"input_image","image_url":"data:image/png;base64,AQID"}]}
		]`)

		require.Len(t, messages, 3)
		require.Len(t, messages[0].ToolCalls, 1)
		require.Equal(t, "call_A", messages[0].ToolCalls[0].ID)
		parts := chatContentParts(t, messages[2])
		require.Len(t, parts, 2)
		require.NotContains(t, string(messages[2].Content), "call_B")
	})
}

// TestResponsesToolOutputMedia_PreservesMediaFreeOutputBytes covers rich
// objects, no-image arrays, plain/JSON strings, and embedded URL text. The
// scalar tool content must equal each decoded-string or raw-JSON expectation
// after normal encoding.
func TestResponsesToolOutputMedia_PreservesMediaFreeOutputBytes(t *testing.T) {
	tests := []struct {
		name string // media-free fixture case label
		// output is inserted as raw result JSON. Expected tool text uses its
		// decoded string when possible, otherwise the complete raw spelling,
		// before normal scalar encoding is compared with returned content.
		output string // supplied result JSON value
	}{
		{
			name:   "rich unknown object",
			output: `{"type":"result","url":"https://example.com/result","score":0.9,"text":"complete","extra":{"count":2}}`,
		},
		{
			name:   "no-image array",
			output: `[ { "type": "input_text", "text": "ok" }, {"unknown":true} ]`,
		},
		{
			name:   "plain string",
			output: `"plain output"`,
		},
		{
			name:   "JSON string without image",
			output: `"{ \"ok\": true }"`,
		},
		{
			name:   "embedded data URL text",
			output: `"prefix data:image/png;base64,AQID suffix"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := fmt.Sprintf(`[
				{"type":"function_call","call_id":"call_text","name":"exec","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_text","output":%s}
			]`, tt.output)
			messages := convertToolOutputMedia(t, input)
			require.Len(t, messages, 2)

			var expected string
			if err := json.Unmarshal([]byte(tt.output), &expected); err != nil {
				// A nonstring fixture value remains raw JSON text on the no-media path.
				// Use its complete spelling so equality detects layout changes inside that
				// scalar result.
				expected = tt.output
			}
			expectedContent, err := json.Marshal(expected)
			require.NoError(t, err)
			require.Equal(t, string(expectedContent), string(messages[1].Content))
		})
	}
}

// TestResponsesToolOutputMedia_DuplicateCallIDIsLastWins supplies repeated
// outputs under one correlation. Later images replace earlier URLs, while a
// later text-only result removes the earlier media message and retains its
// text.
func TestResponsesToolOutputMedia_DuplicateCallIDIsLastWins(t *testing.T) {
	t.Run("later media replaces earlier media", func(t *testing.T) {
		messages := convertToolOutputMedia(t, `[
			{"type":"function_call","call_id":"call_image","name":"view_image","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_image","output":[{"type":"input_image","image_url":{"url":"https://example.com/first.png"}}]},
			{"type":"function_call_output","call_id":"call_image","output":[{"type":"input_image","image_url":{"url":"https://example.com/last.png"}}]}
		]`)

		require.Len(t, messages, 3)
		require.NotContains(t, string(messages[1].Content), "first.png")
		require.NotContains(t, string(messages[2].Content), "first.png")
		require.Contains(t, string(messages[2].Content), "last.png")
	})

	t.Run("later text clears earlier media", func(t *testing.T) {
		messages := convertToolOutputMedia(t, `[
			{"type":"function_call","call_id":"call_image","name":"view_image","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_image","output":[{"type":"input_image","image_url":"data:image/png;base64,AQID"}]},
			{"type":"function_call_output","call_id":"call_image","output":"latest text"}
		]`)

		require.Len(t, messages, 2)
		require.Equal(t, "latest text", chatToolContentString(t, messages[1]))
	})
}

// TestResponsesToChatCompletionsRequest_ToolContentNeverContainsExtractedMedia
// mixes function/custom/search results with recognized images. Common
// invariants pass, three tool messages exclude inspected media strings, and
// the repaired role sequence ends with one user batch.
func TestResponsesToChatCompletionsRequest_ToolContentNeverContainsExtractedMedia(t *testing.T) {
	req := &ResponsesRequest{
		Model: "vision-model",
		Input: json.RawMessage(`[
			{"type":"function_call","call_id":"call_function","name":"view_image","arguments":"{}"},
			{"type":"custom_tool_call","call_id":"call_custom","name":"custom_image","input":"{}"},
			{"type":"tool_search_call","call_id":"call_search","arguments":{"query":"image"}},
			{"type":"function_call_output","call_id":"call_function","output":[{"type":"input_image","image_url":"data:image/png;base64,AQID"}]},
			{"type":"custom_tool_call_output","call_id":"call_custom","output":{"content":[{"type":"image_url","image_url":{"url":"https://example.com/custom.png"}}]}},
			{"type":"tool_search_output","call_id":"call_search","output":"data:image/jpeg;base64,BAUG"}
		]`),
	}

	out, err := ResponsesToChatCompletionsRequestWithOptions(req, nil)
	require.NoError(t, err)
	assertChatInvariants(t, out.Messages)

	var toolCount int
	for _, message := range out.Messages {
		if message.Role != "tool" {
			continue
		}
		toolCount++
		content := string(message.Content)
		require.NotContains(t, content, "data:image/")
		require.NotContains(t, content, "https://example.com/custom.png")
	}
	require.Equal(t, 3, toolCount)
	require.Equal(t, []string{"assistant", "tool", "tool", "tool", "user"}, chatMessageRoles(out.Messages))
}

// TestResponsesToChatCompletionsPreservesXSearchTool supplies hosted search
// with handle filters and extra options. Assertions inspect retained
// kind/allowed/excluded handles and the hosted selector; date/media option
// values are supplied but unasserted.
func TestResponsesToChatCompletionsPreservesXSearchTool(t *testing.T) {
	enabled := true
	req := &ResponsesRequest{
		Model: "grok-4.5",
		Input: json.RawMessage(`"latest xAI post"`),
		Tools: []ResponsesTool{{
			Type:                     "x_search",
			AllowedXHandles:          []string{"xai"},
			ExcludedXHandles:         []string{"spam"},
			FromDate:                 "2026-08-01",
			ToDate:                   "2026-08-10",
			EnableImageUnderstanding: &enabled,
			EnableVideoUnderstanding: &enabled,
		}},
		ToolChoice: json.RawMessage(`{"type":"x_search"}`),
	}

	chat, err := ResponsesToChatCompletionsRequestWithOptions(req, nil)
	require.NoError(t, err)
	require.Len(t, chat.Tools, 1)
	require.Equal(t, "x_search", chat.Tools[0].Type)
	require.Equal(t, []string{"xai"}, chat.Tools[0].AllowedXHandles)
	require.Equal(t, []string{"spam"}, chat.Tools[0].ExcludedXHandles)
	require.JSONEq(t, `{"type":"x_search"}`, string(chat.ToolChoice))
}

// TestResponsesToChatCompletionsXSearchToolChoiceString selects declared
// hosted search through a scalar string. JSON equality checks retention of
// that selector after successful conversion of the supplied request.
func TestResponsesToChatCompletionsXSearchToolChoiceString(t *testing.T) {
	chat, err := ResponsesToChatCompletionsRequestWithOptions(&ResponsesRequest{
		Model:      "grok-4.5",
		Input:      json.RawMessage(`"latest xAI post"`),
		Tools:      []ResponsesTool{{Type: "x_search"}},
		ToolChoice: json.RawMessage(`"x_search"`),
	}, nil)
	require.NoError(t, err)
	require.JSONEq(t, `"x_search"`, string(chat.ToolChoice))
}

// TestChatCompletionsResponseToResponses_CarriesCreatedAt covers positive
// source time, omitted time, and nil reply. It checks exact supplied timestamp
// retention or positive runtime-stamped creation for the fallback fixtures.
func TestChatCompletionsResponseToResponses_CarriesCreatedAt(t *testing.T) {
	t.Run("uses_upstream_created_when_present", func(t *testing.T) {
		out := ChatCompletionsResponseToResponses(testRuntime(), &ChatCompletionsResponse{
			ID:      "chatcmpl_1",
			Created: 1700000000,
			Model:   "deepseek-v4-flash",
			Choices: []ChatChoice{{Message: ChatMessage{Role: "assistant", Content: json.RawMessage(`"hi"`)}}},
		}, "deepseek-v4-flash", nil, nil, false, nil)
		require.EqualValues(t, 1700000000, out.CreatedAt, "上游给了 created 就照搬，不要另起时间")
	})

	t.Run("stamps_now_when_upstream_omits_created", func(t *testing.T) {
		out := ChatCompletionsResponseToResponses(testRuntime(), &ChatCompletionsResponse{
			ID:      "chatcmpl_2",
			Model:   "deepseek-v4-flash",
			Choices: []ChatChoice{{Message: ChatMessage{Role: "assistant", Content: json.RawMessage(`"hi"`)}}},
		}, "deepseek-v4-flash", nil, nil, false, nil)
		require.Greater(t, out.CreatedAt, int64(0))
	})

	t.Run("nil_upstream_response_still_stamps", func(t *testing.T) {
		out := ChatCompletionsResponseToResponses(testRuntime(), nil, "deepseek-v4-flash", nil, nil, false, nil)
		require.Greater(t, out.CreatedAt, int64(0), "空上游响应也必须产出可解析的对象")
	})
}

// TestChatCompletionsToResponsesStream_CreatedAtStableAcrossEvents converts
// and finalizes one text chunk. Creation and terminal snapshots must both
// contain the captured positive state timestamp, with equal inspected creation
// times.
func TestChatCompletionsToResponsesStream_CreatedAtStableAcrossEvents(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState(testRuntime(), "deepseek-v4-flash")
	require.Greater(t, state.Created, int64(0), "前提：state 早就采集了时间戳")

	var chunk ChatCompletionsChunk
	require.NoError(t, json.Unmarshal(
		[]byte(`{"choices":[{"index":0,"delta":{"content":"hi"}}]}`), &chunk))

	events := ChatCompletionsChunkToResponsesEvents(testRuntime(), &chunk, state)
	events = append(events, FinalizeChatCompletionsResponsesStream(testRuntime(), state)...)

	seen := map[string]int64{}
	for _, evt := range events {
		if evt.Response == nil {
			continue
		}
		seen[evt.Type] = requireCreatedAt(t, responseObjectOf(t, evt))
	}

	require.Contains(t, seen, "response.created")
	require.Contains(t, seen, "response.completed")
	require.Equal(t, state.Created, seen["response.created"])
	require.Equal(t, seen["response.created"], seen["response.completed"],
		"同一条流的 created_at 必须恒定")
}

// TestChatCompletionsResponseToResponses_PreservesServiceTier converts a
// first-choice reply with default tier. The returned field must retain that
// value, and encoded Responses JSON must contain its service_tier member.
func TestChatCompletionsResponseToResponses_PreservesServiceTier(t *testing.T) {
	cc := &ChatCompletionsResponse{
		ID:          "chatcmpl-1",
		Model:       "gpt-5.5",
		ServiceTier: "default",
		Choices: []ChatChoice{{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: json.RawMessage(`"hi"`)},
			FinishReason: "stop",
		}},
		Usage: &ChatUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}
	resp := ChatCompletionsResponseToResponses(testRuntime(), cc, "gpt-5.5", nil, nil, false, nil)
	require.Equal(t, "default", resp.ServiceTier)

	raw, err := json.Marshal(resp)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"service_tier":"default"`)
}

// TestChatCompletionsResponseToResponses_NilRespOmitsServiceTier converts a
// nil source with the supplied model and runtime. Its service-tier field must
// remain empty in the returned typed response.
func TestChatCompletionsResponseToResponses_NilRespOmitsServiceTier(t *testing.T) {
	resp := ChatCompletionsResponseToResponses(testRuntime(), nil, "gpt-5.5", nil, nil, false, nil)
	require.Empty(t, resp.ServiceTier)
}

// TestChatCompletionsChunkToResponsesEvents_PreservesServiceTier supplies flex
// tier on one text chunk. Both located creation and terminal snapshots must
// contain that observed tier after conversion and finalization.
func TestChatCompletionsChunkToResponsesEvents_PreservesServiceTier(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState(testRuntime(), "gpt-5.5")
	chunk := &ChatCompletionsChunk{
		ID:          "chatcmpl-2",
		Model:       "gpt-5.5",
		ServiceTier: "flex",
		Choices: []ChatChunkChoice{{
			Index: 0,
			Delta: ChatDelta{Content: strPtr("hi")},
		}},
	}
	events := ChatCompletionsChunkToResponsesEvents(testRuntime(), chunk, state)
	require.NotEmpty(t, events)

	// The creation event carries service_tier.
	created := findEvent(events, "response.created")
	require.NotNil(t, created)
	require.NotNil(t, created.Response)
	require.Equal(t, "flex", created.Response.ServiceTier)

	// The terminal response carries it too.
	final := FinalizeChatCompletionsResponsesStream(testRuntime(), state)
	completed := findEvent(final, "response.completed")
	require.NotNil(t, completed)
	require.NotNil(t, completed.Response)
	require.Equal(t, "flex", completed.Response.ServiceTier)
}

// TestChatCompletionsChunkToResponsesEvents_NoTierStaysClean supplies a
// metadata-only chunk without tier. The located creation snapshot must have
// empty typed tier, and its encoded event must omit service_tier.
func TestChatCompletionsChunkToResponsesEvents_NoTierStaysClean(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState(testRuntime(), "gpt-5.5")
	chunk := &ChatCompletionsChunk{ID: "chatcmpl-3", Model: "gpt-5.5"}
	events := ChatCompletionsChunkToResponsesEvents(testRuntime(), chunk, state)
	created := findEvent(events, "response.created")
	require.NotNil(t, created)
	require.Empty(t, created.Response.ServiceTier)
	raw, err := json.Marshal(created)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "service_tier")
}

// chatMessageRoles returns supplied message roles in order for fixture
// sequence comparisons.
func chatMessageRoles(messages []ChatMessage) []string {
	roles := make([]string, 0, len(messages))
	for _, message := range messages {
		roles = append(roles, message.Role)
	}
	return roles
}

// assertChatInvariants checks replies immediately following each call list in
// call order, prohibits consecutive assistant roles, and requires nonempty
// tool-message IDs. Failed assertions stop the test; it does not independently
// match every orphan reply backward or validate message content JSON.
func assertChatInvariants(t *testing.T, messages []ChatMessage) {
	t.Helper()
	for i, m := range messages {
		// A call message defines the exact following reply positions. Check each
		// correlation there so relocation cannot reorder parallel results in these
		// fixtures.
		if len(m.ToolCalls) > 0 {
			for j, tc := range m.ToolCalls {
				k := i + 1 + j
				require.Lessf(t, k, len(messages), "tool_call %s has no following tool message", tc.ID)
				require.Equalf(t, "tool", messages[k].Role, "tool_call %s not followed by a tool message", tc.ID)
				require.Equalf(t, tc.ID, messages[k].ToolCallID, "tool reply order mismatch for %s", tc.ID)
			}
		}
		// These fixtures must not produce consecutive assistant messages.
		if i > 0 && m.Role == "assistant" && messages[i-1].Role == "assistant" {
			t.Fatalf("consecutive assistant messages at %d", i)
		}
		// Every tool message needs a call ID.
		if m.Role == "tool" {
			require.NotEmptyf(t, m.ToolCallID, "tool message without tool_call_id at %d", i)
		}
	}
}

// convertGolden converts raw fixture input with fixed top-level instructions
// and no reasoning lookup. Conversion errors stop the test; scenario callers
// run their selected invariant checks and inspect the returned message values.
func convertGolden(t *testing.T, input string) []ChatMessage {
	t.Helper()
	msgs, err := responsesInputToChatMessagesWithOptions("You are a helpful assistant.", json.RawMessage(input), nil)
	require.NoError(t, err)
	return msgs
}

// convertToolOutputMedia converts raw fixture input with no instructions or
// reasoning lookup and checks common Chat invariants. Decode/conversion
// failures stop the test; the returned messages support case-specific marker,
// ordering, and image assertions.
func convertToolOutputMedia(t *testing.T, input string) []ChatMessage {
	t.Helper()
	messages, err := responsesInputToChatMessagesWithOptions("", json.RawMessage(input), nil)
	require.NoError(t, err)
	assertChatInvariants(t, messages)
	return messages
}

// chatToolContentString requires exact tool role and string-decodable content,
// failing the test otherwise. It returns the selected scalar text for
// marker/equality assertions; required call correlation is checked elsewhere.
func chatToolContentString(t *testing.T, message ChatMessage) string {
	t.Helper()
	require.Equal(t, "tool", message.Role)
	var content string
	require.NoError(t, json.Unmarshal(message.Content, &content))
	return content
}

// chatContentParts requires array-decodable fixture content and checks nonnil,
// nonblank URLs for every image_url part. Failures stop the test; URL
// scheme/base64 correctness and other part fields are outside these
// assertions.
func chatContentParts(t *testing.T, message ChatMessage) []ChatContentPart {
	t.Helper()
	var parts []ChatContentPart
	require.NoError(t, json.Unmarshal(message.Content, &parts))
	for _, part := range parts {
		if part.Type == "image_url" {
			require.NotNil(t, part.ImageURL)
			require.False(t, strings.TrimSpace(part.ImageURL.URL) == "")
		}
	}
	return parts
}

// findEvent returns a pointer to the first exact matching event in the
// supplied slice. No match returns nil, and the pointer shares original slice
// storage; callers check presence before inspecting or mutating it.
func findEvent(events []ResponsesStreamEvent, eventType string) *ResponsesStreamEvent {
	for i := range events {
		if events[i].Type == eventType {
			return &events[i]
		}
	}
	return nil
}
