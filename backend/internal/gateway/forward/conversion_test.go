package forward

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	protocolbridge "github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
)

// Protocol bridge fixtures preserve content and tool identity across request
// representations while selecting protocol-specific defaults. These tests
// compare converted structures or serialized fields, including effort, media,
// tool-history pairing, and option presence before execution resolves a provider.

// Shared conversion fixtures supply an unsigned thinking/tool turn and the
// output-token floor used by request-budget assertions across both bridge paths.
const (
	// anthropicThinkingToolTurn supplies assistant thinking, text, and a weather call.
	anthropicThinkingToolTurn = `[
	{"type":"thinking","thinking":"user wants weather, call the tool"},
	{"type":"text","text":"checking"},
	{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"SF"}}
]`

	// minMaxOutputTokens is the expected 128-token floor used by protocol bridge assertions.
	minMaxOutputTokens = 128
)

// TestExtractCCReasoningEffortFromBody checks nested and flat Chat effort with
// casing and x-high aliases. The fixtures also retain max for DeepSeek or
// mapped Kimi and return nil when effort is absent.
func TestExtractCCReasoningEffortFromBody(t *testing.T) {
	t.Parallel()

	t.Run("nested reasoning.effort", func(t *testing.T) {
		got := chatEffortFixture([]byte(`{"reasoning":{"effort":"HIGH"}}`))
		require.NotNil(t, got)
		require.Equal(t, "high", *got)
	})

	t.Run("flat reasoning_effort", func(t *testing.T) {
		got := chatEffortFixture([]byte(`{"reasoning_effort":"x-high"}`))
		require.NotNil(t, got)
		require.Equal(t, "xhigh", *got)
	})

	t.Run("DeepSeek V4 preserves max", func(t *testing.T) {
		got := chatEffortFixture([]byte(`{"model":"deepseek-v4-flash","reasoning_effort":"Max"}`))
		require.NotNil(t, got)
		require.Equal(t, "max", *got)
	})

	t.Run("mapped Kimi alias preserves max", func(t *testing.T) {
		got := chatEffortFixture(
			[]byte(`{"model":"public-alias","reasoning_effort":"max"}`),
			"kimi-k3",
			"public-alias",
		)
		require.NotNil(t, got)
		require.Equal(t, "max", *got)
	})

	t.Run("missing effort", func(t *testing.T) {
		require.Nil(t, chatEffortFixture([]byte(`{"model":"gpt-5"}`)))
	})
}

// TestAdaptResponsesClientToolsForAnthropic_FlattensNamespace supplies a
// namespaced declaration and historical function call. Both use the flattened
// function name, the history loses its namespace field, and the restoration
// mapping retains the namespace/name pair.
func TestAdaptResponsesClientToolsForAnthropic_FlattensNamespace(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model":"claude-fable-5",
		"input":[{"type":"function_call","call_id":"call_1","namespace":"codex_app","name":"read_thread","arguments":"{}"}],
		"tools":[{"type":"namespace","name":"codex_app","tools":[{"type":"function","name":"read_thread","description":"Read a task","parameters":{"type":"object","properties":{}}}]}]
	}`)

	adapted, mapping, err := AdaptResponsesClientToolsForAnthropic(body)
	require.NoError(t, err)
	require.Equal(t, protocolbridge.ResponsesNamespaceName{Namespace: "codex_app", Name: "read_thread"}, mapping.NamespaceTools["codex_app__read_thread"])

	var request map[string]any
	require.NoError(t, json.Unmarshal(adapted, &request))
	tools := testassert.MustType[[]any](request["tools"])
	require.Len(t, tools, 1)
	tool := testassert.MustType[map[string]any](tools[0])
	require.Equal(t, "function", tool["type"])
	require.Equal(t, "codex_app__read_thread", tool["name"])

	input := testassert.MustType[[]any](request["input"])
	call := testassert.MustType[map[string]any](input[0])
	require.Equal(t, "codex_app__read_thread", call["name"])
	require.NotContains(t, call, "namespace")
}

// TestAdaptResponsesClientToolsForAnthropic_LiftsAdditionalTools supplies
// custom and namespace tools inside an additional_tools input item. Conversion
// promotes two function declarations, records both restoration identities, and
// leaves only the user message in input.
func TestAdaptResponsesClientToolsForAnthropic_LiftsAdditionalTools(t *testing.T) {
	body := []byte(`{
		"model":"claude-fable-5",
		"input":[
			{"type":"additional_tools","tools":[
				{"type":"custom","name":"exec","description":"Run a command"},
				{"type":"namespace","name":"codex_app","tools":[
					{"type":"function","name":"read_thread","parameters":{"type":"object"}}
				]}
			]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect"}]}
		]
	}`)

	adapted, mapping, err := AdaptResponsesClientToolsForAnthropic(body)
	require.NoError(t, err)
	require.True(t, mapping.CustomTools["exec"])
	require.Equal(t, protocolbridge.ResponsesNamespaceName{Namespace: "codex_app", Name: "read_thread"}, mapping.NamespaceTools["codex_app__read_thread"])

	var request map[string]any
	require.NoError(t, json.Unmarshal(adapted, &request))
	tools := testassert.MustType[[]any](request["tools"])
	require.Len(t, tools, 2)
	require.Equal(t, "function", testassert.MustType[map[string]any](tools[0])["type"])
	require.Equal(t, "codex_app__read_thread", testassert.MustType[map[string]any](tools[1])["name"])

	input := testassert.MustType[[]any](request["input"])
	require.Len(t, input, 1)
	require.Equal(t, "message", testassert.MustType[map[string]any](input[0])["type"])
}

// TestExtractResponsesReasoningEffortFromBody checks nested effort for Claude,
// DeepSeek, and a mapped GLM alias. Casing normalizes, eligible max survives
// model selection, and a request without effort returns nil.
func TestExtractResponsesReasoningEffortFromBody(t *testing.T) {
	t.Parallel()

	got := responsesEffortFixture([]byte(`{"model":"claude-sonnet-4.5","reasoning":{"effort":"HIGH"}}`))
	require.NotNil(t, got)
	require.Equal(t, "high", *got)

	maxGot := responsesEffortFixture([]byte(`{"model":"deepseek-v4-pro","reasoning":{"effort":"max"}}`))
	require.NotNil(t, maxGot)
	require.Equal(t, "max", *maxGot)

	mappedMax := responsesEffortFixture(
		[]byte(`{"model":"public-alias","reasoning":{"effort":"max"}}`),
		"provider/glm-5.2",
		"public-alias",
	)
	require.NotNil(t, mappedMax)
	require.Equal(t, "max", *mappedMax)

	require.Nil(t, responsesEffortFixture([]byte(`{"model":"claude-sonnet-4.5"}`)))
}

// TestAnthropicToResponses_BasicText converts an Anthropic request with user
// text, streaming enabled, and an output budget. Model, stream, budget, disabled
// storage, role, and the
// resulting input_text content match the supplied fixture.
func TestAnthropicToResponses_BasicText(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.2",
		MaxTokens: 1024,
		Stream:    true,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"Hello"`)},
		},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)
	assert.Equal(t, "gpt-5.2", resp.Model)
	assert.True(t, resp.Stream)
	assert.Equal(t, 1024, *resp.MaxOutputTokens)
	assert.False(t, *resp.Store)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	require.Len(t, items, 1)
	assert.Equal(t, "message", items[0].Type)
	assert.Equal(t, "user", items[0].Role)
	var parts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[0].Content, &parts))
	require.Len(t, parts, 1)
	assert.Equal(t, "input_text", parts[0].Type)
	assert.Equal(t, "Hello", parts[0].Text)
}

// TestAnthropicToResponses_SystemPrompt covers string and array system prompts
// plus a billing-header block. Prompt text becomes developer content in order,
// while the billing block is excluded from the asserted prompt parts.
func TestAnthropicToResponses_SystemPrompt(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		req := &protocolanthropic.AnthropicRequest{
			Model:     "gpt-5.2",
			MaxTokens: 100,
			System:    json.RawMessage(`"You are helpful."`),
			Messages:  []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hi"`)}},
		}
		resp, err := AnthropicToResponses(req)
		require.NoError(t, err)

		var items []protocolopenai.ResponsesInputItem
		require.NoError(t, json.Unmarshal(resp.Input, &items))
		require.Len(t, items, 2)
		assert.Equal(t, "developer", items[0].Role)
		var parts []protocolopenai.ResponsesContentPart
		require.NoError(t, json.Unmarshal(items[0].Content, &parts))
		require.Len(t, parts, 1)
		assert.Equal(t, "input_text", parts[0].Type)
		assert.Equal(t, "You are helpful.", parts[0].Text)
	})

	t.Run("array", func(t *testing.T) {
		req := &protocolanthropic.AnthropicRequest{
			Model:     "gpt-5.2",
			MaxTokens: 100,
			System:    json.RawMessage(`[{"type":"text","text":"Part 1"},{"type":"text","text":"Part 2"}]`),
			Messages:  []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hi"`)}},
		}
		resp, err := AnthropicToResponses(req)
		require.NoError(t, err)

		var items []protocolopenai.ResponsesInputItem
		require.NoError(t, json.Unmarshal(resp.Input, &items))
		require.Len(t, items, 2)
		assert.Equal(t, "developer", items[0].Role)
		var parts []protocolopenai.ResponsesContentPart
		require.NoError(t, json.Unmarshal(items[0].Content, &parts))
		require.Len(t, parts, 2)
		assert.Equal(t, "input_text", parts[0].Type)
		assert.Equal(t, "Part 1", parts[0].Text)
		assert.Equal(t, "input_text", parts[1].Type)
		assert.Equal(t, "Part 2", parts[1].Text)
	})

	t.Run("billing header skipped", func(t *testing.T) {
		req := &protocolanthropic.AnthropicRequest{
			Model:     "gpt-5.2",
			MaxTokens: 100,
			System:    json.RawMessage(`[{"type":"text","text":"x-anthropic-billing-header: cc_version=1;"},{"type":"text","text":"Project prompt"}]`),
			Messages:  []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hi"`)}},
		}
		resp, err := AnthropicToResponses(req)
		require.NoError(t, err)

		var items []protocolopenai.ResponsesInputItem
		require.NoError(t, json.Unmarshal(resp.Input, &items))
		require.Len(t, items, 2)
		var parts []protocolopenai.ResponsesContentPart
		require.NoError(t, json.Unmarshal(items[0].Content, &parts))
		require.Len(t, parts, 1)
		assert.Equal(t, "Project prompt", parts[0].Text)
	})
}

// TestAnthropicToResponses_ToolUse converts an assistant tool call and matching
// user result. The function declaration has explicit false strictness, and
// historical call/result items share call_1 with no fabricated item ID.
func TestAnthropicToResponses_ToolUse(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.2",
		MaxTokens: 1024,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"What is the weather?"`)},
			{Role: "assistant", Content: json.RawMessage(`[{"type":"text","text":"Let me check."},{"type":"tool_use","id":"call_1","name":"get_weather","input":{"city":"NYC"}}]`)},
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"call_1","content":"Sunny, 72°F"}]`)},
		},
		Tools: []protocolanthropic.AnthropicTool{
			{Name: "get_weather", Description: "Get weather", InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`)},
		},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)

	require.Len(t, resp.Tools, 1)
	assert.Equal(t, "function", resp.Tools[0].Type)
	assert.Equal(t, "get_weather", resp.Tools[0].Name)
	require.NotNil(t, resp.Tools[0].Strict)
	assert.False(t, *resp.Tools[0].Strict)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	// user + assistant + function_call + function_call_output = 4
	require.Len(t, items, 4)

	assert.Equal(t, "user", items[0].Role)
	assert.Equal(t, "assistant", items[1].Role)
	assert.Equal(t, "function_call", items[2].Type)
	assert.Equal(t, "call_1", items[2].CallID)
	assert.Empty(t, items[2].ID)
	assert.Equal(t, "function_call_output", items[3].Type)
	assert.Equal(t, "call_1", items[3].CallID)
	assert.Equal(t, "Sunny, 72°F", items[3].Output)
}

// TestAnthropicToResponses_ThinkingWithoutSignatureIgnored places unsigned
// thinking beside assistant text between user turns. Conversion retains three
// messages and emits only the asserted text part in the assistant content.
func TestAnthropicToResponses_ThinkingWithoutSignatureIgnored(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.2",
		MaxTokens: 1024,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"Hello"`)},
			{Role: "assistant", Content: json.RawMessage(`[{"type":"thinking","thinking":"deep thought"},{"type":"text","text":"Hi!"}]`)},
			{Role: "user", Content: json.RawMessage(`"More"`)},
		},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	// Three messages remain: user, assistant text with unsigned thinking omitted, then user.
	require.Len(t, items, 3)
	assert.Equal(t, "assistant", items[1].Role)
	// Unsigned thinking does not become assistant content.
	var parts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[1].Content, &parts))
	require.Len(t, parts, 1)
	assert.Equal(t, "output_text", parts[0].Type)
	assert.Equal(t, "Hi!", parts[0].Text)
}

