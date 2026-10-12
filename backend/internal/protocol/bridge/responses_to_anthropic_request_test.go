package bridge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// anthropicInboundBlockTypes supplies the exact labels accepted by the fixture
// content checker; it does not validate the fields required by each block kind.
var anthropicInboundBlockTypes = map[string]bool{
	"text":              true,
	"image":             true,
	"document":          true,
	"tool_use":          true,
	"tool_result":       true,
	"thinking":          true,
	"redacted_thinking": true,
}

// TestResponsesToAnthropicRequest_ToolChoiceFunctionName supplies a top-level
// Responses function name. The decoded Messages choice must use tool type and
// retain that selected weather name.
func TestResponsesToAnthropicRequest_ToolChoiceFunctionName(t *testing.T) {
	req := &ResponsesRequest{
		Model:      "gpt-5.2",
		Input:      json.RawMessage(`[{"role":"user","content":"Hello"}]`),
		ToolChoice: json.RawMessage(`{"type":"function","name":"get_weather"}`),
	}

	resp, err := ResponsesToAnthropicRequest(req)
	require.NoError(t, err)

	var tc map[string]string
	require.NoError(t, json.Unmarshal(resp.ToolChoice, &tc))
	assert.Equal(t, "tool", tc["type"])
	assert.Equal(t, "get_weather", tc["name"])
}

// TestResponsesToAnthropicRequest_ToolChoiceLegacyFunctionName supplies the
// legacy nested function-name shape. The converted selector must decode to
// tool type and the same selected weather name.
func TestResponsesToAnthropicRequest_ToolChoiceLegacyFunctionName(t *testing.T) {
	req := &ResponsesRequest{
		Model:      "gpt-5.2",
		Input:      json.RawMessage(`[{"role":"user","content":"Hello"}]`),
		ToolChoice: json.RawMessage(`{"type":"function","function":{"name":"get_weather"}}`),
	}

	resp, err := ResponsesToAnthropicRequest(req)
	require.NoError(t, err)

	var tc map[string]string
	require.NoError(t, json.Unmarshal(resp.ToolChoice, &tc))
	assert.Equal(t, "tool", tc["type"])
	assert.Equal(t, "get_weather", tc["name"])
}

// TestResponsesToAnthropicRequest_RejectsUltraReasoningEffort supplies ultra
// reasoning with scalar input. Conversion must return an unsupported-effort
// error and a nil request for that supplied level.
func TestResponsesToAnthropicRequest_RejectsUltraReasoningEffort(t *testing.T) {
	req := &ResponsesRequest{
		Model:     "gpt-5.6-sol",
		Input:     json.RawMessage(`"Hello"`),
		Reasoning: &ResponsesReasoning{Effort: "ultra"},
	}

	resp, err := ResponsesToAnthropicRequest(req)
	require.ErrorContains(t, err, "not supported")
	require.Nil(t, resp)
}

// TestOpenAIPriorityConvertsToClaudeFast converts a priority Responses request
// containing scalar user input. Its Messages speed field must be fast after
// successful request conversion.
func TestOpenAIPriorityConvertsToClaudeFast(t *testing.T) {
	input, err := json.Marshal("hello")
	require.NoError(t, err)
	converted, err := ResponsesToAnthropicRequest(&ResponsesRequest{
		Model:       "claude-opus-4.8",
		Input:       input,
		ServiceTier: "priority",
	})
	require.NoError(t, err)
	require.Equal(t, "fast", converted.Speed)
}

