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
	testToolImageDataURL = "data:image/png;base64,AQID"
	testToolImageRemote  = "https://example.com/tool-output.png"
)

func TestResponsesInputToChatMessages_DeveloperRoleMapsToSystem(t *testing.T) {
	messages, err := responsesInputToChatMessagesWithOptions("", json.RawMessage(`[{"role":"developer","content":"follow project instructions"}]`), nil)
	require.NoError(t, err)
	require.Len(t, messages, 1)

	assert.Equal(t, "system", messages[0].Role)
	assert.JSONEq(t, `"follow project instructions"`, string(messages[0].Content))
}

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

func TestResponsesInputToChatMessages_EmptyRoleFallsBackToUser(t *testing.T) {
	messages, err := responsesInputToChatMessagesWithOptions("", json.RawMessage(`[{"role":"","content":"hello"}]`), nil)
	require.NoError(t, err)
	require.Len(t, messages, 1)

	assert.Equal(t, "user", messages[0].Role)
}

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

	chatReq, err := ResponsesToChatCompletionsRequestWithOptions(req, nil)
	require.NoError(t, err)
	require.Len(t, chatReq.Tools, 2)
	assert.Equal(t, "exec", chatReq.Tools[0].Function.Name)
	assert.Equal(t, "functions__wait", chatReq.Tools[1].Function.Name)

	customTools := CustomToolNames(req.Tools)
	namespaceTools := NamespaceToolNames(req.Tools)
	resp := &ChatCompletionsResponse{Choices: []ChatChoice{{Message: ChatMessage{ToolCalls: []ChatToolCall{
		{ID: "call_exact", Function: ChatFunctionCall{Name: "exec", Arguments: `{"input":"pwd"}`}},
		{ID: "call_wait", Function: ChatFunctionCall{Name: "functions__wait", Arguments: `{"cell_id":"1"}`}},
		{ID: "call_alias", Function: ChatFunctionCall{Name: "functions__exec", Arguments: `not-json`}},
	}}}}}

	out := ChatCompletionsResponseToResponses(testRuntime(), resp, req.Model, customTools, FunctionToolNames(req.Tools), false, namespaceTools)
	require.Len(t, out.Output, 3)
	assert.Equal(t, "custom_tool_call", out.Output[0].Type)
	assert.Equal(t, "exec", out.Output[0].Name)
	assert.Equal(t, "pwd", out.Output[0].Input)
	assert.Equal(t, "function_call", out.Output[1].Type)
	assert.Equal(t, "functions", out.Output[1].Namespace)
	assert.Equal(t, "wait", out.Output[1].Name)
	assert.Equal(t, "custom_tool_call", out.Output[2].Type)
	assert.Equal(t, "exec", out.Output[2].Name)
	assert.Equal(t, "not-json", out.Output[2].Input)
}

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

func TestEffectiveResponsesTools_SkipsStringInputItems(t *testing.T) {
	req := &ResponsesRequest{
		Input: json.RawMessage(`["plain input",{"type":"additional_tools","tools":[{"type":"custom","name":"exec"}]}]`),
	}

	tools, err := EffectiveResponsesTools(req)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "exec", tools[0].Name)
}

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

func TestEffectiveResponsesTools_RejectsMalformedAdditionalTools(t *testing.T) {
	req := &ResponsesRequest{
		Input: json.RawMessage(`[{"type":"additional_tools","tools":"not-an-array"}]`),
	}

	tools, err := EffectiveResponsesTools(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse responses additional tools item")
	assert.Empty(t, tools)
}

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

func TestExtractCustomToolCallInput_FallsBackToRawArguments(t *testing.T) {
	assert.Equal(t, "dir", extractCustomToolCallInput(`{"input": "dir"}`))
	assert.Equal(t, "console.log(1)", extractCustomToolCallInput(`console.log(1)`))
	assert.Equal(t, `{"other": "x"}`, extractCustomToolCallInput(`{"other": "x"}`))
	assert.Equal(t, "", extractCustomToolCallInput(`{}`))
	assert.Equal(t, "", extractCustomToolCallInput(""))
}

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

// TestChatCompletionsResponseToResponses_ToolSearchCallOutputItem checks restoration
// of tool_search_call with execution=client. Codex rejects a same-named function_call
// in ToolSearchHandler because the payload type does not match.
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

	// The serialized item needs execution=client and object-valued arguments
	// from which Codex reads query and limit.
	b, err := json.Marshal(item)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	assert.Equal(t, "client", m["execution"])
	args, ok := m["arguments"].(map[string]any)
	require.True(t, ok, "arguments 必须序列化为 JSON 对象")
	assert.Equal(t, "gmail", args["query"])
}

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

	// Without a tool_search declaration, a normal function with that name keeps its type.
	out := ChatCompletionsResponseToResponses(testRuntime(), resp, "glm-5.2", nil, nil, false, nil)
	require.Len(t, out.Output, 1)
	assert.Equal(t, "function_call", out.Output[0].Type)
}

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

	// Check serialized SSE because responsesItemWire reconstructs fields from an allowlist.
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