// TestAnthropicToResponses_ThinkingSignatureBecomesReasoning supplies signed
// thinking before text and a tool call. The supplied signature becomes the
// encrypted-content field in a preceding reasoning item, with assistant and
// function-call items occupying the asserted following positions.
func TestAnthropicToResponses_ThinkingSignatureBecomesReasoning(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "grok-4.5",
		MaxTokens: 1024,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"Hello"`)},
			{Role: "assistant", Content: json.RawMessage(`[{"type":"thinking","thinking":"plan","signature":"enc-rs-1"},{"type":"text","text":"Hi!"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]`)},
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]`)},
		},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	// The sequence includes user, reasoning, assistant text, function call, and call result.
	require.GreaterOrEqual(t, len(items), 4)
	assert.Equal(t, "reasoning", items[1].Type)
	assert.Equal(t, "enc-rs-1", items[1].EncryptedContent)
	assert.Equal(t, "assistant", items[2].Role)
	assert.Equal(t, "function_call", items[3].Type)
}

// TestAnthropicToResponses_MaxTokensFloor uses an output budget below the
// bridge minimum. The converted Responses request raises that supplied budget
// to the asserted 128-token floor for this fixture.
func TestAnthropicToResponses_MaxTokensFloor(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.2",
		MaxTokens: 10, // supplied budget below the enforced 128-token floor
		Messages:  []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hi"`)}},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)
	assert.Equal(t, 128, *resp.MaxOutputTokens)
}

// TestAnthropicToResponses_ThinkingEnabled supplies enabled thinking and a
// token budget without output effort. Recorded effort is medium with auto
// summary, encrypted-content inclusion, and no reasoning.summary inclusion in
// this fixture.
func TestAnthropicToResponses_ThinkingEnabled(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.2",
		MaxTokens: 1024,
		Messages:  []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
		Thinking:  &protocolanthropic.AnthropicThinking{Type: "enabled", BudgetTokens: 10000},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)
	require.NotNil(t, resp.Reasoning)
	// thinking.type is ignored for effort; Codex bridge default medium applies.
	assert.Equal(t, "medium", resp.Reasoning.Effort)
	assert.Equal(t, "auto", resp.Reasoning.Summary)
	assert.Contains(t, resp.Include, "reasoning.encrypted_content")
	assert.NotContains(t, resp.Include, "reasoning.summary")
}

// TestAnthropicToResponses_ThinkingAdaptive supplies adaptive thinking without
// a separate output effort. The converted request records medium and auto
// summary while omitting reasoning.summary from its inclusion list.
func TestAnthropicToResponses_ThinkingAdaptive(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.2",
		MaxTokens: 1024,
		Messages:  []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
		Thinking:  &protocolanthropic.AnthropicThinking{Type: "adaptive", BudgetTokens: 5000},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)
	require.NotNil(t, resp.Reasoning)
	// thinking.type is ignored for effort; Codex bridge default medium applies.
	assert.Equal(t, "medium", resp.Reasoning.Effort)
	assert.Equal(t, "auto", resp.Reasoning.Summary)
	assert.NotContains(t, resp.Include, "reasoning.summary")
}

// TestAnthropicToResponses_ThinkingDisabled supplies disabled thinking without
// output effort for the GPT fixture. A nonnil reasoning configuration still
// uses the bridge default of medium in the converted request.
func TestAnthropicToResponses_ThinkingDisabled(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.2",
		MaxTokens: 1024,
		Messages:  []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
		Thinking:  &protocolanthropic.AnthropicThinking{Type: "disabled"},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)
	// Default effort applies (medium) even when thinking is disabled.
	require.NotNil(t, resp.Reasoning)
	assert.Equal(t, "medium", resp.Reasoning.Effort)
}

// TestAnthropicToResponses_NoThinking omits both thinking and output
// configuration from the GPT fixture. The resulting reasoning configuration
// remains present and records medium effort after successful conversion.
func TestAnthropicToResponses_NoThinking(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.2",
		MaxTokens: 1024,
		Messages:  []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)
	// Default effort applies (medium) when no thinking/output_config is set.
	require.NotNil(t, resp.Reasoning)
	assert.Equal(t, "medium", resp.Reasoning.Effort)
}

// TestAnthropicToResponses_OutputConfigOverridesDefault combines enabled
// thinking with low output effort. Conversion uses low rather than the medium
// default and retains auto summary in the resulting reasoning configuration.
func TestAnthropicToResponses_OutputConfigOverridesDefault(t *testing.T) {
	// output_config.effort="low" overrides the default medium effort.
	req := &protocolanthropic.AnthropicRequest{
		Model:        "gpt-5.2",
		MaxTokens:    1024,
		Messages:     []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
		Thinking:     &protocolanthropic.AnthropicThinking{Type: "enabled", BudgetTokens: 10000},
		OutputConfig: &protocolanthropic.AnthropicOutputConfig{Effort: "low"},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)
	require.NotNil(t, resp.Reasoning)
	assert.Equal(t, "low", resp.Reasoning.Effort)
	assert.Equal(t, "auto", resp.Reasoning.Summary)
}

// TestAnthropicToResponses_OutputConfigWithoutThinking provides medium output
// effort with no thinking configuration. Conversion still creates reasoning,
// retaining medium effort and auto summary for the supplied GPT request.
func TestAnthropicToResponses_OutputConfigWithoutThinking(t *testing.T) {
	// output_config.effort creates reasoning even when thinking is absent.
	// The medium value passes through unchanged.
	req := &protocolanthropic.AnthropicRequest{
		Model:        "gpt-5.2",
		MaxTokens:    1024,
		Messages:     []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
		OutputConfig: &protocolanthropic.AnthropicOutputConfig{Effort: "medium"},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)
	require.NotNil(t, resp.Reasoning)
	assert.Equal(t, "medium", resp.Reasoning.Effort)
	assert.Equal(t, "auto", resp.Reasoning.Summary)
}

// TestAnthropicToResponses_OutputConfigHigh provides high Anthropic output
// effort without thinking configuration. The converted Responses request
// retains high and auto summary after conversion succeeds for the GPT fixture.
func TestAnthropicToResponses_OutputConfigHigh(t *testing.T) {
	// The high effort value passes through unchanged.
	req := &protocolanthropic.AnthropicRequest{
		Model:        "gpt-5.2",
		MaxTokens:    1024,
		Messages:     []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
		OutputConfig: &protocolanthropic.AnthropicOutputConfig{Effort: "high"},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)
	require.NotNil(t, resp.Reasoning)
	assert.Equal(t, "high", resp.Reasoning.Effort)
	assert.Equal(t, "auto", resp.Reasoning.Summary)
}

// TestAnthropicToResponses_OutputConfigMax supplies max output effort on the
// GPT-5.2 input. Intermediate conversion retains max and auto summary, without
// exercising later execution-time target capability filtering.
func TestAnthropicToResponses_OutputConfigMax(t *testing.T) {
	// Retain client effort until final route capabilities are available.
	req := &protocolanthropic.AnthropicRequest{
		Model:        "gpt-5.2",
		MaxTokens:    1024,
		Messages:     []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
		OutputConfig: &protocolanthropic.AnthropicOutputConfig{Effort: "max"},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)
	require.NotNil(t, resp.Reasoning)
	assert.Equal(t, "max", resp.Reasoning.Effort)
	assert.Equal(t, "auto", resp.Reasoning.Summary)
}

// TestAnthropicToResponses_OutputConfigMaxForGPT56 supplies max effort on the
// GPT-5.6-sol input. The converted Responses reasoning retains max and auto
// summary at this intermediate conversion stage.
func TestAnthropicToResponses_OutputConfigMaxForGPT56(t *testing.T) {
	// Retain max effort for GPT-5.6.
	req := &protocolanthropic.AnthropicRequest{
		Model:        "gpt-5.6-sol",
		MaxTokens:    1024,
		Messages:     []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
		OutputConfig: &protocolanthropic.AnthropicOutputConfig{Effort: "max"},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)
	require.NotNil(t, resp.Reasoning)
	assert.Equal(t, "max", resp.Reasoning.Effort)
	assert.Equal(t, "auto", resp.Reasoning.Summary)
}

// TestAnthropicToResponses_RejectsUltraReasoningEffort uses ultra output effort
// with four selected GPT model strings. Every fixture returns an
// unsupported-effort error and a nil converted Responses request.
func TestAnthropicToResponses_RejectsUltraReasoningEffort(t *testing.T) {
	for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5"} {
		t.Run(model, func(t *testing.T) {
			req := &protocolanthropic.AnthropicRequest{
				Model:        model,
				MaxTokens:    1024,
				Messages:     []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
				OutputConfig: &protocolanthropic.AnthropicOutputConfig{Effort: "ultra"},
			}

			resp, err := AnthropicToResponses(req)
			require.ErrorContains(t, err, "not supported")
			require.Nil(t, resp)
		})
	}
}

// TestAnthropicToResponses_NoOutputConfig uses enabled thinking while omitting
// output_config entirely. The resulting nonnil reasoning configuration records
// medium, isolating the default used by that supplied fixture.
func TestAnthropicToResponses_NoOutputConfig(t *testing.T) {
	// Enabled thinking without output_config selects medium in this fixture.
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.2",
		MaxTokens: 1024,
		Messages:  []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
		Thinking:  &protocolanthropic.AnthropicThinking{Type: "enabled", BudgetTokens: 10000},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)
	require.NotNil(t, resp.Reasoning)
	assert.Equal(t, "medium", resp.Reasoning.Effort)
}

// TestAnthropicToResponses_OutputConfigWithoutEffort supplies an empty
// Anthropic output configuration. The converted request still has reasoning and
// selects medium for the omitted effort in this GPT fixture.
func TestAnthropicToResponses_OutputConfigWithoutEffort(t *testing.T) {
	// An empty output_config effort uses the medium default.
	req := &protocolanthropic.AnthropicRequest{
		Model:        "gpt-5.2",
		MaxTokens:    1024,
		Messages:     []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
		OutputConfig: &protocolanthropic.AnthropicOutputConfig{},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)
	require.NotNil(t, resp.Reasoning)
	assert.Equal(t, "medium", resp.Reasoning.Effort)
}

// TestAnthropicToResponses_ToolChoiceAuto converts an automatic Anthropic tool
// selector for Responses. The encoded selection decodes as the string auto,
// establishing the target selector shape for this input.
func TestAnthropicToResponses_ToolChoiceAuto(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:      "gpt-5.2",
		MaxTokens:  1024,
		Messages:   []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
		ToolChoice: json.RawMessage(`{"type":"auto"}`),
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)

	var tc string
	require.NoError(t, json.Unmarshal(resp.ToolChoice, &tc))
	assert.Equal(t, "auto", tc)
}

// TestAnthropicToResponses_ToolChoiceAny converts the Anthropic any tool
// selector. The encoded Responses selector decodes as required, retaining
// mandatory tool selection through the protocol name change.
func TestAnthropicToResponses_ToolChoiceAny(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:      "gpt-5.2",
		MaxTokens:  1024,
		Messages:   []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
		ToolChoice: json.RawMessage(`{"type":"any"}`),
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)

	var tc string
	require.NoError(t, json.Unmarshal(resp.ToolChoice, &tc))
	assert.Equal(t, "required", tc)
}

// TestAnthropicToResponses_ToolChoiceSpecific supplies a named Anthropic tool
// selector. Responses uses a function/name object with the selected weather
// name and no nested function wrapper in the asserted result.
func TestAnthropicToResponses_ToolChoiceSpecific(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:      "gpt-5.2",
		MaxTokens:  1024,
		Messages:   []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
		ToolChoice: json.RawMessage(`{"type":"tool","name":"get_weather"}`),
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)

	var tc map[string]any
	require.NoError(t, json.Unmarshal(resp.ToolChoice, &tc))
	assert.Equal(t, "function", tc["type"])
	assert.Equal(t, "get_weather", tc["name"])
	assert.NotContains(t, tc, "function")
}

// TestAnthropicToResponses_UserImageBlock combines user text with a base64 PNG
// block. Conversion retains both parts in order and constructs the expected
// data URL for the image content.
func TestAnthropicToResponses_UserImageBlock(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.2",
		MaxTokens: 1024,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`[
				{"type":"text","text":"What is in this image?"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR"}}
			]`)},
		},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	require.Len(t, items, 1)
	assert.Equal(t, "user", items[0].Role)

	var parts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[0].Content, &parts))
	require.Len(t, parts, 2)
	assert.Equal(t, "input_text", parts[0].Type)
	assert.Equal(t, "What is in this image?", parts[0].Text)
	assert.Equal(t, "input_image", parts[1].Type)
	assert.Equal(t, "data:image/png;base64,iVBOR", parts[1].ImageURL)
}