// TestResponsesToAnthropicRequest_Instructions covers present/absent
// instructions, instructions beside a system item, and scalar input.
// Assertions check system text presence/content and the selected output
// message shape for those four fixtures.
func TestResponsesToAnthropicRequest_Instructions(t *testing.T) {
	t.Run("instructions_becomes_system", func(t *testing.T) {
		req := &ResponsesRequest{
			Model:        "claude-sonnet-4-20250514",
			Instructions: "You are a helpful assistant.",
			Input:        json.RawMessage(`[{"role":"user","content":"hello"}]`),
		}

		result, err := ResponsesToAnthropicRequest(req)
		require.NoError(t, err)

		var system string
		require.NoError(t, json.Unmarshal(result.System, &system))
		assert.Equal(t, "You are a helpful assistant.", system)
		assert.NotEmpty(t, result.Messages)
	})

	t.Run("empty_instructions_no_system", func(t *testing.T) {
		req := &ResponsesRequest{
			Model: "claude-sonnet-4-20250514",
			Input: json.RawMessage(`[{"role":"user","content":"hello"}]`),
		}

		result, err := ResponsesToAnthropicRequest(req)
		require.NoError(t, err)
		assert.Nil(t, result.System)
	})

	t.Run("instructions_and_system_item_concatenated", func(t *testing.T) {
		req := &ResponsesRequest{
			Model:        "claude-sonnet-4-20250514",
			Instructions: "Top-level instruction.",
			Input: json.RawMessage(`[
				{"role":"system","content":"Input-level system prompt."},
				{"role":"user","content":"hello"}
			]`),
		}

		result, err := ResponsesToAnthropicRequest(req)
		require.NoError(t, err)

		var system string
		require.NoError(t, json.Unmarshal(result.System, &system))
		assert.Contains(t, system, "Top-level instruction.")
		assert.Contains(t, system, "Input-level system prompt.")
	})

	t.Run("instructions_with_string_input", func(t *testing.T) {
		req := &ResponsesRequest{
			Model:        "claude-sonnet-4-20250514",
			Instructions: "Be concise.",
			Input:        json.RawMessage(`"What is Go?"`),
		}

		result, err := ResponsesToAnthropicRequest(req)
		require.NoError(t, err)

		var system string
		require.NoError(t, json.Unmarshal(result.System, &system))
		assert.Equal(t, "Be concise.", system)
		require.Len(t, result.Messages, 1)
		assert.Equal(t, "user", result.Messages[0].Role)
	})
}

// TestConvertResponsesInputToAnthropic_DeveloperRole supplies developer text
// as blocks or a scalar beside user content. It checks system extraction,
// exclusion from inspected user text, and instructions-before-developer
// concatenation.
func TestConvertResponsesInputToAnthropic_DeveloperRole(t *testing.T) {
	t.Run("developer_becomes_system", func(t *testing.T) {
		input := `[
			{"role":"developer","content":[{"type":"input_text","text":"You are a code reviewer."}]},
			{"role":"user","content":"review this code"}
		]`

		system, messages, err := convertResponsesInputToAnthropic("", json.RawMessage(input))
		require.NoError(t, err)

		var systemText string
		require.NoError(t, json.Unmarshal(system, &systemText))
		assert.Equal(t, "You are a code reviewer.", systemText)

		require.Len(t, messages, 1)
		assert.Equal(t, "user", messages[0].Role)
	})

	t.Run("developer_does_not_become_user", func(t *testing.T) {
		input := `[
			{"role":"developer","content":[{"type":"input_text","text":"System prompt."}]},
			{"role":"user","content":"hi"}
		]`

		_, messages, err := convertResponsesInputToAnthropic("", json.RawMessage(input))
		require.NoError(t, err)

		for _, m := range messages {
			if m.Role == "user" {
				var s string
				if json.Unmarshal(m.Content, &s) == nil {
					assert.NotContains(t, s, "System prompt.")
				}
			}
		}
	})

	t.Run("instructions_and_developer_concatenated_in_order", func(t *testing.T) {
		input := `[
			{"role":"developer","content":"Extra context."},
			{"role":"user","content":"hello"}
		]`

		system, _, err := convertResponsesInputToAnthropic("Main instruction.", json.RawMessage(input))
		require.NoError(t, err)

		var systemText string
		require.NoError(t, json.Unmarshal(system, &systemText))
		assert.Equal(t, "Main instruction.\n\nExtra context.", systemText)
	})
}