func TestHasToolSearchTool(t *testing.T) {
	assert.True(t, HasToolSearchTool([]ResponsesTool{{Type: "tool_search"}}))
	assert.False(t, HasToolSearchTool([]ResponsesTool{{Type: "function", Name: "tool_search"}}))
	assert.False(t, HasToolSearchTool(nil))
}

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

func TestResponsesToolsParsing_StringToolBecomesCustom(t *testing.T) {
	var req ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"glm-5.2","input":"hi","tools":["exec",{"type":"function","name":"wait"}]}`), &req))

	require.Len(t, req.Tools, 2)
	assert.Equal(t, "custom", req.Tools[0].Type)
	assert.Equal(t, "exec", req.Tools[0].Name)
	assert.Equal(t, "function", req.Tools[1].Type)

	assert.True(t, CustomToolNames(req.Tools)["exec"])
}

func TestFlattenNamespaceToolName_CapsAt64WithHashSuffix(t *testing.T) {
	assert.Equal(t, "gmail__send", flattenNamespaceToolName("gmail", "send"))

	long := flattenNamespaceToolName("very_long_namespace_prefix_for_testing_purposes", "and_a_rather_long_tool_name_too")
	assert.LessOrEqual(t, len(long), 64)
	assert.Contains(t, long, "__")
	// Identical input produces a stable name.
	assert.Equal(t, long, flattenNamespaceToolName("very_long_namespace_prefix_for_testing_purposes", "and_a_rather_long_tool_name_too"))
}

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

func TestEffectiveResponsesTools_RejectsDiscoveredConflict(t *testing.T) {
	var req ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(`{"tools":[{"type":"tool_search"},{"type":"function","name":"inspect","parameters":{"type":"object"}}],"input":[{"type":"tool_search_output","status":"completed","tools":[{"type":"function","name":"inspect","parameters":{"type":"string"}}]}]}`), &req))
	_, err := EffectiveResponsesTools(&req)
	require.ErrorContains(t, err, "conflicts")
}

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

	// Long flattened names are truncated and hashed; the map restores the full identity.
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

// TestResponsesToChatCompletionsRequest_RejectsToolSearchNameConflict checks proxy-name conflicts.
// A collision would restore an ordinary call as tool_search_call on return.
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

// TestResponsesToChatCompletionsRequest_DropsToolChoiceForDroppedTool checks choices of omitted or absent tools.
// Those choices cause upstream 400 errors; strings and choices of retained tools still convert.
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

// TestResponsesToChatCompletionsRequest_ToolSearchToolChoiceMapsToProxy checks forced proxy selection.
// Dropping this choice would let the model decide whether to search.
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

// TestResponsesToChatCompletionsRequest_RejectsAmbiguousFlattenedNames checks conversion errors for name collisions.
// Chat dispatches by name alone; ambiguous flattened names would restore the wrong tool.
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

// TestResponsesToChatCompletionsRequest_DedupesIdenticalNamespaceChildren checks repeated child declarations.
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

// TestChatCompletionsResponseToResponses_NamespacedToolCallRestored checks separate namespace/name fields.
// Codex dispatches by both fields and rejects an unrestored flattened name
// as an unsupported call.
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

// TestGolden_SingleToolCall covers a Codex turn with reasoning, one call, and its reply.
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

// TestGolden_ParallelToolCalls covers Codex running multiple commands in one turn.
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
	// Parallel calls share one assistant message.
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

// TestGolden_UnknownItemBetweenToolCallAndOutput checks that unsupported items
// between a call and its output do not break call/reply adjacency.
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

// TestRequest_SequentialToolCallsStaySeparate checks that a reply separates sequential call messages.
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

// TestGolden_MessageBetweenToolCallAndOutput covers an intervening Codex notification.
// Move it after the tool reply so the call and reply remain adjacent.
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

// TestGolden_PartialParallelDropsUnansweredCall covers a missing parallel reply,
// such as after interruption; every retained call must have an output.
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

// TestGolden_DanglingToolCallDropped checks that an unanswered trailing call is removed.
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

// TestNormalize_DropsOrphanToolReply checks removal of replies with no assistant call.
func TestNormalize_DropsOrphanToolReply(t *testing.T) {
	msgs := convertGolden(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]},
		{"type":"function_call_output","call_id":"ghost","output":"orphan"}
	]`)
	for _, m := range msgs {
		require.NotEqualf(t, "tool", m.Role, "orphan tool reply should have been dropped")
	}
}