// TestAnthropicToResponses_ImageOnlyUserMessage supplies one base64 JPEG
// without user text. Conversion keeps a single user input image with its
// expected JPEG data URL and does not add a text part.
func TestAnthropicToResponses_ImageOnlyUserMessage(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.2",
		MaxTokens: 1024,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`[
				{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"/9j/4AAQ"}}
			]`)},
		},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	require.Len(t, items, 1)

	var parts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[0].Content, &parts))
	require.Len(t, parts, 1)
	assert.Equal(t, "input_image", parts[0].Type)
	assert.Equal(t, "data:image/jpeg;base64,/9j/4AAQ", parts[0].ImageURL)
}

// TestAnthropicToResponses_ToolResultWithImage supplies an image-only result
// matching an assistant tool call. The function result uses the empty
// placeholder, and a following user message carries the asserted PNG
// input_image.
func TestAnthropicToResponses_ToolResultWithImage(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.2",
		MaxTokens: 1024,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"Read the screenshot"`)},
			{Role: "assistant", Content: json.RawMessage(`[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/tmp/screen.png"}}]`)},
			{Role: "user", Content: json.RawMessage(`[
				{"type":"tool_result","tool_use_id":"toolu_1","content":[
					{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR"}}
				]}
			]`)},
		},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	// user + function_call + function_call_output + user(image) = 4
	require.Len(t, items, 4)

	// The image-only result uses a string placeholder before the separate user image.
	assert.Equal(t, "function_call_output", items[2].Type)
	assert.Equal(t, "toolu_1", items[2].CallID)
	assert.Equal(t, "(empty)", items[2].Output)

	// The following user message carries the extracted input image.
	assert.Equal(t, "user", items[3].Role)
	var parts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[3].Content, &parts))
	require.Len(t, parts, 1)
	assert.Equal(t, "input_image", parts[0].Type)
	assert.Equal(t, "data:image/png;base64,iVBOR", parts[0].ImageURL)
}

// TestAnthropicToResponses_ToolResultMixed combines metadata text and a PNG
// inside one tool result. Converted function output retains the text while a
// following user image part carries the expected data URL.
func TestAnthropicToResponses_ToolResultMixed(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.2",
		MaxTokens: 1024,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"Describe the file"`)},
			{Role: "assistant", Content: json.RawMessage(`[{"type":"tool_use","id":"toolu_2","name":"Read","input":{"file_path":"/tmp/photo.png"}}]`)},
			{Role: "user", Content: json.RawMessage(`[
				{"type":"tool_result","tool_use_id":"toolu_2","content":[
					{"type":"text","text":"File metadata: 800x600 PNG"},
					{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}
				]}
			]`)},
		},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	// user + function_call + function_call_output + user(image) = 4
	require.Len(t, items, 4)

	// The function result retains its text before the separate user image.
	assert.Equal(t, "function_call_output", items[2].Type)
	assert.Equal(t, "File metadata: 800x600 PNG", items[2].Output)

	// The following user message carries the extracted input image.
	assert.Equal(t, "user", items[3].Role)
	var parts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[3].Content, &parts))
	require.Len(t, parts, 1)
	assert.Equal(t, "input_image", parts[0].Type)
	assert.Equal(t, "data:image/png;base64,AAAA", parts[0].ImageURL)
}

// TestAnthropicToResponses_TextOnlyToolResultBackwardCompat uses an
// array-valued tool result containing one text block. Conversion retains three
// history items and produces the expected plain string as the matching function
// output.
func TestAnthropicToResponses_TextOnlyToolResultBackwardCompat(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.2",
		MaxTokens: 1024,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"Check weather"`)},
			{Role: "assistant", Content: json.RawMessage(`[{"type":"tool_use","id":"call_1","name":"get_weather","input":{"city":"NYC"}}]`)},
			{Role: "user", Content: json.RawMessage(`[
				{"type":"tool_result","tool_use_id":"call_1","content":[
					{"type":"text","text":"Sunny, 72°F"}
				]}
			]`)},
		},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	// user + function_call + function_call_output = 3
	require.Len(t, items, 3)

	// Text-only tool_result should produce a plain string.
	assert.Equal(t, "Sunny, 72°F", items[2].Output)
}

// TestAnthropicToResponses_ImageEmptyMediaType supplies base64 image bytes with
// an empty media type. The single converted input_image uses the asserted
// image/png data-URL default for this source block.
func TestAnthropicToResponses_ImageEmptyMediaType(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.2",
		MaxTokens: 1024,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`[
				{"type":"image","source":{"type":"base64","media_type":"","data":"iVBOR"}}
			]`)},
		},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	require.Len(t, items, 1)

	var parts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[0].Content, &parts))
	require.Len(t, parts, 1)
	assert.Equal(t, "input_image", parts[0].Type)
	// Missing media type selects PNG for the generated data URL.
	assert.Equal(t, "data:image/png;base64,iVBOR", parts[0].ImageURL)
}

// TestAnthropicToResponses_ToolWithoutProperties declares an object-schema
// function without a properties member. Conversion retains its function/name
// identity and adds the properties field checked in the encoded parameters.
func TestAnthropicToResponses_ToolWithoutProperties(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.2",
		MaxTokens: 1024,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"Hello"`)},
		},
		Tools: []protocolanthropic.AnthropicTool{
			{Name: "mcp__pencil__get_style_guide_tags", Description: "Get style tags", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)

	require.Len(t, resp.Tools, 1)
	assert.Equal(t, "function", resp.Tools[0].Type)
	assert.Equal(t, "mcp__pencil__get_style_guide_tags", resp.Tools[0].Name)

	// Parameters must have "properties" field after normalization.
	var params map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(resp.Tools[0].Parameters, &params))
	assert.Contains(t, params, "properties")
}

// TestAnthropicToResponses_ToolWithNilSchema declares a tool without an input
// schema. The converted function has an object parameter type and empty
// properties, establishing the shape used for this missing-schema fixture.
func TestAnthropicToResponses_ToolWithNilSchema(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.2",
		MaxTokens: 1024,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"Hello"`)},
		},
		Tools: []protocolanthropic.AnthropicTool{
			{Name: "simple_tool", Description: "A tool"},
		},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)

	require.Len(t, resp.Tools, 1)
	var params map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(resp.Tools[0].Parameters, &params))
	assert.JSONEq(t, `"object"`, string(params["type"]))
	assert.JSONEq(t, `{}`, string(params["properties"]))
}

// TestAnthropicToResponses_SamplingPreservedBeforeResolution supplies
// temperature and top_p for a GPT request. Both values remain in the converted
// structure and serialized JSON, before any later route-specific parameter
// filtering.
func TestAnthropicToResponses_SamplingPreservedBeforeResolution(t *testing.T) {
	temp := 0.7
	req := &protocolanthropic.AnthropicRequest{
		Model:       "gpt-5.2",
		MaxTokens:   1024,
		Messages:    []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
		Temperature: &temp,
		TopP:        &temp,
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)
	assert.Equal(t, req.Temperature, resp.Temperature)
	assert.Equal(t, req.TopP, resp.TopP)

	// Route-specific parameter filtering runs after conversion.
	b, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"temperature"`)
	assert.Contains(t, string(b), `"top_p"`)
}

// TestAnthropicToResponses_SamplingDoesNotGuessModelFamily runs sampling inputs
// across five selected GPT model strings. Conversion retains the supplied
// temperature and top_p for every fixture without testing execution-time model
// attributes.
func TestAnthropicToResponses_SamplingDoesNotGuessModelFamily(t *testing.T) {
	temp := 1.0
	models := []string{"gpt-5.2", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex", "gpt-5.5"}
	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			req := &protocolanthropic.AnthropicRequest{
				Model:       model,
				MaxTokens:   1024,
				Messages:    []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hello"`)}},
				Temperature: &temp,
				TopP:        &temp,
			}
			resp, err := AnthropicToResponses(req)
			require.NoError(t, err)
			assert.Equal(t, req.Temperature, resp.Temperature)
			assert.Equal(t, req.TopP, resp.TopP)
		})
	}
}