// TestResponsesToAnthropic_ReasoningItemWithContentIsDropped replays a
// reasoning_text item after valid user content. One accepted message remains,
// and neither the reasoning kind nor its text appears in its content.
func TestResponsesToAnthropic_ReasoningItemWithContentIsDropped(t *testing.T) {
	messages := responsesToAnthropicMessages(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"run a shell command"}]},
		{"type":"reasoning","id":"rs_1","summary":[],"content":[{"type":"reasoning_text","text":"let me think"}]}
	]`)

	requireAnthropicMessagesAreSendable(t, messages)
	require.Len(t, messages, 1)
	require.NotContains(t, string(messages[0].Content), "reasoning_text")
	require.NotContains(t, string(messages[0].Content), "let me think")
}

// TestResponsesToAnthropic_ReasoningItemSummaryOnlyStillDropped replays
// summary/encrypted reasoning after one user item. The accepted output retains
// one message and excludes the inspected encrypted signature from its content.
func TestResponsesToAnthropic_ReasoningItemSummaryOnlyStillDropped(t *testing.T) {
	messages := responsesToAnthropicMessages(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
		{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"s"}],"encrypted_content":"gAAAA"}
	]`)

	requireAnthropicMessagesAreSendable(t, messages)
	require.Len(t, messages, 1)
	require.NotContains(t, string(messages[0].Content), "gAAAA")
}

// TestResponsesToAnthropic_UnknownItemTypeContentIsSanitized supplies a
// search-call item containing an unsupported result part. Filtering must
// produce no Messages turns, and the common content checks inspect that
// result.
func TestResponsesToAnthropic_UnknownItemTypeContentIsSanitized(t *testing.T) {
	messages := responsesToAnthropicMessages(t, `[
		{"type":"web_search_call","id":"ws_1","content":[{"type":"web_search_result","text":"payload"}]}
	]`)

	requireAnthropicMessagesAreSendable(t, messages)
	require.Empty(t, messages, "整条内容都无法映射时不应发出消息")
}

// TestResponsesToAnthropic_UnknownItemTypeKeepsRecognizableText mixes
// input_text and reasoning_text under an unfamiliar item type. One accepted
// message keeps the first text and excludes the unsupported contribution.
func TestResponsesToAnthropic_UnknownItemTypeKeepsRecognizableText(t *testing.T) {
	messages := responsesToAnthropicMessages(t, `[
		{"type":"some_future_item","content":[
			{"type":"input_text","text":"keep me"},
			{"type":"reasoning_text","text":"drop me"}
		]}
	]`)

	requireAnthropicMessagesAreSendable(t, messages)
	require.Len(t, messages, 1)
	require.Contains(t, string(messages[0].Content), "keep me")
	require.NotContains(t, string(messages[0].Content), "drop me")
}

// TestResponsesToAnthropic_UserMessageWithOnlyUnknownPartsIsDropped supplies a
// user message consisting of a file part. The converter must return no
// Messages turns after that unsupported content is filtered.
func TestResponsesToAnthropic_UserMessageWithOnlyUnknownPartsIsDropped(t *testing.T) {
	messages := responsesToAnthropicMessages(t, `[
		{"type":"message","role":"user","content":[{"type":"input_file","file_id":"file_1"}]}
	]`)

	requireAnthropicMessagesAreSendable(t, messages)
	require.Empty(t, messages)
}

// TestResponsesToAnthropic_AssistantMessageWithOnlyUnknownPartsIsDropped
// places an unsupported refusal after valid user text. Only the accepted user
// message remains, with no empty assistant turn.
func TestResponsesToAnthropic_AssistantMessageWithOnlyUnknownPartsIsDropped(t *testing.T) {
	messages := responsesToAnthropicMessages(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
		{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"no"}]}
	]`)

	requireAnthropicMessagesAreSendable(t, messages)
	require.Len(t, messages, 1)
	require.Equal(t, "user", messages[0].Role)
}

// TestResponsesToAnthropic_BlankTextMessagesAreDropped compares blank user
// strings/blocks, assistant blocks, and an unfamiliar item. Every supplied
// fixture must yield no turns after conversion and the common content checks.
func TestResponsesToAnthropic_BlankTextMessagesAreDropped(t *testing.T) {
	tests := []struct {
		name  string // blank-content fixture case label
		input string // supplied Responses input JSON
	}{
		{"user_string", `[{"type":"message","role":"user","content":"   "}]`},
		{"user_block", `[{"type":"message","role":"user","content":[{"type":"input_text","text":"\n\t"}]}]`},
		{"assistant_block", `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"  "}]}]`},
		{"future_item", `[{"type":"future_item","content":[{"type":"input_text","text":"  "}]}]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			messages := responsesToAnthropicMessages(t, tc.input)
			requireAnthropicMessagesAreSendable(t, messages)
			require.Empty(t, messages)
		})
	}
}