// TestStream_ReasoningOpensItemBeforeDelta checks announcement before reasoning deltas.
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

// TestStream_ToolCallLifecycleComplete checks function_call_arguments.done and
// output_item.done, both needed by Codex before executing the tool.
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

// TestStream_ToolCallArgumentsInFirstChunkNotDoubled covers GLM/Zhipu chunks
// containing the ID, name, and arguments in one tool-call delta.
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

func TestResponsesToolOutputMedia_ExtractsSupportedShapes(t *testing.T) {
	tests := []struct {
		name       string
		call       string
		outputType string
		output     string
		imageURL   string
		toolText   string
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

func TestResponsesToolOutputMedia_PreservesMediaFreeOutputBytes(t *testing.T) {
	tests := []struct {
		name   string
		output string
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
				expected = tt.output
			}
			expectedContent, err := json.Marshal(expected)
			require.NoError(t, err)
			require.Equal(t, string(expectedContent), string(messages[1].Content))
		})
	}
}

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

// TestChatCompletionsToResponsesStream_CreatedAtStableAcrossEvents checks one creation time per response.
// response.created and the terminal event must use the same timestamp.
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

func TestChatCompletionsResponseToResponses_NilRespOmitsServiceTier(t *testing.T) {
	resp := ChatCompletionsResponseToResponses(testRuntime(), nil, "gpt-5.5", nil, nil, false, nil)
	require.Empty(t, resp.ServiceTier)
}

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

func chatMessageRoles(messages []ChatMessage) []string {
	roles := make([]string, 0, len(messages))
	for _, message := range messages {
		roles = append(roles, message.Role)
	}
	return roles
}

// assertChatInvariants checks tool-reply ordering and assistant-message shape
// for Codex fixtures converted to DeepSeek/OpenAI Chat requests.
func assertChatInvariants(t *testing.T, messages []ChatMessage) {
	t.Helper()
	for i, m := range messages {
		// Replies immediately follow the assistant's calls in call order.
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

func convertGolden(t *testing.T, input string) []ChatMessage {
	t.Helper()
	msgs, err := responsesInputToChatMessagesWithOptions("You are a helpful assistant.", json.RawMessage(input), nil)
	require.NoError(t, err)
	return msgs
}

func convertToolOutputMedia(t *testing.T, input string) []ChatMessage {
	t.Helper()
	messages, err := responsesInputToChatMessagesWithOptions("", json.RawMessage(input), nil)
	require.NoError(t, err)
	assertChatInvariants(t, messages)
	return messages
}

func chatToolContentString(t *testing.T, message ChatMessage) string {
	t.Helper()
	require.Equal(t, "tool", message.Role)
	var content string
	require.NoError(t, json.Unmarshal(message.Content, &content))
	return content
}

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

func findEvent(events []ResponsesStreamEvent, eventType string) *ResponsesStreamEvent {
	for i := range events {
		if events[i].Type == eventType {
			return &events[i]
		}
	}
	return nil
}