// TestAnthropicToChatCompletionsRequest_BasicText converts a single Anthropic
// user string into Chat. The supplied model, user role/content, and 1024-token
// completion budget remain in the asserted request fields.
func TestAnthropicToChatCompletionsRequest_BasicText(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"hello"`)},
		},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-20250514", out.Model)
	require.Len(t, out.Messages, 1)
	require.Equal(t, "user", out.Messages[0].Role)
	require.Equal(t, `"hello"`, string(out.Messages[0].Content))
	require.NotNil(t, out.MaxCompletionTokens)
	require.Equal(t, 1024, *out.MaxCompletionTokens)
}

// TestAnthropicToChatCompletionsRequest_SystemPrompt supplies an Anthropic
// system string before a user turn. The Chat request contains two messages and
// retains that string in the leading system-role message.
func TestAnthropicToChatCompletionsRequest_SystemPrompt(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 100,
		System:    json.RawMessage(`"You are helpful"`),
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Len(t, out.Messages, 2)
	require.Equal(t, "system", out.Messages[0].Role)
	require.Equal(t, `"You are helpful"`, string(out.Messages[0].Content))
}

// TestAnthropicToChatCompletionsRequest_ToolUseInAssistant supplies assistant
// text and a weather tool call with its result. The surviving assistant tool
// call retains its ID, function type, name, and encoded city argument.
func TestAnthropicToChatCompletionsRequest_ToolUseInAssistant(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 100,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"check weather"`)},
			{Role: "assistant", Content: json.RawMessage(`[{"type":"text","text":"Let me check."},{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"SF"}}]`)},
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"toolu_1","content":"sunny"}]`)},
		},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	// Locate the assistant call; this length assertion alone does not establish all message positions.
	require.GreaterOrEqual(t, len(out.Messages), 2)

	var assistant *protocolopenai.ChatMessage
	for i := range out.Messages {
		if out.Messages[i].Role == "assistant" && len(out.Messages[i].ToolCalls) > 0 {
			assistant = &out.Messages[i]
		}
	}
	require.NotNil(t, assistant, "assistant message with tool_calls should survive normalization")
	require.Len(t, assistant.ToolCalls, 1)
	require.Equal(t, "toolu_1", assistant.ToolCalls[0].ID)
	require.Equal(t, "function", assistant.ToolCalls[0].Type)
	require.Equal(t, "get_weather", assistant.ToolCalls[0].Function.Name)
	require.Equal(t, `{"city":"SF"}`, assistant.ToolCalls[0].Function.Arguments)
}

// TestAnthropicToChatCompletionsRequest_ToolResultBecomesToolMessage supplies a
// weather call followed by its Anthropic result. The converted Chat tool-role
// message retains the matching tool-call ID and expected quoted result text.
func TestAnthropicToChatCompletionsRequest_ToolResultBecomesToolMessage(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 100,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"check weather"`)},
			{Role: "assistant", Content: json.RawMessage(`[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"SF"}}]`)},
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"toolu_1","content":"sunny, 72F"}]`)},
		},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)

	var toolMsg *protocolopenai.ChatMessage
	for i := range out.Messages {
		if out.Messages[i].Role == "tool" {
			toolMsg = &out.Messages[i]
		}
	}
	require.NotNil(t, toolMsg, "tool_result should become a tool role message")
	require.Equal(t, "toolu_1", toolMsg.ToolCallID)
	require.Equal(t, `"sunny, 72F"`, string(toolMsg.Content))
}

// TestAnthropicToChatCompletionsRequest_ThinkingDropped uses an assistant turn
// containing thinking and text but no tool call. The single Chat message
// retains answer text and has empty reasoning_content in this fixture.
func TestAnthropicToChatCompletionsRequest_ThinkingDropped(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 100,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "assistant", Content: json.RawMessage(`[{"type":"thinking","thinking":"secret thoughts"},{"type":"text","text":"answer"}]`)},
		},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Len(t, out.Messages, 1)
	// A text-only turn drops thinking from the Chat payload.
	require.Equal(t, `"answer"`, string(out.Messages[0].Content))
	require.Empty(t, out.Messages[0].ReasoningContent)
}

// TestAnthropicToChatCompletionsRequest_ToolChoiceAuto declares a weather
// function with automatic Anthropic selection. The Chat request retains one
// tool, emits the auto selector string, and enables parallel tool calls.
func TestAnthropicToChatCompletionsRequest_ToolChoiceAuto(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 100,
		Tools: []protocolanthropic.AnthropicTool{
			{Name: "get_weather", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		},
		ToolChoice: json.RawMessage(`{"type":"auto"}`),
		Messages:   []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Len(t, out.Tools, 1)
	require.Equal(t, `"auto"`, string(out.ToolChoice))
	require.NotNil(t, out.ParallelToolCalls)
	require.True(t, *out.ParallelToolCalls)
}

// TestAnthropicToChatCompletionsRequest_ParallelToolChoiceMapping checks
// omitted, false, and true disable_parallel_tool_use inputs. Every Chat result
// supplies a nonnil parallel flag, with the disable input inverted for the
// explicit cases.
func TestAnthropicToChatCompletionsRequest_ParallelToolChoiceMapping(t *testing.T) {
	tests := []struct {
		name       string          // parallel-flag case label
		toolChoice json.RawMessage // source disable-parallel selector
		want       bool            // expected Chat parallelism
	}{
		{name: "省略时默认启用", want: true},
		{name: "显式 false 时启用", toolChoice: json.RawMessage(`{"type":"auto","disable_parallel_tool_use":false}`), want: true},
		{name: "显式 true 时禁用", toolChoice: json.RawMessage(`{"type":"auto","disable_parallel_tool_use":true}`), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &protocolanthropic.AnthropicRequest{
				Model:     "claude-sonnet-4-20250514",
				MaxTokens: 100,
				Tools: []protocolanthropic.AnthropicTool{
					{Name: "get_weather", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
				},
				ToolChoice: tt.toolChoice,
				Messages:   []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
			}

			out, err := AnthropicToChatCompletionsRequest(req)
			require.NoError(t, err)
			require.NotNil(t, out.ParallelToolCalls)
			require.Equal(t, tt.want, *out.ParallelToolCalls)
		})
	}
}

// TestAnthropicToChatCompletionsRequest_ToolChoiceAny supplies any selection
// with a declared weather tool. The Chat request emits the required selector
// string after conversion succeeds for that declaration and user history.
func TestAnthropicToChatCompletionsRequest_ToolChoiceAny(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 100,
		Tools: []protocolanthropic.AnthropicTool{
			{Name: "get_weather", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		},
		ToolChoice: json.RawMessage(`{"type":"any"}`),
		Messages:   []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Equal(t, `"required"`, string(out.ToolChoice))
}

// TestAnthropicToChatCompletionsRequest_ToolChoiceSpecificTool selects a
// declared weather tool by name. The Chat selector is a function object whose
// nested function member retains that name after successful conversion.
func TestAnthropicToChatCompletionsRequest_ToolChoiceSpecificTool(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 100,
		Tools: []protocolanthropic.AnthropicTool{
			{Name: "get_weather", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		},
		ToolChoice: json.RawMessage(`{"type":"tool","name":"get_weather"}`),
		Messages:   []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	var tc map[string]any
	require.NoError(t, json.Unmarshal(out.ToolChoice, &tc))
	require.Equal(t, "function", tc["type"])
	fn, ok := tc["function"].(map[string]any)
	require.True(t, ok, "tool_choice function should be a map")
	require.Equal(t, "get_weather", fn["name"])
}

// TestAnthropicToChatCompletionsRequest_SamplingPreservedBeforeResolution
// supplies distinct temperature and top_p values on a GPT fixture. Both
// pointed-to values remain in the converted Chat request before later target
// attribute resolution.
func TestAnthropicToChatCompletionsRequest_SamplingPreservedBeforeResolution(t *testing.T) {
	temp := 0.7
	topP := 0.9
	req := &protocolanthropic.AnthropicRequest{
		Model:       "gpt-5.4",
		MaxTokens:   100,
		Temperature: &temp,
		TopP:        &topP,
		Messages:    []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Equal(t, req.Temperature, out.Temperature)
	require.Equal(t, req.TopP, out.TopP)
}

// TestAnthropicToChatCompletionsRequest_TemperaturePreservedForNonReasoningModel
// uses the DeepSeek model string with temperature and top_p. The assertions
// retain both supplied values and nonnil pointers without establishing that
// model string's reasoning capabilities.
func TestAnthropicToChatCompletionsRequest_TemperaturePreservedForNonReasoningModel(t *testing.T) {
	temp := 0.7
	topP := 0.9
	req := &protocolanthropic.AnthropicRequest{
		Model:       "deepseek-v4-pro",
		MaxTokens:   100,
		Temperature: &temp,
		TopP:        &topP,
		Messages:    []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.NotNil(t, out.Temperature)
	require.Equal(t, 0.7, *out.Temperature)
	require.NotNil(t, out.TopP)
	require.Equal(t, 0.9, *out.TopP)
}

// TestAnthropicToChatCompletionsRequest_MaxTokensFloor supplies a ten-token
// Anthropic budget for Chat conversion. The resulting nonnil completion budget
// is the shared 128-token floor asserted by the protocol fixtures.
func TestAnthropicToChatCompletionsRequest_MaxTokensFloor(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 10, // supplied budget below the enforced 128-token floor
		Messages:  []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.NotNil(t, out.MaxCompletionTokens)
	require.Equal(t, minMaxOutputTokens, *out.MaxCompletionTokens)
}

// TestAnthropicToChatCompletionsRequest_ReasoningEffortMapping supplies max
// output effort on a GPT-5.4 request. The Chat request records max after
// intermediate conversion, without exercising final provider capability
// resolution.
func TestAnthropicToChatCompletionsRequest_ReasoningEffortMapping(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:        "gpt-5.4",
		MaxTokens:    100,
		OutputConfig: &protocolanthropic.AnthropicOutputConfig{Effort: "max"},
		Messages:     []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Equal(t, "max", out.ReasoningEffort)
}

// TestAnthropicToChatCompletionsRequest_ReasoningEffortMaxForGPT56 provides max
// effort on the GPT-5.6 input. The resulting Chat reasoning_effort remains max
// after the bridge succeeds with the supplied user history.
func TestAnthropicToChatCompletionsRequest_ReasoningEffortMaxForGPT56(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:        "gpt-5.6",
		MaxTokens:    100,
		OutputConfig: &protocolanthropic.AnthropicOutputConfig{Effort: "max"},
		Messages:     []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Equal(t, "max", out.ReasoningEffort)
}

// TestAnthropicToChatCompletionsRequest_ReasoningEffortUltraRejected supplies
// whitespace-padded ultra on a GPT-5.6 fixture. Conversion returns the specific
// unsupported-effort diagnostic and a nil Chat request rather than a usable
// converted result.
func TestAnthropicToChatCompletionsRequest_ReasoningEffortUltraRejected(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:        "gpt-5.6",
		MaxTokens:    100,
		OutputConfig: &protocolanthropic.AnthropicOutputConfig{Effort: " ultra "},
		Messages:     []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.ErrorContains(t, err, `reasoning effort "ultra" is not supported`)
	require.Nil(t, out)
}

// TestAnthropicToChatCompletionsRequest_ReasoningEffortDefaultMedium omits
// output effort in the GPT-5.4 request. The successful Chat conversion supplies
// medium reasoning_effort for that otherwise ordinary user-message fixture.
func TestAnthropicToChatCompletionsRequest_ReasoningEffortDefaultMedium(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.4",
		MaxTokens: 100,
		Messages:  []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Equal(t, "medium", out.ReasoningEffort)
}

// TestAnthropicToChatCompletionsRequest_ServerToolDropped mixes an Anthropic
// web-search server tool with a weather function. The converted Chat request
// contains only the weather function in the asserted tool list.
func TestAnthropicToChatCompletionsRequest_ServerToolDropped(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 100,
		Tools: []protocolanthropic.AnthropicTool{
			{Type: "web_search_20250305", Name: "web_search"},
			{Name: "get_weather", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		},
		Messages: []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Len(t, out.Tools, 1, "web_search server tool should be dropped")
	require.Equal(t, "get_weather", out.Tools[0].Function.Name)
}

// TestDirectBridge_RequestMatchesDoubleConversion compares a tool-history
// fixture through direct Chat conversion and a Responses intermediate. Selected
// request fields and per-message roles, decoded content, call IDs, names, and
// arguments agree between those paths.
func TestDirectBridge_RequestMatchesDoubleConversion(t *testing.T) {
	temp := 0.5
	req := &protocolanthropic.AnthropicRequest{
		Model:       "deepseek-v4-pro",
		MaxTokens:   500,
		Temperature: &temp,
		System:      json.RawMessage(`"be helpful"`),
		Tools: []protocolanthropic.AnthropicTool{
			{Name: "get_weather", InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`)},
		},
		ToolChoice: json.RawMessage(`{"type":"auto"}`),
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"what's the weather?"`)},
			{Role: "assistant", Content: json.RawMessage(`[{"type":"text","text":"checking"},{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"SF"}}]`)},
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"toolu_1","content":"sunny"}]`)},
		},
	}

	// Direct Anthropic-to-Chat conversion.
	direct, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)

	// Conversion through Responses.
	responsesReq, err := AnthropicToResponses(req)
	require.NoError(t, err)
	double, err := protocolbridge.ResponsesToChatCompletionsRequestWithOptions(responsesReq, nil)
	require.NoError(t, err)

	require.Equal(t, double.Model, direct.Model)
	require.Equal(t, double.Temperature, direct.Temperature)
	require.Equal(t, double.MaxCompletionTokens, direct.MaxCompletionTokens)
	require.Equal(t, double.ReasoningEffort, direct.ReasoningEffort)
	require.Equal(t, string(double.ToolChoice), string(direct.ToolChoice))
	require.Len(t, direct.Tools, len(double.Tools))

	// Both paths must agree on message count, roles, and content.
	require.Len(t, direct.Messages, len(double.Messages), "message count mismatch")
	for i := range direct.Messages {
		require.Equal(t, double.Messages[i].Role, direct.Messages[i].Role, "msg %d role mismatch", i)
		// Compare decoded values while discarding unmarshal errors here.
		// Two invalid contents could both decode to nil, so this assertion
		// establishes value agreement rather than independent JSON validity.
		var dContent, dblContent any
		_ = json.Unmarshal(double.Messages[i].Content, &dblContent)
		_ = json.Unmarshal(direct.Messages[i].Content, &dContent)
		require.Equal(t, dblContent, dContent, "msg %d content mismatch", i)
		require.Equal(t, double.Messages[i].ToolCallID, direct.Messages[i].ToolCallID, "msg %d tool_call_id mismatch", i)
		require.Len(t, direct.Messages[i].ToolCalls, len(double.Messages[i].ToolCalls), "msg %d tool_calls count mismatch", i)
		for j := range direct.Messages[i].ToolCalls {
			require.Equal(t, double.Messages[i].ToolCalls[j].ID, direct.Messages[i].ToolCalls[j].ID, "msg %d tool %d id mismatch", i, j)
			require.Equal(t, double.Messages[i].ToolCalls[j].Function.Name, direct.Messages[i].ToolCalls[j].Function.Name, "msg %d tool %d name mismatch", i, j)
			require.Equal(t, double.Messages[i].ToolCalls[j].Function.Arguments, direct.Messages[i].ToolCalls[j].Function.Arguments, "msg %d tool %d arguments mismatch", i, j)
		}
	}
}

// TestChatCompletionsChunkToAnthropicEvents_ImageInToolResult uses request
// conversion with an image-bearing Anthropic tool result. It asserts that
// converted user content contains a PNG data URL; this fixture does not invoke
// chunk-to-event conversion.
func TestChatCompletionsChunkToAnthropicEvents_ImageInToolResult(t *testing.T) {
	// Promote images in tool_result into image_url parts of a following user message.
	req := &protocolanthropic.AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 100,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"check this image"`)},
			{Role: "assistant", Content: json.RawMessage(`[{"type":"text","text":"let me look"},{"type":"tool_use","id":"toolu_1","name":"analyze","input":{"x":1}}]`)},
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"result"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]`)},
		},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	// Require a nontrivial history before looking for user image content. The
	// scan below checks image presence independently of a fixed message order.
	require.GreaterOrEqual(t, len(out.Messages), 3)

	var foundImage bool
	for _, m := range out.Messages {
		if m.Role != "user" {
			continue
		}
		var parts []protocolopenai.ChatContentPart
		if err := json.Unmarshal(m.Content, &parts); err == nil {
			for _, p := range parts {
				if p.Type == "image_url" && p.ImageURL != nil {
					foundImage = true
					require.True(t, strings.HasPrefix(p.ImageURL.URL, "data:image/png;base64,"))
				}
			}
		}
	}
	require.True(t, foundImage, "image from tool_result should appear in user message")
}