// TestResponsesToAnthropic_BlankTextBesideImageKeepsImage combines blank user
// text with an inline image. One message containing one image block remains
// after blank-part filtering and content checks.
func TestResponsesToAnthropic_BlankTextBesideImageKeepsImage(t *testing.T) {
	messages := responsesToAnthropicMessages(t, `[
		{"type":"message","role":"user","content":[
			{"type":"input_text","text":"  "},
			{"type":"input_image","image_url":"data:image/png;base64,YQ=="}
		]}
	]`)

	requireAnthropicMessagesAreSendable(t, messages)
	require.Len(t, messages, 1)
	blocks := parseContentBlocks(messages[0].Content)
	require.Len(t, blocks, 1)
	require.Equal(t, "image", blocks[0].Type)
}

// TestResponsesToAnthropic_CodexToolRoundStaysIntactAndSendable interleaves
// user text, encrypted reasoning, a call/result pair, and continuation text.
// Assertions find the expected correlated tool blocks and exclude reasoning
// text/signature from encoded messages.
func TestResponsesToAnthropic_CodexToolRoundStaysIntactAndSendable(t *testing.T) {
	messages := responsesToAnthropicMessages(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"run ls"}]},
		{"type":"reasoning","id":"rs_1","summary":[],"content":[{"type":"reasoning_text","text":"plan"}],"encrypted_content":"gAAAA"},
		{"type":"function_call","id":"fc_1","call_id":"call_1","name":"shell","arguments":"{\"cmd\":\"ls\"}"},
		{"type":"function_call_output","call_id":"call_1","output":"file1"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}
	]`)

	requireAnthropicMessagesAreSendable(t, messages)

	var sawToolUse, sawToolResult bool
	for _, m := range messages {
		for _, b := range parseContentBlocks(m.Content) {
			switch b.Type {
			case "tool_use":
				sawToolUse = true
				require.Equal(t, "call_1", b.ID)
				require.Equal(t, "shell", b.Name)
			case "tool_result":
				sawToolResult = true
				require.Equal(t, "call_1", b.ToolUseID)
			}
		}
	}
	require.True(t, sawToolUse, "function_call 必须转成 tool_use")
	require.True(t, sawToolResult, "function_call_output 必须转成 tool_result")
	require.NotContains(t, string(mustMarshal(t, messages)), "reasoning_text")
	require.NotContains(t, string(mustMarshal(t, messages)), "gAAAA")
}

// TestAnthropicContentIsEmpty compares absent, null, empty-array, blank-
// string, and nonempty-content fixtures. Each predicate result must match its
// expected flag, including whitespace around selected representations.
func TestAnthropicContentIsEmpty(t *testing.T) {
	cases := []struct {
		// raw and want pair one JSON content representation with its expected
		// predicate result. Fixtures distinguish empty encodings and blank scalar
		// text from a nonempty text block or scalar.
		raw  string // supplied content JSON text
		want bool   // expected content predicate result
	}{
		{``, true},
		{`""`, true},
		{`"   "`, true},
		{`null`, true},
		{`[]`, true},
		{`  []  `, true},
		{`"hi"`, false},
		{`" hi "`, false},
		{`[{"type":"text","text":"hi"}]`, false},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, anthropicContentIsEmpty(json.RawMessage(tc.raw)), "raw=%q", tc.raw)
	}
}

// TestAnthropicContentIsOnlyBlankText compares blank-only text arrays with
// nonblank, mixed image, and empty arrays. The predicate must match each
// expected flag, separating empty content from nonempty blank blocks.
func TestAnthropicContentIsOnlyBlankText(t *testing.T) {
	cases := []struct {
		// raw and want pair one JSON content representation with its expected
		// predicate result. Cases compare emptiness rules, including blank text and
		// mixed block arrays.
		raw  string // supplied content JSON text
		want bool   // expected content predicate result
	}{
		{`[{"type":"text","text":""}]`, true},
		{`[{"type":"text","text":"   "}]`, true},
		{`[{"type":"text","text":""},{"type":"text","text":" "}]`, true},
		{`[{"type":"text","text":"hi"}]`, false},
		{`[{"type":"text","text":""},{"type":"image","source":{}}]`, false},
		{`[]`, false},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, anthropicContentIsOnlyBlankText(json.RawMessage(tc.raw)), "raw=%q", tc.raw)
	}
}

// TestAnthropicPairing_DeveloperMessageBetween places developer approval text
// between a call and its output. Common assertions require alternating
// adjacent pairs, and the inspected call must have its correlated user result
// next.
func TestAnthropicPairing_DeveloperMessageBetween(t *testing.T) {
	msgs := convertAnthropic(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"do it"}]},
		{"type":"function_call","call_id":"call_A","name":"exec","arguments":"{}"},
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"Approved command prefix saved"}]},
		{"type":"function_call_output","call_id":"call_A","output":"ok"}
	]`)
	for i, m := range msgs {
		if hasToolUse(parseContentBlocks(m.Content), "call_A") {
			require.Equal(t, "user", msgs[i+1].Role)
			require.True(t, hasToolResult(parseContentBlocks(msgs[i+1].Content), "call_A"))
		}
	}
}