// TestAnthropicToChatCompletionsRequest_UserArrayContentFoldsToString supplies
// two text blocks in one user message. Conversion joins them with two newline
// characters into the asserted Chat string content for this fixture.
func TestAnthropicToChatCompletionsRequest_UserArrayContentFoldsToString(t *testing.T) {
	// Text-only blocks become the asserted string with blank-line separators.
	req := &protocolanthropic.AnthropicRequest{
		Model:     "deepseek-v4-pro",
		MaxTokens: 100,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"first"},{"type":"text","text":"second"}]`)},
		},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Len(t, out.Messages, 1)
	require.Equal(t, `"first\n\nsecond"`, string(out.Messages[0].Content))
}

// TestDirectBridge_RequestMatchesDoubleConversion_ArrayUserContent compares
// histories containing text-only arrays and text plus image. Direct and
// intermediate Chat paths produce equal message counts, roles, and successfully
// decoded JSON content for those messages.
func TestDirectBridge_RequestMatchesDoubleConversion_ArrayUserContent(t *testing.T) {
	// Compare both paths on text-only and image-bearing history. Decoding
	// success is asserted separately before comparing the converted values.
	req := &protocolanthropic.AnthropicRequest{
		Model:     "deepseek-v4-pro",
		MaxTokens: 100,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"first"},{"type":"text","text":"second"}]`)},
			{Role: "assistant", Content: json.RawMessage(`"ok"`)},
			{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"look"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]`)},
		},
	}

	direct, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)

	responsesReq, err := AnthropicToResponses(req)
	require.NoError(t, err)
	double, err := protocolbridge.ResponsesToChatCompletionsRequestWithOptions(responsesReq, nil)
	require.NoError(t, err)

	require.Len(t, direct.Messages, len(double.Messages), "message count mismatch")
	for i := range direct.Messages {
		require.Equal(t, double.Messages[i].Role, direct.Messages[i].Role, "msg %d role mismatch", i)
		var dContent, dblContent any
		require.NoError(t, json.Unmarshal(double.Messages[i].Content, &dblContent))
		require.NoError(t, json.Unmarshal(direct.Messages[i].Content, &dContent))
		require.Equal(t, dblContent, dContent, "msg %d content mismatch", i)
	}
}

// TestAnthropicToChatCompletionsRequest_ToolChoiceUndeclaredDropped selects
// missing, removed-server, unknown-type, and declared function tools. Invalid
// selections disappear, while the declared weather selection retains the
// asserted nested Chat function selector.
func TestAnthropicToChatCompletionsRequest_ToolChoiceUndeclaredDropped(t *testing.T) {
	// Keep a named selection only when its declared function survives conversion.
	base := protocolanthropic.AnthropicRequest{
		Model:     "deepseek-v4-pro",
		MaxTokens: 100,
		Tools: []protocolanthropic.AnthropicTool{
			{Name: "get_weather", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "web_search_20250305", Type: "web_search_20250305", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
	}

	undeclared := base
	undeclared.ToolChoice = json.RawMessage(`{"type":"tool","name":"nonexistent"}`)
	out, err := AnthropicToChatCompletionsRequest(&undeclared)
	require.NoError(t, err)
	require.Empty(t, out.ToolChoice, "tool_choice for an undeclared tool must be dropped")

	droppedServerTool := base
	droppedServerTool.ToolChoice = json.RawMessage(`{"type":"tool","name":"web_search_20250305"}`)
	out, err = AnthropicToChatCompletionsRequest(&droppedServerTool)
	require.NoError(t, err)
	require.Empty(t, out.ToolChoice, "tool_choice for a dropped server tool must be dropped")

	unknownType := base
	unknownType.ToolChoice = json.RawMessage(`{"type":"mystery"}`)
	out, err = AnthropicToChatCompletionsRequest(&unknownType)
	require.NoError(t, err)
	require.Empty(t, out.ToolChoice, "unknown tool_choice types must be dropped")

	declared := base
	declared.ToolChoice = json.RawMessage(`{"type":"tool","name":"get_weather"}`)
	out, err = AnthropicToChatCompletionsRequest(&declared)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"function","function":{"name":"get_weather"}}`, string(out.ToolChoice))
}

// TestAnthropicToChatCompletionsRequest_ThinkingBecomesReasoningContentOnToolTurn
// supplies unsigned assistant thinking beside text and a tool call. The
// surviving Chat assistant retains the thinking in reasoning_content, one call,
// and the asserted text content.
func TestAnthropicToChatCompletionsRequest_ThinkingBecomesReasoningContentOnToolTurn(t *testing.T) {
	out, err := AnthropicToChatCompletionsRequest(anthropicAssistantMsg(t, anthropicThinkingToolTurn))
	require.NoError(t, err)

	var assistant *protocolopenai.ChatMessage
	for i := range out.Messages {
		if out.Messages[i].Role == "assistant" {
			assistant = &out.Messages[i]
			break
		}
	}
	require.NotNil(t, assistant, "assistant message must survive the bridge")
	require.Equal(t, "user wants weather, call the tool", assistant.ReasoningContent,
		"产生工具调用的 thinking 必须作为 reasoning_content 回传，否则 DeepSeek 400")
	require.Len(t, assistant.ToolCalls, 1)
	require.Equal(t, `"checking"`, string(assistant.Content), "text/tool_use 处理保持不变")
}

// TestAnthropicToChatCompletionsRequest_ReasoningContentSerializesOnWire
// serializes the thinking/tool-turn Chat fixture after successful conversion.
// Its encoded request contains the expected reasoning_content field and exact
// thinking text from that assistant turn.
func TestAnthropicToChatCompletionsRequest_ReasoningContentSerializesOnWire(t *testing.T) {
	out, err := AnthropicToChatCompletionsRequest(anthropicAssistantMsg(t, anthropicThinkingToolTurn))
	require.NoError(t, err)

	payload, err := json.Marshal(out)
	require.NoError(t, err)
	require.Contains(t, string(payload), `"reasoning_content":"user wants weather, call the tool"`)
}

// TestAnthropicChatBridge_MatchesResponsesChatBridgeReasoningPlacement compares
// equivalent thinking/tool-call histories from two source formats. Both Chat
// outputs put the same reasoning text on an assistant message containing tool
// calls.
func TestAnthropicChatBridge_MatchesResponsesChatBridgeReasoningPlacement(t *testing.T) {
	responsesReq := &protocolopenai.ResponsesRequest{
		Model: "deepseek-v4-flash",
		Input: json.RawMessage(`[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"what's the weather?"}]},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"call the tool"}]},
			{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"SF\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"sunny"}
		]`),
	}
	viaResponses, err := protocolbridge.ResponsesToChatCompletionsRequestWithOptions(responsesReq, nil)
	require.NoError(t, err)

	viaAnthropic, err := AnthropicToChatCompletionsRequest(anthropicAssistantMsg(t, `[
		{"type":"thinking","thinking":"call the tool"},
		{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"SF"}}
	]`))
	require.NoError(t, err)

	reasoningOnToolCallMessage := func(msgs []protocolopenai.ChatMessage) string {
		for _, m := range msgs {
			if m.Role == "assistant" && len(m.ToolCalls) > 0 {
				return m.ReasoningContent
			}
		}
		return ""
	}
	require.Equal(t, "call the tool", reasoningOnToolCallMessage(viaResponses.Messages),
		"前置条件：兄弟桥本来就带 reasoning_content")
	require.Equal(t, reasoningOnToolCallMessage(viaResponses.Messages),
		reasoningOnToolCallMessage(viaAnthropic.Messages),
		"两条桥对等价历史必须产出同样的 reasoning_content 位置")
}

// TestAnthropicToChatCompletionsRequest_ThinkingWithoutToolCallsStaysDropped
// supplies DeepSeek thinking alongside assistant text with no tool call.
// Reasoning content is empty in the structure and absent from serialized JSON
// while answer text remains.
func TestAnthropicToChatCompletionsRequest_ThinkingWithoutToolCallsStaysDropped(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "deepseek-v4-flash",
		MaxTokens: 100,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "assistant", Content: json.RawMessage(
				`[{"type":"thinking","thinking":"secret thoughts"},{"type":"text","text":"answer"}]`)},
		},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Len(t, out.Messages, 1)
	require.Empty(t, out.Messages[0].ReasoningContent)
	require.Equal(t, `"answer"`, string(out.Messages[0].Content))

	payload, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "reasoning_content")
}

// TestChatReasoningAliasRequestConversion supplies the historical assistant
// reasoning alias together with answer text. The converted Responses text
// contains the asserted thinking tag followed by a newline and the answer.
func TestChatReasoningAliasRequestConversion(t *testing.T) {
	request := &protocolopenai.ChatCompletionsRequest{
		Model: "reasoning-model",
		Messages: []protocolopenai.ChatMessage{
			{Role: "assistant", Reasoning: "prior plan", Content: json.RawMessage(`"answer"`)},
		},
	}

	converted, err := ChatCompletionsToResponses(request)
	require.NoError(t, err)
	require.Equal(t, "<thinking>prior plan</thinking>\nanswer", gjson.GetBytes(converted.Input, "0.content.0.text").String())
}

// TestChatCompletionsToResponses_BasicText converts one Chat user string into
// Responses input. Model and user role remain, streaming is enabled, and the
// storage option is explicitly false in the asserted result.
func TestChatCompletionsToResponses_BasicText(t *testing.T) {
	req := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-4o",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: json.RawMessage(`"Hello"`)},
		},
	}

	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)
	assert.Equal(t, "gpt-4o", resp.Model)
	assert.True(t, resp.Stream) // always forced true
	assert.False(t, *resp.Store)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	require.Len(t, items, 1)
	assert.Equal(t, "user", items[0].Role)
}

// TestChatCompletionsToResponses_SystemMessage supplies a Chat system message
// followed by a user string. Converted input contains two messages retaining
// system and user roles in the supplied order.
func TestChatCompletionsToResponses_SystemMessage(t *testing.T) {
	req := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-4o",
		Messages: []protocolopenai.ChatMessage{
			{Role: "system", Content: json.RawMessage(`"You are helpful."`)},
			{Role: "user", Content: json.RawMessage(`"Hi"`)},
		},
	}

	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	require.Len(t, items, 2)
	assert.Equal(t, "system", items[0].Role)
	assert.Equal(t, "user", items[1].Role)
}

// TestChatCompletionsToResponses_ToolCalls supplies a declared function call
// and matching tool reply without assistant text. Responses emits three history
// items, retains call linkage/name/output, and keeps the function declaration.
func TestChatCompletionsToResponses_ToolCalls(t *testing.T) {
	req := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-4o",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: json.RawMessage(`"Call the function"`)},
			{
				Role: "assistant",
				ToolCalls: []protocolopenai.ChatToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: protocolopenai.ChatFunctionCall{
							Name:      "ping",
							Arguments: `{"host":"example.com"}`,
						},
					},
				},
			},
			{
				Role:       "tool",
				ToolCallID: "call_1",
				Content:    json.RawMessage(`"pong"`),
			},
		},
		Tools: []protocolopenai.ChatTool{
			{
				Type: "function",
				Function: &protocolopenai.ChatFunction{
					Name:        "ping",
					Description: "Ping a host",
					Parameters:  json.RawMessage(`{"type":"object"}`),
				},
			},
		},
	}

	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	// user + function_call + function_call_output = 3
	// (assistant message with empty content + tool_calls → only function_call items emitted)
	require.Len(t, items, 3)

	assert.Equal(t, "function_call", items[1].Type)
	assert.Equal(t, "call_1", items[1].CallID)
	assert.Empty(t, items[1].ID)
	assert.Equal(t, "ping", items[1].Name)

	assert.Equal(t, "function_call_output", items[2].Type)
	assert.Equal(t, "call_1", items[2].CallID)
	assert.Equal(t, "pong", items[2].Output)

	require.Len(t, resp.Tools, 1)
	assert.Equal(t, "function", resp.Tools[0].Type)
	assert.Equal(t, "ping", resp.Tools[0].Name)
}

// TestChatCompletionsToResponses_ToolStrict checks omitted strictness and
// explicit true/false on Chat function tools. Converted pointers are nonnil and
// the encoded Responses tool includes the exact asserted strict value.
func TestChatCompletionsToResponses_ToolStrict(t *testing.T) {
	strictTrue := true
	strictFalse := false
	tests := []struct {
		name string // strictness fixture label
		// strict supplies optional source presence, while want is the explicit
		// target value; the omitted source case expects a nonnil false result.
		strict *bool // optional source strictness
		want   bool  // expected target strictness
	}{
		{name: "defaults omitted strict to false", want: false},
		{name: "preserves explicit true", strict: &strictTrue, want: true},
		{name: "preserves explicit false", strict: &strictFalse, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &protocolopenai.ChatCompletionsRequest{
				Model:    "gpt-4o",
				Messages: []protocolopenai.ChatMessage{{Role: "user", Content: json.RawMessage(`"Hi"`)}},
				Tools: []protocolopenai.ChatTool{{
					Type: "function",
					Function: &protocolopenai.ChatFunction{
						Name:   "lookup",
						Strict: tt.strict,
					},
				}},
			}

			resp, err := ChatCompletionsToResponses(req)
			require.NoError(t, err)
			require.Len(t, resp.Tools, 1)
			require.NotNil(t, resp.Tools[0].Strict)
			assert.Equal(t, tt.want, *resp.Tools[0].Strict)

			payload, err := json.Marshal(resp)
			require.NoError(t, err)

			var serialized struct {
				Tools []map[string]json.RawMessage `json:"tools"` // serialized tool fields
			}
			require.NoError(t, json.Unmarshal(payload, &serialized))
			require.Len(t, serialized.Tools, 1)
			strictJSON, ok := serialized.Tools[0]["strict"]
			require.True(t, ok, "strict must be present in the Responses payload")
			assert.JSONEq(t, string(mustMarshalJSON(t, tt.want)), string(strictJSON))
		})
	}
}