// TestAnthropicPairing_ParallelBothAnswered supplies two calls followed by
// both results. Common pairing assertions validate the converted turns, and
// both tool-use IDs must occur in one assistant message.
func TestAnthropicPairing_ParallelBothAnswered(t *testing.T) {
	msgs := convertAnthropic(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"features?"}]},
		{"type":"function_call","call_id":"call_c0","name":"exec","arguments":"{}"},
		{"type":"function_call","call_id":"call_c1","name":"exec","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_c0","output":"log"},
		{"type":"function_call_output","call_id":"call_c1","output":"tags"}
	]`)
	var sawGrouped bool
	for _, m := range msgs {
		blocks := parseContentBlocks(m.Content)
		if hasToolUse(blocks, "call_c0") && hasToolUse(blocks, "call_c1") {
			sawGrouped = true
		}
	}
	require.True(t, sawGrouped, "parallel tool_use blocks should share one assistant message")
}

// TestAnthropicPairing_ParallelOneUnanswered supplies parallel calls with only
// one output. Common pairing assertions inspect the repaired turns, and every
// emitted message must exclude the unanswered call ID.
func TestAnthropicPairing_ParallelOneUnanswered(t *testing.T) {
	msgs := convertAnthropic(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]},
		{"type":"function_call","call_id":"call_A","name":"exec","arguments":"{}"},
		{"type":"function_call","call_id":"call_B","name":"exec","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_A","output":"oa"}
	]`)
	for _, m := range msgs {
		require.Falsef(t, hasToolUse(parseContentBlocks(m.Content), "call_B"),
			"unanswered tool_use call_B should have been dropped")
	}
}

// TestAnthropicPairing_OrphanToolResultDropped supplies a result whose call is
// absent. Common pairing checks inspect the conversion, and all emitted
// messages must exclude that orphan result ID.
func TestAnthropicPairing_OrphanToolResultDropped(t *testing.T) {
	msgs := convertAnthropic(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]},
		{"type":"function_call_output","call_id":"call_ghost","output":"orphan"}
	]`)
	for _, m := range msgs {
		require.Falsef(t, hasToolResult(parseContentBlocks(m.Content), "call_ghost"),
			"orphan tool_result should have been dropped")
	}
}

// TestAnthropicPairing_DanglingCallDropped supplies an unanswered call after
// user text. The converted turns undergo common pairing checks and must
// contain no tool-use block for that call.
func TestAnthropicPairing_DanglingCallDropped(t *testing.T) {
	msgs := convertAnthropic(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]},
		{"type":"function_call","call_id":"call_A","name":"exec","arguments":"{}"}
	]`)
	for _, m := range msgs {
		require.Falsef(t, hasToolUse(parseContentBlocks(m.Content), "call_A"),
			"dangling tool_use call_A should have been dropped")
	}
}

// TestAnthropicPairing_SingleCall supplies user text, a call/result pair, and
// final assistant text. It checks at least four turns and the leading user,
// call, and correlated result positions.
func TestAnthropicPairing_SingleCall(t *testing.T) {
	msgs := convertAnthropic(t, `[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"latest sha?"}]},
		{"type":"function_call","call_id":"call_A","name":"exec","arguments":"{\"cmd\":\"git rev-parse HEAD\"}"},
		{"type":"function_call_output","call_id":"call_A","output":"deadbeef"},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"It is deadbeef."}]}
	]`)
	// Pairing repair is checked again through leading turn positions here.
	// The final text only contributes to the minimum length assertion below.
	require.GreaterOrEqual(t, len(msgs), 4)
	require.Equal(t, "user", msgs[0].Role)
	require.True(t, hasToolUse(parseContentBlocks(msgs[1].Content), "call_A"))
	require.True(t, hasToolResult(parseContentBlocks(msgs[2].Content), "call_A"))
}

// TestResponsesToAnthropic_FunctionOutputContentArray returns text and an
// inline PNG inside one function output. Assertions check two turns, one
// result wrapper, ordered text/image blocks, and the image's retained
// MIME/data.
func TestResponsesToAnthropic_FunctionOutputContentArray(t *testing.T) {
	msgs := convertAnthropic(t, `[
		{"type":"function_call","call_id":"call_A","name":"view_image","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_A","output":[
			{"type":"input_text","text":"image loaded"},
			{"type":"input_image","image_url":"data:image/png;base64,YQ=="}
		]}
	]`)

	require.Len(t, msgs, 2)
	resultBlocks := parseContentBlocks(msgs[1].Content)
	require.Len(t, resultBlocks, 1)
	require.Equal(t, "tool_result", resultBlocks[0].Type)

	var content []AnthropicContentBlock
	require.NoError(t, json.Unmarshal(resultBlocks[0].Content, &content))
	require.Len(t, content, 2)
	require.Equal(t, "text", content[0].Type)
	require.Equal(t, "image loaded", content[0].Text)
	require.Equal(t, "image", content[1].Type)
	require.NotNil(t, content[1].Source)
	require.Equal(t, "image/png", content[1].Source.MediaType)
	require.Equal(t, "YQ==", content[1].Source.Data)
}

// TestResponsesToAnthropic_CustomGrammarToolUsesObjectSchema decodes a grammar
// custom declaration with no parameters. Its converted tool keeps
// name/description, uses the empty object schema, and omits inspected
// custom/format/grammar fields from wire JSON.
func TestResponsesToAnthropic_CustomGrammarToolUsesObjectSchema(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.2",
		"input": "apply this patch",
		"tools": [{
			"type": "custom",
			"name": "apply_patch",
			"description": "Apply a patch to the working tree",
			"format": {
				"type": "grammar",
				"syntax": "lark",
				"definition": "start: /.+/"
			}
		}]
	}`)

	var req ResponsesRequest
	require.NoError(t, json.Unmarshal(body, &req))

	anthropicReq, err := ResponsesToAnthropicRequest(&req)
	require.NoError(t, err)
	require.Len(t, anthropicReq.Tools, 1)

	tool := anthropicReq.Tools[0]
	assert.Empty(t, tool.Type)
	assert.Equal(t, "apply_patch", tool.Name)
	assert.Equal(t, "Apply a patch to the working tree", tool.Description)
	requireObjectInputSchema(t, tool.InputSchema)
	assert.JSONEq(t, `{"type":"object","properties":{}}`, string(tool.InputSchema))

	wire, err := json.Marshal(tool)
	require.NoError(t, err)
	assert.NotContains(t, string(wire), `"type":"custom"`)
	assert.NotContains(t, string(wire), `"format"`)
	assert.NotContains(t, string(wire), `"grammar"`)
}

// TestResponsesToAnthropic_CustomToolPreservesSchemaParameters supplies a
// custom tool with object properties and required members. Conversion keeps
// its name and unnamed type, while parsed schema assertions retain those
// supplied constraints.
func TestResponsesToAnthropic_CustomToolPreservesSchemaParameters(t *testing.T) {
	tools := convertResponsesToAnthropicTools([]ResponsesTool{{
		Type:        "custom",
		Name:        "edit_file",
		Description: "Edit a file",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"patch":{"type":"string"}},"required":["patch"]}`),
	}})

	require.Len(t, tools, 1)
	assert.Empty(t, tools[0].Type)
	assert.Equal(t, "edit_file", tools[0].Name)

	schema := requireObjectInputSchema(t, tools[0].InputSchema)
	assert.JSONEq(t, `{"patch":{"type":"string"}}`, string(schema["properties"]))
	assert.JSONEq(t, `["patch"]`, string(schema["required"]))
}