// TestChatCompletionsToResponses_LegacyFunctionDefaultsStrictFalse uses the
// legacy functions field without a strict value. The converted function
// supplies a nonnil false strict pointer and serializes strict:false in the
// request.
func TestChatCompletionsToResponses_LegacyFunctionDefaultsStrictFalse(t *testing.T) {
	req := &protocolopenai.ChatCompletionsRequest{
		Model:    "gpt-4o",
		Messages: []protocolopenai.ChatMessage{{Role: "user", Content: json.RawMessage(`"Hi"`)}},
		Functions: []protocolopenai.ChatFunction{{
			Name: "lookup",
		}},
	}

	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)
	require.Len(t, resp.Tools, 1)
	require.NotNil(t, resp.Tools[0].Strict)
	assert.False(t, *resp.Tools[0].Strict)

	payload, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.Contains(t, string(payload), `"strict":false`)
}

// TestChatCompletionsToResponses_MaxTokens checks legacy max_tokens below the
// output floor and simultaneous completion-token input. The first fixture
// clamps to 128, while max_completion_tokens takes precedence and retains 500
// in the second.
func TestChatCompletionsToResponses_MaxTokens(t *testing.T) {
	t.Run("max_tokens", func(t *testing.T) {
		maxTokens := 100
		req := &protocolopenai.ChatCompletionsRequest{
			Model:     "gpt-4o",
			MaxTokens: &maxTokens,
			Messages:  []protocolopenai.ChatMessage{{Role: "user", Content: json.RawMessage(`"Hi"`)}},
		}
		resp, err := ChatCompletionsToResponses(req)
		require.NoError(t, err)
		require.NotNil(t, resp.MaxOutputTokens)
		// The supplied budget exercises the bridge output-token floor.
		assert.Equal(t, minMaxOutputTokens, *resp.MaxOutputTokens)
	})

	t.Run("max_completion_tokens_preferred", func(t *testing.T) {
		maxTokens := 100
		maxCompletion := 500
		req := &protocolopenai.ChatCompletionsRequest{
			Model:               "gpt-4o",
			MaxTokens:           &maxTokens,
			MaxCompletionTokens: &maxCompletion,
			Messages:            []protocolopenai.ChatMessage{{Role: "user", Content: json.RawMessage(`"Hi"`)}},
		}
		resp, err := ChatCompletionsToResponses(req)
		require.NoError(t, err)
		require.NotNil(t, resp.MaxOutputTokens)
		assert.Equal(t, 500, *resp.MaxOutputTokens)
	})
}

// TestChatCompletionsToResponses_ReasoningEffort supplies high Chat effort with
// ordinary user input. Conversion produces nonnil reasoning with high effort
// and auto summary in the asserted Responses request.
func TestChatCompletionsToResponses_ReasoningEffort(t *testing.T) {
	req := &protocolopenai.ChatCompletionsRequest{
		Model:           "gpt-4o",
		ReasoningEffort: "high",
		Messages:        []protocolopenai.ChatMessage{{Role: "user", Content: json.RawMessage(`"Hi"`)}},
	}
	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)
	require.NotNil(t, resp.Reasoning)
	assert.Equal(t, "high", resp.Reasoning.Effort)
	assert.Equal(t, "auto", resp.Reasoning.Summary)
}

// TestChatCompletionsToResponses_ResponseFormatJsonObject converts a Chat
// json_object response format. The Responses text format matches that object
// both in the converted structure and after serialization followed by decoding.
func TestChatCompletionsToResponses_ResponseFormatJsonObject(t *testing.T) {
	req := &protocolopenai.ChatCompletionsRequest{
		Model:          "gpt-4o",
		Messages:       []protocolopenai.ChatMessage{{Role: "user", Content: json.RawMessage(`"Return JSON"`)}},
		ResponseFormat: json.RawMessage(`{"type":"json_object"}`),
	}

	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)
	require.NotNil(t, resp.Text)
	assert.JSONEq(t, `{"type":"json_object"}`, string(resp.Text.Format))

	payload, err := json.Marshal(resp)
	require.NoError(t, err)
	var serialized struct {
		Text protocolopenai.ResponsesText `json:"text"` // serialized Responses formatting
	}
	require.NoError(t, json.Unmarshal(payload, &serialized))
	assert.JSONEq(t, `{"type":"json_object"}`, string(serialized.Text.Format))
}

// TestChatCompletionsToResponses_ResponseFormatJsonSchema supplies a nested
// named JSON schema with strictness and required fields. The Responses text
// format flattens its wrapper while retaining the asserted schema contents and
// strict flag.
func TestChatCompletionsToResponses_ResponseFormatJsonSchema(t *testing.T) {
	req := &protocolopenai.ChatCompletionsRequest{
		Model:    "gpt-4o",
		Messages: []protocolopenai.ChatMessage{{Role: "user", Content: json.RawMessage(`"Return structured JSON"`)}},
		ResponseFormat: json.RawMessage(`{
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
		}`),
	}

	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)
	require.NotNil(t, resp.Text)
	assert.JSONEq(t, `{
		"type":"json_schema",
		"name":"answer",
		"schema":{
			"type":"object",
			"properties":{"ok":{"type":"boolean"}},
			"required":["ok"],
			"additionalProperties":false
		},
		"strict":true
	}`, string(resp.Text.Format))
}

// TestChatCompletionsToResponses_RejectsUltraReasoningEffort provides ultra
// effort for a GPT-5.6-sol request. The bridge returns an unsupported-effort
// error and no usable Responses request for that supplied fixture.
func TestChatCompletionsToResponses_RejectsUltraReasoningEffort(t *testing.T) {
	req := &protocolopenai.ChatCompletionsRequest{
		Model:           "gpt-5.6-sol",
		ReasoningEffort: "ultra",
		Messages:        []protocolopenai.ChatMessage{{Role: "user", Content: json.RawMessage(`"Hi"`)}},
	}

	resp, err := ChatCompletionsToResponses(req)
	require.ErrorContains(t, err, "not supported")
	require.Nil(t, resp)
}

// TestChatCompletionsToResponses_ImageURL combines text and a base64 image URL
// in Chat user content. Responses retains both ordered parts with their
// asserted text/image types and unchanged image URL.
func TestChatCompletionsToResponses_ImageURL(t *testing.T) {
	content := `[{"type":"text","text":"Describe this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,abc123"}}]`
	req := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-4o",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: json.RawMessage(content)},
		},
	}
	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	require.Len(t, items, 1)

	var parts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[0].Content, &parts))
	require.Len(t, parts, 2)
	assert.Equal(t, "input_text", parts[0].Type)
	assert.Equal(t, "Describe this", parts[0].Text)
	assert.Equal(t, "input_image", parts[1].Type)
	assert.Equal(t, "data:image/png;base64,abc123", parts[1].ImageURL)
}

// TestChatCompletionsToResponses_EmptyBase64ImageURLSkipped supplies an image
// data URL with no payload beside text. Converted user content contains only
// the retained text part after the empty image is discarded.
func TestChatCompletionsToResponses_EmptyBase64ImageURLSkipped(t *testing.T) {
	content := `[{"type":"text","text":"Describe this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,"}}]`
	req := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-4o",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: json.RawMessage(content)},
		},
	}
	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	require.Len(t, items, 1)

	var parts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[0].Content, &parts))
	require.Len(t, parts, 1)
	assert.Equal(t, "input_text", parts[0].Type)
	assert.Equal(t, "Describe this", parts[0].Text)
}

// TestChatCompletionsToResponses_WhitespaceOnlyBase64ImageURLSkipped supplies a
// whitespace-only base64 payload beside user text. Responses retains just the
// text part, excluding the unusable image from the asserted content array.
func TestChatCompletionsToResponses_WhitespaceOnlyBase64ImageURLSkipped(t *testing.T) {
	content := `[{"type":"text","text":"Describe this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,   "}}]`
	req := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-4o",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: json.RawMessage(content)},
		},
	}
	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	require.Len(t, items, 1)

	var parts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[0].Content, &parts))
	require.Len(t, parts, 1)
	assert.Equal(t, "input_text", parts[0].Type)
	assert.Equal(t, "Describe this", parts[0].Text)
}

// TestChatCompletionsToResponses_FilePartFileData supplies text and an inline
// PDF file part. The converted input_file retains filename and file_data, has
// no file_id, and follows the retained text in order.
func TestChatCompletionsToResponses_FilePartFileData(t *testing.T) {
	content := `[{"type":"text","text":"Summarize the attached document"},{"type":"file","file":{"filename":"document.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjQ="}}]`
	req := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-4o",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: json.RawMessage(content)},
		},
	}
	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	require.Len(t, items, 1)

	var parts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[0].Content, &parts))
	require.Len(t, parts, 2)
	assert.Equal(t, "input_text", parts[0].Type)
	assert.Equal(t, "Summarize the attached document", parts[0].Text)
	assert.Equal(t, "input_file", parts[1].Type)
	assert.Equal(t, "document.pdf", parts[1].Filename)
	assert.Equal(t, "data:application/pdf;base64,JVBERi0xLjQ=", parts[1].FileData)
	assert.Empty(t, parts[1].FileID)
}

// TestChatCompletionsToResponses_FilePartFileID supplies a Chat file reference
// with a file_id. The single Responses input_file retains that identifier and
// has no inline file_data in the asserted result.
func TestChatCompletionsToResponses_FilePartFileID(t *testing.T) {
	content := `[{"type":"file","file":{"file_id":"file-abc123"}}]`
	req := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-4o",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: json.RawMessage(content)},
		},
	}
	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	require.Len(t, items, 1)

	var parts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[0].Content, &parts))
	require.Len(t, parts, 1)
	assert.Equal(t, "input_file", parts[0].Type)
	assert.Equal(t, "file-abc123", parts[0].FileID)
	assert.Empty(t, parts[0].FileData)
}

// TestChatCompletionsToResponses_EmptyFilePartSkipped provides a filename-only
// file part beside user text. The converted content contains only input_text,
// excluding that file without testing image handling in this fixture.
func TestChatCompletionsToResponses_EmptyFilePartSkipped(t *testing.T) {
	// This filename-only fixture has neither inline file bytes nor an uploaded file ID.
	content := `[{"type":"text","text":"Describe this"},{"type":"file","file":{"filename":"empty.pdf"}}]`
	req := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-4o",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: json.RawMessage(content)},
		},
	}
	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	require.Len(t, items, 1)

	var parts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[0].Content, &parts))
	require.Len(t, parts, 1)
	assert.Equal(t, "input_text", parts[0].Type)
}

// TestChatCompletionsToResponses_EmptyContentNeverNull covers null,
// empty-array, empty-text, and empty-image Chat content. Every converted
// request contains one input item with an empty JSON string and no content:null
// field.
func TestChatCompletionsToResponses_EmptyContentNeverNull(t *testing.T) {
	// When these fixtures produce no usable parts, encoded input content is an
	// empty string. The assertions inspect both the complete serialized input
	// and the decoded item so a null value cannot satisfy either expectation.
	cases := []struct {
		name    string          // empty-content fixture label
		content json.RawMessage // source Chat content
	}{
		{"null content", json.RawMessage(`null`)},
		{"empty array content", json.RawMessage(`[]`)},
		{"only empty text part", json.RawMessage(`[{"type":"text","text":""}]`)},
		{"only empty base64 image part", json.RawMessage(`[{"type":"image_url","image_url":{"url":"data:image/png;base64,"}}]`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &protocolopenai.ChatCompletionsRequest{
				Model: "gpt-5.5",
				Messages: []protocolopenai.ChatMessage{
					{Role: "user", Content: tc.content},
				},
			}
			resp, err := ChatCompletionsToResponses(req)
			require.NoError(t, err)
			assert.NotContains(t, string(resp.Input), `"content":null`,
				"converted input must not contain a null content field")

			var items []protocolopenai.ResponsesInputItem
			require.NoError(t, json.Unmarshal(resp.Input, &items))
			require.Len(t, items, 1)
			assert.Equal(t, `""`, string(items[0].Content),
				"content must be an empty string, not null")
		})
	}
}

// TestChatCompletionsToResponses_SystemArrayContent supplies array-valued
// system text and user text plus image. Responses retains two messages, the
// asserted system text part, and the user image type/data URL.
func TestChatCompletionsToResponses_SystemArrayContent(t *testing.T) {
	req := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-4o",
		Messages: []protocolopenai.ChatMessage{
			{Role: "system", Content: json.RawMessage(`[{"type":"text","text":"You are a careful visual assistant."}]`)},
			{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"Describe this image"},{"type":"image_url","image_url":{"url":"data:image/png;base64,abc123"}}]`)},
		},
	}

	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	require.Len(t, items, 2)

	var systemParts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[0].Content, &systemParts))
	require.Len(t, systemParts, 1)
	assert.Equal(t, "input_text", systemParts[0].Type)
	assert.Equal(t, "You are a careful visual assistant.", systemParts[0].Text)

	var userParts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[1].Content, &userParts))
	require.Len(t, userParts, 2)
	assert.Equal(t, "input_image", userParts[1].Type)
	assert.Equal(t, "data:image/png;base64,abc123", userParts[1].ImageURL)
}

// TestChatCompletionsToResponses_LegacyFunctions uses legacy functions and
// function_call fields for a weather function. The converted declaration
// remains a function, and the selector uses a flat Responses function/name
// object.
func TestChatCompletionsToResponses_LegacyFunctions(t *testing.T) {
	req := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-4o",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: json.RawMessage(`"Hi"`)},
		},
		Functions: []protocolopenai.ChatFunction{
			{
				Name:        "get_weather",
				Description: "Get weather",
				Parameters:  json.RawMessage(`{"type":"object"}`),
			},
		},
		FunctionCall: json.RawMessage(`{"name":"get_weather"}`),
	}

	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)
	require.Len(t, resp.Tools, 1)
	assert.Equal(t, "function", resp.Tools[0].Type)
	assert.Equal(t, "get_weather", resp.Tools[0].Name)

	require.NotNil(t, resp.ToolChoice)
	var tc map[string]any
	require.NoError(t, json.Unmarshal(resp.ToolChoice, &tc))
	assert.Equal(t, "function", tc["type"])
	assert.Equal(t, "get_weather", tc["name"])
	assert.NotContains(t, tc, "function")
}

// TestChatCompletionsToResponses_ServiceTier supplies the flex tier on an
// ordinary Chat request. The converted Responses request retains flex exactly
// in the asserted service-tier field after conversion succeeds.
func TestChatCompletionsToResponses_ServiceTier(t *testing.T) {
	req := &protocolopenai.ChatCompletionsRequest{
		Model:       "gpt-4o",
		ServiceTier: "flex",
		Messages:    []protocolopenai.ChatMessage{{Role: "user", Content: json.RawMessage(`"Hi"`)}},
	}
	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)
	assert.Equal(t, "flex", resp.ServiceTier)
}

// TestChatCompletionsToResponses_ParallelToolCalls supplies explicit false and
// true parallel-tool flags. Each converted pointer is nonnil, retains the
// supplied value, and serializes that same boolean in the request.
func TestChatCompletionsToResponses_ParallelToolCalls(t *testing.T) {
	for _, value := range []bool{false, true} {
		req := &protocolopenai.ChatCompletionsRequest{
			Model:             "gpt-4o",
			ParallelToolCalls: &value,
			Messages:          []protocolopenai.ChatMessage{{Role: "user", Content: json.RawMessage(`"Hi"`)}},
		}

		resp, err := ChatCompletionsToResponses(req)
		require.NoError(t, err)
		require.NotNil(t, resp.ParallelToolCalls)
		assert.Equal(t, value, *resp.ParallelToolCalls)

		payload, err := json.Marshal(resp)
		require.NoError(t, err)
		assert.Contains(t, string(payload), `"parallel_tool_calls":`+string(mustMarshalJSON(t, value)))
	}
}

// TestChatCompletionsToResponses_SamplingPreservedBeforeResolution supplies
// temperature and top_p on a GPT-5.2 fixture. Both remain in the converted
// fields and serialized JSON before any later route-specific attribute
// filtering.
func TestChatCompletionsToResponses_SamplingPreservedBeforeResolution(t *testing.T) {
	temp := 0.7
	req := &protocolopenai.ChatCompletionsRequest{
		Model:       "gpt-5.2",
		Messages:    []protocolopenai.ChatMessage{{Role: "user", Content: json.RawMessage(`"Hi"`)}},
		Temperature: &temp,
		TopP:        &temp,
	}

	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)
	assert.Equal(t, req.Temperature, resp.Temperature)
	assert.Equal(t, req.TopP, resp.TopP)

	// Route-specific parameter filtering runs after conversion.
	b, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"temperature"`)
	assert.Contains(t, string(b), `"top_p"`)
}

// TestChatCompletionsToResponses_TemperaturePreservedForNonReasoningModel uses
// the GPT-4o string with sampling parameters. Conversion retains nonnil
// temperature and top_p pointers whose values match 0.7 within the asserted
// floating-point tolerance.
func TestChatCompletionsToResponses_TemperaturePreservedForNonReasoningModel(t *testing.T) {
	temp := 0.7
	req := &protocolopenai.ChatCompletionsRequest{
		Model:       "gpt-4o",
		Messages:    []protocolopenai.ChatMessage{{Role: "user", Content: json.RawMessage(`"Hi"`)}},
		Temperature: &temp,
		TopP:        &temp,
	}

	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)
	require.NotNil(t, resp.Temperature, "non-reasoning model: temperature must be preserved")
	// Compare retained sampling values with a small floating-point tolerance.
	assert.InDelta(t, 0.7, *resp.Temperature, 1e-9)
	require.NotNil(t, resp.TopP, "non-reasoning model: top_p must be preserved")
	assert.InDelta(t, 0.7, *resp.TopP, 1e-9)
}

// TestChatCompletionsToResponses_AssistantWithTextAndToolCalls combines
// assistant text and a function call after user input. Responses emits separate
// assistant and function-call items in order, with no fabricated item ID on the
// call.
func TestChatCompletionsToResponses_AssistantWithTextAndToolCalls(t *testing.T) {
	req := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-4o",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: json.RawMessage(`"Do something"`)},
			{
				Role:    "assistant",
				Content: json.RawMessage(`"Let me call a function."`),
				ToolCalls: []protocolopenai.ChatToolCall{
					{
						ID:   "call_abc",
						Type: "function",
						Function: protocolopenai.ChatFunctionCall{
							Name:      "do_thing",
							Arguments: `{}`,
						},
					},
				},
			},
		},
	}

	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	// user + assistant message (with text) + function_call
	require.Len(t, items, 3)
	assert.Equal(t, "user", items[0].Role)
	assert.Equal(t, "assistant", items[1].Role)
	assert.Equal(t, "function_call", items[2].Type)
	assert.Empty(t, items[2].ID)
}

// TestChatCompletionsToResponses_AssistantArrayContentPreserved supplies two
// assistant text blocks after user input. The converted assistant has one
// output_text part containing their concatenated AB value in this fixture.
func TestChatCompletionsToResponses_AssistantArrayContentPreserved(t *testing.T) {
	req := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-4o",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: json.RawMessage(`"Hi"`)},
			{Role: "assistant", Content: json.RawMessage(`[{"type":"text","text":"A"},{"type":"text","text":"B"}]`)},
		},
	}

	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	require.Len(t, items, 2)
	assert.Equal(t, "assistant", items[1].Role)

	var parts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[1].Content, &parts))
	require.Len(t, parts, 1)
	assert.Equal(t, "output_text", parts[0].Type)
	assert.Equal(t, "AB", parts[0].Text)
}

// TestChatCompletionsToResponses_AssistantThinkingTagPreserved combines
// assistant thinking and final text in array content. The converted single
// output_text contains both the asserted thinking tag and final answer text.
func TestChatCompletionsToResponses_AssistantThinkingTagPreserved(t *testing.T) {
	req := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-4o",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: json.RawMessage(`"Hi"`)},
			{Role: "assistant", Content: json.RawMessage(`[{"type":"thinking","thinking":"internal plan"},{"type":"text","text":"final answer"}]`)},
		},
	}

	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	require.Len(t, items, 2)

	var parts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[1].Content, &parts))
	require.Len(t, parts, 1)
	assert.Equal(t, "output_text", parts[0].Type)
	assert.Contains(t, parts[0].Text, "<thinking>internal plan</thinking>")
	assert.Contains(t, parts[0].Text, "final answer")
}

// TestChatCompletionsToResponses_AssistantReasoningContentPreserved supplies
// reasoning_content separately from the assistant answer string. The converted
// output_text contains the asserted thinking tag and answer in the same
// assistant content part.
func TestChatCompletionsToResponses_AssistantReasoningContentPreserved(t *testing.T) {
	req := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-4o",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: json.RawMessage(`"Hi"`)},
			{
				Role:             "assistant",
				ReasoningContent: "internal plan",
				Content:          json.RawMessage(`"final answer"`),
			},
		},
	}

	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	require.Len(t, items, 2)

	var parts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[1].Content, &parts))
	require.Len(t, parts, 1)
	assert.Equal(t, "output_text", parts[0].Type)
	assert.Contains(t, parts[0].Text, "<thinking>internal plan</thinking>")
	assert.Contains(t, parts[0].Text, "final answer")
}

// TestChatCompletionsToResponses_ToolArrayContent supplies a matching Chat tool
// result containing text and image parts. Responses retains the call ID and
// concatenates the two text values into the asserted function output.
func TestChatCompletionsToResponses_ToolArrayContent(t *testing.T) {
	req := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-4o",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: json.RawMessage(`"Use the tool"`)},
			{
				Role: "assistant",
				ToolCalls: []protocolopenai.ChatToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: protocolopenai.ChatFunctionCall{
							Name:      "inspect_image",
							Arguments: `{}`,
						},
					},
				},
			},
			{
				Role:       "tool",
				ToolCallID: "call_1",
				Content: json.RawMessage(
					`[{"type":"text","text":"image width: 100"},{"type":"image_url","image_url":{"url":"data:image/png;base64,ignored"}},{"type":"text","text":"; image height: 200"}]`,
				),
			},
		},
	}

	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)

	var items []protocolopenai.ResponsesInputItem
	require.NoError(t, json.Unmarshal(resp.Input, &items))
	require.Len(t, items, 3)
	assert.Equal(t, "function_call_output", items[2].Type)
	assert.Equal(t, "call_1", items[2].CallID)
	assert.Equal(t, "image width: 100; image height: 200", items[2].Output)
}

// TestChatCompletionsToResponsesPreservesXSearchTool supplies x_search filters,
// dates, media-understanding flags, and tool choice. The converted declaration
// retains each asserted option and its selected x_search tool-choice object.
func TestChatCompletionsToResponsesPreservesXSearchTool(t *testing.T) {
	enabled := true
	req := &protocolopenai.ChatCompletionsRequest{
		Model: "grok-4.5",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: json.RawMessage(`"latest xAI post"`)},
		},
		Tools: []protocolopenai.ChatTool{{
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

	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)
	require.Len(t, resp.Tools, 1)
	require.Equal(t, "x_search", resp.Tools[0].Type)
	require.Equal(t, []string{"xai"}, resp.Tools[0].AllowedXHandles)
	require.Equal(t, []string{"spam"}, resp.Tools[0].ExcludedXHandles)
	require.Equal(t, "2026-08-01", resp.Tools[0].FromDate)
	require.Equal(t, "2026-08-10", resp.Tools[0].ToDate)
	require.NotNil(t, resp.Tools[0].EnableImageUnderstanding)
	require.True(t, *resp.Tools[0].EnableImageUnderstanding)
	require.NotNil(t, resp.Tools[0].EnableVideoUnderstanding)
	require.True(t, *resp.Tools[0].EnableVideoUnderstanding)
	require.JSONEq(t, `{"type":"x_search"}`, string(resp.ToolChoice))
}