// TestResponsesToAnthropic_FunctionToolSchemaUnchanged supplies a function
// with a complete object schema. Its converted name/description remain, the
// type is empty, and JSON equality checks the retained parameter structure.
func TestResponsesToAnthropic_FunctionToolSchemaUnchanged(t *testing.T) {
	parameters := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)
	tools := convertResponsesToAnthropicTools([]ResponsesTool{{
		Type:        "function",
		Name:        "get_weather",
		Description: "Get weather",
		Parameters:  parameters,
	}})

	require.Len(t, tools, 1)
	assert.Empty(t, tools[0].Type)
	assert.Equal(t, "get_weather", tools[0].Name)
	assert.Equal(t, "Get weather", tools[0].Description)
	assert.JSONEq(t, string(parameters), string(tools[0].InputSchema))
}

// TestResponsesToAnthropic_MixedToolsProduceValidAnthropicTools mixes
// function, custom, and hosted search declarations. Assertions inspect ordered
// names/types and object schemas, then verify that encoded hosted search omits
// input_schema.
func TestResponsesToAnthropic_MixedToolsProduceValidAnthropicTools(t *testing.T) {
	tools := convertResponsesToAnthropicTools([]ResponsesTool{
		{
			Type:       "function",
			Name:       "read_file",
			Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		},
		{
			Type: "custom",
			Name: "apply_patch",
		},
		{
			Type: "web_search",
		},
	})

	require.Len(t, tools, 3)
	assert.Empty(t, tools[0].Type)
	assert.Equal(t, "read_file", tools[0].Name)
	requireObjectInputSchema(t, tools[0].InputSchema)

	assert.Empty(t, tools[1].Type)
	assert.Equal(t, "apply_patch", tools[1].Name)
	assert.JSONEq(t, `{"type":"object","properties":{}}`, string(tools[1].InputSchema))

	assert.Equal(t, "web_search_20250305", tools[2].Type)
	assert.Equal(t, "web_search", tools[2].Name)
	assert.Empty(t, tools[2].InputSchema)
	serverToolWire, err := json.Marshal(tools[2])
	require.NoError(t, err)
	assert.NotContains(t, string(serverToolWire), `"input_schema"`)
}

// TestResponsesToAnthropic_DefaultToolNormalizesInputSchema supplies an
// unfamiliar local_shell declaration without parameters. Its type/name
// survive, and the converted input schema must equal the empty object
// template.
func TestResponsesToAnthropic_DefaultToolNormalizesInputSchema(t *testing.T) {
	tools := convertResponsesToAnthropicTools([]ResponsesTool{{
		Type: "local_shell",
		Name: "shell",
	}})

	require.Len(t, tools, 1)
	assert.Equal(t, "local_shell", tools[0].Type)
	assert.Equal(t, "shell", tools[0].Name)
	assert.JSONEq(t, `{"type":"object","properties":{}}`, string(tools[0].InputSchema))
}