// TestChatCompletionsToResponsesPreservesSupportedBuiltInTools mixes a valid
// function, supported built-ins, an unknown built-in, and a missing function
// body. Exactly the function, web_search, and code_execution declarations
// remain in order.
func TestChatCompletionsToResponsesPreservesSupportedBuiltInTools(t *testing.T) {
	req := &protocolopenai.ChatCompletionsRequest{
		Model:    "claude-opus-4-6-thinking",
		Messages: []protocolopenai.ChatMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
		Tools: []protocolopenai.ChatTool{
			{Type: "function", Function: &protocolopenai.ChatFunction{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`)}},
			{Type: "web_search"},
			{Type: "code_execution"},
			{Type: "unsupported_builtin"},
			{Type: "function"},
		},
	}

	resp, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)
	require.Len(t, resp.Tools, 3)
	require.Equal(t, "function", resp.Tools[0].Type)
	require.Equal(t, "read_file", resp.Tools[0].Name)
	require.Equal(t, "web_search", resp.Tools[1].Type)
	require.Equal(t, "code_execution", resp.Tools[2].Type)
}

// TestAnthropicFastConvertsToOpenAIPriority provides fast Anthropic speed on a
// Claude fixture. The converted Responses request records priority service tier
// after successfully converting the supplied user string.
func TestAnthropicFastConvertsToOpenAIPriority(t *testing.T) {
	content, err := json.Marshal("hello")
	require.NoError(t, err)
	converted, err := AnthropicToResponses(&protocolanthropic.AnthropicRequest{
		Model:     "claude-opus-4.8",
		MaxTokens: 128,
		Speed:     "fast",
		Messages:  []protocolanthropic.AnthropicMessage{{Role: "user", Content: content}},
	})
	require.NoError(t, err)
	require.Equal(t, "priority", converted.ServiceTier)
}

// TestCCChain_OrphanToolResultFromTrimmedHistory supplies history retaining a
// tool reply after its assistant call was removed. The forwarding conversion
// chain passes adjacency checks and no decoded output block retains that orphan
// result ID.
func TestCCChain_OrphanToolResultFromTrimmedHistory(t *testing.T) {
	orphanID := "call_00_TgfbRvKlnD7oK6Dg00sL1661"
	msgs := ccChainToAnthropic(t, &protocolopenai.ChatCompletionsRequest{
		Model: "deepseek-v4-pro",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: json.RawMessage(`"search the web for X"`)},
			// The assistant declaration of orphanID was trimmed from history.
			{Role: "tool", ToolCallID: orphanID, Content: json.RawMessage(`"stale search results"`)},
			{Role: "assistant", Content: json.RawMessage(`"Here is what I found."`)},
			{Role: "user", Content: json.RawMessage(`"thanks, now do Y"`)},
		},
	})
	for _, m := range msgs {
		require.Falsef(t, hasToolResult(parseContentBlocks(m.Content), orphanID),
			"orphan tool_result %s should have been dropped", orphanID)
	}
}

// TestCCChain_ParallelToolOneResultMissing supplies two parallel calls with
// only call_a answered. The conversion chain passes pairing checks, and the
// asserted decoded history contains no tool_use for unanswered call_b.
func TestCCChain_ParallelToolOneResultMissing(t *testing.T) {
	msgs := ccChainToAnthropic(t, &protocolopenai.ChatCompletionsRequest{
		Model: "deepseek-v4-pro",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: json.RawMessage(`"search A and B"`)},
			{Role: "assistant", Content: json.RawMessage(`"searching both"`), ToolCalls: []protocolopenai.ChatToolCall{
				{ID: "call_a", Type: "function", Function: protocolopenai.ChatFunctionCall{Name: "web_search", Arguments: `{"q":"A"}`}},
				{ID: "call_b", Type: "function", Function: protocolopenai.ChatFunctionCall{Name: "web_search", Arguments: `{"q":"B"}`}},
			}},
			{Role: "tool", ToolCallID: "call_a", Content: json.RawMessage(`"result A"`)},
			// The result for call_b is absent.
		},
	})
	for _, m := range msgs {
		require.Falsef(t, hasToolUse(parseContentBlocks(m.Content), "call_b"),
			"unanswered tool_use call_b should have been dropped")
	}
}

// TestCCChain_WellFormedMultiRound supplies two sequential calls with matching
// results and intervening assistant text. The conversion chain passes adjacency
// checks and the asserted history retains both call_a and call_b declarations.
func TestCCChain_WellFormedMultiRound(t *testing.T) {
	msgs := ccChainToAnthropic(t, &protocolopenai.ChatCompletionsRequest{
		Model: "deepseek-v4-pro",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: json.RawMessage(`"do A then B"`)},
			{Role: "assistant", Content: json.RawMessage(`"running A"`), ToolCalls: []protocolopenai.ChatToolCall{
				{ID: "call_a", Type: "function", Function: protocolopenai.ChatFunctionCall{Name: "exec", Arguments: `{"cmd":"A"}`}},
			}},
			{Role: "tool", ToolCallID: "call_a", Content: json.RawMessage(`"A ok"`)},
			{Role: "assistant", Content: json.RawMessage(`"A done, running B"`), ToolCalls: []protocolopenai.ChatToolCall{
				{ID: "call_b", Type: "function", Function: protocolopenai.ChatFunctionCall{Name: "exec", Arguments: `{"cmd":"B"}`}},
			}},
			{Role: "tool", ToolCallID: "call_b", Content: json.RawMessage(`"B ok"`)},
			{Role: "assistant", Content: json.RawMessage(`"all done"`)},
		},
	})
	// Both calls survive; assertAnthropicPairing checks their matching results.
	var sawA, sawB bool
	for _, m := range msgs {
		blocks := parseContentBlocks(m.Content)
		sawA = sawA || hasToolUse(blocks, "call_a")
		sawB = sawB || hasToolUse(blocks, "call_b")
	}
	require.True(t, sawA && sawB, "both well-formed calls should be preserved")
}

// TestAnthropicReasoningBridgePreservesEffort runs low through max effort via
// Responses and Chat ingress conversion. Each resulting Anthropic output
// configuration retains its input value, including distinct xhigh and max
// levels.
func TestAnthropicReasoningBridgePreservesEffort(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		t.Run(effort, func(t *testing.T) {
			var responses protocolopenai.ResponsesRequest
			require.NoError(t, json.Unmarshal([]byte(`{"model":"claude-fable-5-1","input":"hi","reasoning":{"effort":"`+effort+`"}}`), &responses))
			converted, err := protocolbridge.ResponsesToAnthropicRequest(&responses)
			require.NoError(t, err)
			require.Equal(t, effort, converted.OutputConfig.Effort)
			var chat protocolopenai.ChatCompletionsRequest
			require.NoError(t, json.Unmarshal([]byte(`{"model":"claude-fable-5-1","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"`+effort+`"}`), &chat))
			bridge, err := ChatCompletionsToResponses(&chat)
			require.NoError(t, err)
			converted, err = protocolbridge.ResponsesToAnthropicRequest(bridge)
			require.NoError(t, err)
			require.Equal(t, effort, converted.OutputConfig.Effort)
		})
	}
}

// chatEffortFixture reads Chat effort through capability-sensitive
// normalization for supplied model candidates. Missing usable effort returns
// nil; candidate precedence and the flat Chat alias follow ExtractEffort rather
// than separate fixture logic.
func chatEffortFixture(body []byte, models ...string) *string {
	return ExtractEffort(body, true, capability.NormalizeRecordedOpenAIEffortForModel, models...)
}

// responsesEffortFixture reads nested Responses effort with
// capability-sensitive model normalization. Supplied candidates follow
// ExtractEffort precedence, and absent or rejected effort returns nil without
// enabling the flat Chat alias.
func responsesEffortFixture(body []byte, models ...string) *string {
	return ExtractEffort(body, false, capability.NormalizeRecordedOpenAIEffortForModel, models...)
}

// AnthropicToResponses delegates fixture conversion using options that retain
// max through the intermediate request. A nonnil request is required for its
// Model lookup, and protocol conversion errors are returned to the test.
func AnthropicToResponses(req *protocolanthropic.AnthropicRequest) (*protocolopenai.ResponsesRequest, error) {
	return protocolbridge.AnthropicToResponses(req, ConversionOptionsForModel(req.Model))
}

// AnthropicToChatCompletionsRequest delegates Anthropic fixture conversion with
// intermediate max-effort retention options. The request must be nonnil because
// its Model is read before delegation, and bridge errors remain available to
// assertions.
func AnthropicToChatCompletionsRequest(req *protocolanthropic.AnthropicRequest) (*protocolopenai.ChatCompletionsRequest, error) {
	return protocolbridge.AnthropicToChatCompletionsRequest(req, ConversionOptionsForModel(req.Model))
}

// ChatCompletionsToResponses delegates Chat fixture conversion with
// intermediate max-effort retention options. It requires a nonnil request for
// Model lookup and returns protocol errors directly for each test to assert.
func ChatCompletionsToResponses(req *protocolopenai.ChatCompletionsRequest) (*protocolopenai.ResponsesRequest, error) {
	return protocolbridge.ChatCompletionsToResponses(req, ConversionOptionsForModel(req.Model))
}

// anthropicAssistantMsg builds a DeepSeek user/assistant/tool-result fixture
// from raw assistant block bytes. The supplied JSON is not validated here, and
// the trailing result names toolu_1 for conversion pairing scenarios.
func anthropicAssistantMsg(t *testing.T, blocks string) *protocolanthropic.AnthropicRequest {
	t.Helper()
	return &protocolanthropic.AnthropicRequest{
		Model:     "deepseek-v4-flash",
		MaxTokens: 256,
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"what's the weather?"`)},
			{Role: "assistant", Content: json.RawMessage(blocks)},
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"toolu_1","content":"sunny"}]`)},
		},
	}
}

// mustMarshalJSON encodes a fixture value and fails the current test when
// serialization reports an error. Successful calls return encoded bytes used to
// compare literal JSON boolean fields in converted requests.
func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

// assertAnthropicPairing checks alternating message roles and bidirectional
// adjacent tool linkage. Every decoded tool_result needs a same-ID tool_use in
// the preceding message, and each tool_use needs a result in the following
// message; assertion failure stops the test.
//
// Empty history passes. Block parsing uses parseContentBlocks, so malformed or
// nonarray content may supply no tool blocks and escape linkage checks. These
// assertions establish the checked decoded relationships, not independent JSON
// validity or uniqueness of all IDs.
func assertAnthropicPairing(t *testing.T, messages []protocolanthropic.AnthropicMessage) {
	t.Helper()
	for i, m := range messages {
		blocks := parseContentBlocks(m.Content)

		// Consecutive messages must alternate roles.
		if i > 0 {
			require.NotEqualf(t, messages[i-1].Role, m.Role, "consecutive %s messages at %d", m.Role, i)
		}

		for _, b := range blocks {
			switch b.Type {
			case "tool_result":
				// Each tool_result must match a tool_use in the preceding message.
				require.Positivef(t, i, "tool_result %s has no previous message", b.ToolUseID)
				prev := parseContentBlocks(messages[i-1].Content)
				require.Truef(t, hasToolUse(prev, b.ToolUseID),
					"tool_result %s has no corresponding tool_use in previous message", b.ToolUseID)
			case "tool_use":
				// Each tool_use must have a result in the following message.
				require.Lessf(t, i+1, len(messages), "tool_use %s has no following message", b.ID)
				next := parseContentBlocks(messages[i+1].Content)
				require.Truef(t, hasToolResult(next, b.ID),
					"tool_use %s is not answered in the next message", b.ID)
			}
		}
	}
}

// hasToolUse reports whether decoded blocks contain a tool_use with the
// supplied exact ID. Other block kinds and unmatched IDs contribute no match to
// the pairing assertions.
func hasToolUse(blocks []protocolanthropic.AnthropicContentBlock, id string) bool {
	for _, b := range blocks {
		if b.Type == "tool_use" && b.ID == id {
			return true
		}
	}
	return false
}

// hasToolResult reports whether decoded blocks contain a tool_result with the
// supplied exact tool-use ID. The pairing checker uses that boolean for its
// immediately adjacent message assertion.
func hasToolResult(blocks []protocolanthropic.AnthropicContentBlock, toolUseID string) bool {
	for _, b := range blocks {
		if b.Type == "tool_result" && b.ToolUseID == toolUseID {
			return true
		}
	}
	return false
}

// parseContentBlocks returns content decoded as Anthropic blocks while
// discarding the JSON error. String or malformed input can return nil or
// partial blocks, so absence checks alone do not establish input validity.
func parseContentBlocks(raw json.RawMessage) []protocolanthropic.AnthropicContentBlock {
	var blocks []protocolanthropic.AnthropicContentBlock
	// Pairing helpers intentionally ignore decoding failures and inspect whatever
	// blocks remain. A failed decode can therefore hide a tool ID from an
	// absence assertion; callers use this helper for decoded-shape comparisons.
	_ = json.Unmarshal(raw, &blocks)
	return blocks
}

// ccChainToAnthropic runs Chat fixture conversion through Responses into an
// Anthropic request. It requires successful conversions, checks the resulting
// role/tool adjacency, and returns those messages for scenario-specific
// assertions in the calling test.
func ccChainToAnthropic(t *testing.T, ccReq *protocolopenai.ChatCompletionsRequest) []protocolanthropic.AnthropicMessage {
	t.Helper()
	respReq, err := ChatCompletionsToResponses(ccReq)
	require.NoError(t, err)
	anthReq, err := protocolbridge.ResponsesToAnthropicRequest(respReq)
	require.NoError(t, err)
	assertAnthropicPairing(t, anthReq.Messages)
	return anthReq.Messages
}