// responsesToAnthropicMessages embeds raw input in one request fixture,
// decodes it, and returns converted messages. Decode or conversion errors fail
// the test; the helper supplies the model used by content-filtering cases.
func responsesToAnthropicMessages(t *testing.T, input string) []AnthropicMessage {
	t.Helper()
	var req ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"glm-5.2","input":`+input+`}`), &req))
	out, err := ResponsesToAnthropicRequest(&req)
	require.NoError(t, err)
	return out.Messages
}

// requireAnthropicMessagesAreSendable fails tests on selected empty
// representations, unsupported block labels, or blank text. Scalar content is
// checked separately from block content; other block fields, correlation, and
// upstream schema requirements are outside this helper's assertions.
func requireAnthropicMessagesAreSendable(t *testing.T, messages []AnthropicMessage) {
	t.Helper()
	for i, m := range messages {
		raw := strings.TrimSpace(string(m.Content))
		require.NotContains(t, []string{"", "null", `""`, "[]"}, raw,
			"messages[%d] 内容为空，Anthropic 拒收空内容消息", i)

		var s string
		if err := json.Unmarshal(m.Content, &s); err == nil {
			require.NotEmpty(t, strings.TrimSpace(s), "messages[%d] 字符串内容不能全为空白", i)
			continue
		}
		blocks := parseContentBlocks(m.Content)
		require.NotEmpty(t, blocks, "messages[%d] 解析不出任何 block", i)
		for j, b := range blocks {
			require.True(t, anthropicInboundBlockTypes[b.Type],
				"messages[%d].content[%d] 是 Anthropic 不认识的 block 类型 %q", i, j, b.Type)
			if b.Type == "text" {
				require.NotEmpty(t, strings.TrimSpace(b.Text),
					"messages[%d].content[%d] 是空白 text 块，Anthropic 拒收", i, j)
			}
		}
	}
}

// mustMarshal encodes the supplied fixture and fails the test on encoding
// error. The returned bytes are used for content-presence assertions,
// including fields spread across converted message blocks.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// assertAnthropicPairing requires different adjacent roles and a neighboring
// counterpart for every decoded tool call/result. It fails the test on missing
// adjacency or mismatched IDs; it does not check duplicate uniqueness or
// validate non-tool block fields.
func assertAnthropicPairing(t *testing.T, messages []AnthropicMessage) {
	t.Helper()
	for i, m := range messages {
		blocks := parseContentBlocks(m.Content)

		if i > 0 {
			require.NotEqualf(t, messages[i-1].Role, m.Role, "consecutive %s messages at %d", m.Role, i)
		}

		for _, b := range blocks {
			switch b.Type {
			case "tool_result":
				require.Positivef(t, i, "tool_result %s has no previous message", b.ToolUseID)
				prev := parseContentBlocks(messages[i-1].Content)
				require.Truef(t, hasToolUse(prev, b.ToolUseID),
					"tool_result %s has no corresponding tool_use in previous message", b.ToolUseID)
			case "tool_use":
				require.Lessf(t, i+1, len(messages), "tool_use %s has no following message", b.ID)
				next := parseContentBlocks(messages[i+1].Content)
				require.Truef(t, hasToolResult(next, b.ID),
					"tool_use %s is not answered in the next message", b.ID)
			}
		}
	}
}

// hasToolUse reports an exact tool_use block with the supplied ID.
func hasToolUse(blocks []AnthropicContentBlock, id string) bool {
	for _, b := range blocks {
		if b.Type == "tool_use" && b.ID == id {
			return true
		}
	}
	return false
}

// hasToolResult reports an exact tool_result block with the supplied
// correlation ID.
func hasToolResult(blocks []AnthropicContentBlock, toolUseID string) bool {
	for _, b := range blocks {
		if b.Type == "tool_result" && b.ToolUseID == toolUseID {
			return true
		}
	}
	return false
}

// convertAnthropic converts raw Responses fixture input without top-level
// instructions and returns Messages turns. Conversion errors fail the test,
// and all results pass assertAnthropicPairing before callers inspect their
// scenario-specific blocks.
func convertAnthropic(t *testing.T, input string) []AnthropicMessage {
	t.Helper()
	_, messages, err := convertResponsesInputToAnthropic("", json.RawMessage(input))
	require.NoError(t, err)
	assertAnthropicPairing(t, messages)
	return messages
}

// requireObjectInputSchema decodes a nonempty fixture schema and checks object
// type plus present, map-decodable properties. Failed checks stop the test;
// remaining schema members are returned for case-specific assertions rather
// than fully validated.
func requireObjectInputSchema(t *testing.T, schema json.RawMessage) map[string]json.RawMessage {
	t.Helper()

	require.NotEmpty(t, schema)

	var parsed map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(schema, &parsed))
	require.JSONEq(t, `"object"`, string(parsed["type"]))
	require.Contains(t, parsed, "properties")

	var properties map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(parsed["properties"], &properties))

	return parsed
}
