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

const (
	anthropicThinkingToolTurn = `[
	{"type":"thinking","thinking":"user wants weather, call the tool"},
	{"type":"text","text":"checking"},
	{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"SF"}}
]`

	// minMaxOutputTokens is the minimum output budget expected by protocol tests.
	minMaxOutputTokens = 128
)

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

func TestAnthropicToResponses_MaxTokensFloor(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "gpt-5.2",
		MaxTokens: 10, // below minMaxOutputTokens (128)
		Messages:  []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Hi"`)}},
	}

	resp, err := AnthropicToResponses(req)
	require.NoError(t, err)
	assert.Equal(t, 128, *resp.MaxOutputTokens)
}

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

func TestAnthropicToResponses_NoOutputConfig(t *testing.T) {
	// Without output_config, effort defaults to medium regardless of thinking.type.
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

	// function_call_output should have text-only output (no image).
	assert.Equal(t, "function_call_output", items[2].Type)
	assert.Equal(t, "toolu_1", items[2].CallID)
	assert.Equal(t, "(empty)", items[2].Output)

	// Image should be in a separate user message.
	assert.Equal(t, "user", items[3].Role)
	var parts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[3].Content, &parts))
	require.Len(t, parts, 1)
	assert.Equal(t, "input_image", parts[0].Type)
	assert.Equal(t, "data:image/png;base64,iVBOR", parts[0].ImageURL)
}

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

	// function_call_output should have text-only output.
	assert.Equal(t, "function_call_output", items[2].Type)
	assert.Equal(t, "File metadata: 800x600 PNG", items[2].Output)

	// Image should be in a separate user message.
	assert.Equal(t, "user", items[3].Role)
	var parts []protocolopenai.ResponsesContentPart
	require.NoError(t, json.Unmarshal(items[3].Content, &parts))
	require.Len(t, parts, 1)
	assert.Equal(t, "input_image", parts[0].Type)
	assert.Equal(t, "data:image/png;base64,AAAA", parts[0].ImageURL)
}

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
	// Should default to image/png when media_type is empty.
	assert.Equal(t, "data:image/png;base64,iVBOR", parts[0].ImageURL)
}

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
	// Expect user, assistant tool calls, and a tool reply in order.
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

func TestAnthropicToChatCompletionsRequest_ParallelToolChoiceMapping(t *testing.T) {
	tests := []struct {
		name       string
		toolChoice json.RawMessage
		want       bool
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

func TestAnthropicToChatCompletionsRequest_MaxTokensFloor(t *testing.T) {
	req := &protocolanthropic.AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 10, // Below minMaxOutputTokens (128).
		Messages:  []protocolanthropic.AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}

	out, err := AnthropicToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.NotNil(t, out.MaxCompletionTokens)
	require.Equal(t, minMaxOutputTokens, *out.MaxCompletionTokens)
}

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

// TestDirectBridge_RequestMatchesDoubleConversion compares direct conversion with the Responses intermediate path.
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
		// Compare decoded JSON content to ignore encoding differences.
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
	// Expect user, assistant tool call, tool reply, then user image content.
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

func TestAnthropicToChatCompletionsRequest_UserArrayContentFoldsToString(t *testing.T) {
	// Join text-only arrays with blank lines because some Chat upstreams reject arrays without images.
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

func TestDirectBridge_RequestMatchesDoubleConversion_ArrayUserContent(t *testing.T) {
	// Both conversion paths collapse text-only content and retain arrays containing images.
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

func TestAnthropicToChatCompletionsRequest_ToolChoiceUndeclaredDropped(t *testing.T) {
	// Drop named tool_choice values that refer to removed or unknown tools to avoid upstream rejection.
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

// TestAnthropicToChatCompletionsRequest_ReasoningContentSerializesOnWire checks reasoning_content in the serialized payload.
func TestAnthropicToChatCompletionsRequest_ReasoningContentSerializesOnWire(t *testing.T) {
	out, err := AnthropicToChatCompletionsRequest(anthropicAssistantMsg(t, anthropicThinkingToolTurn))
	require.NoError(t, err)

	payload, err := json.Marshal(out)
	require.NoError(t, err)
	require.Contains(t, string(payload), `"reasoning_content":"user wants weather, call the tool"`)
}

// TestAnthropicChatBridge_MatchesResponsesChatBridgeReasoningPlacement compares both bridges for equivalent history.
// Reasoning belongs on the assistant message containing tool_calls.
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

// TestAnthropicToChatCompletionsRequest_ThinkingWithoutToolCallsStaysDropped checks text-only turns omit reasoning_content.
// Reasoning is passed back with tool calls.
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

// TestChatReasoningAliasRequestConversion checks that historical assistant reasoning aliases survive conversion to Responses.
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

func TestChatCompletionsToResponses_ToolStrict(t *testing.T) {
	strictTrue := true
	strictFalse := false
	tests := []struct {
		name   string
		strict *bool
		want   bool
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
				Tools []map[string]json.RawMessage `json:"tools"`
			}
			require.NoError(t, json.Unmarshal(payload, &serialized))
			require.Len(t, serialized.Tools, 1)
			strictJSON, ok := serialized.Tools[0]["strict"]
			require.True(t, ok, "strict must be present in the Responses payload")
			assert.JSONEq(t, string(mustMarshalJSON(t, tt.want)), string(strictJSON))
		})
	}
}

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
		// Below minMaxOutputTokens (128), should be clamped
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
		Text protocolopenai.ResponsesText `json:"text"`
	}
	require.NoError(t, json.Unmarshal(payload, &serialized))
	assert.JSONEq(t, `{"type":"json_object"}`, string(serialized.Text.Format))
}

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

func TestChatCompletionsToResponses_EmptyFilePartSkipped(t *testing.T) {
	// Drop file parts lacking file_data and file_id, and images with empty URLs, to avoid invalid upstream items.
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

func TestChatCompletionsToResponses_EmptyContentNeverNull(t *testing.T) {
	// Regression #2515: Responses rejects input-item content serialized as JSON null.
	// When a Chat message produces no usable content parts,
	// serialize its content as a string.
	cases := []struct {
		name    string
		content json.RawMessage
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
	assert.InDelta(t, 0.7, *resp.Temperature, 1e-9)
	require.NotNil(t, resp.TopP, "non-reasoning model: top_p must be preserved")
	assert.InDelta(t, 0.7, *resp.TopP, 1e-9)
}

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

// TestCCChain_OrphanToolResultFromTrimmedHistory covers this upstream HTTP 400:
//
//	unexpected ...content.0: tool_use_id found in tool_result blocks:
//	call_00_TgfbRvKlnD7oK6Dg00sL1661. Each tool_result block must have a
//	corresponding tool_use block in the previous message.
//
// Sliding-window history may retain a tool result after dropping the assistant call declaration.
// The orphan result has no matching tool_use, which Anthropic rejects.
// Conversion removes the orphan to restore valid pairing.
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

// TestCCChain_ParallelToolOneResultMissing covers a parallel tool call whose result was omitted.
// Conversion drops the unanswered tool_use so Anthropic receives complete pairs.
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

// TestCCChain_WellFormedMultiRound checks complete pairing across valid multi-round tool history.
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

// TestAnthropicReasoningBridgePreservesEffort checks that neither OpenAI ingress format raises xhigh to max.
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

// chatEffortFixture reads Chat effort and normalizes it with model capability rules.
func chatEffortFixture(body []byte, models ...string) *string {
	return ExtractEffort(body, true, capability.NormalizeRecordedOpenAIEffortForModel, models...)
}

func responsesEffortFixture(body []byte, models ...string) *string {
	return ExtractEffort(body, false, capability.NormalizeRecordedOpenAIEffortForModel, models...)
}

// AnthropicToResponses supplies model options to the protocol converter for tests.
func AnthropicToResponses(req *protocolanthropic.AnthropicRequest) (*protocolopenai.ResponsesRequest, error) {
	return protocolbridge.AnthropicToResponses(req, ConversionOptionsForModel(req.Model))
}

func AnthropicToChatCompletionsRequest(req *protocolanthropic.AnthropicRequest) (*protocolopenai.ChatCompletionsRequest, error) {
	return protocolbridge.AnthropicToChatCompletionsRequest(req, ConversionOptionsForModel(req.Model))
}

func ChatCompletionsToResponses(req *protocolopenai.ChatCompletionsRequest) (*protocolopenai.ResponsesRequest, error) {
	return protocolbridge.ChatCompletionsToResponses(req, ConversionOptionsForModel(req.Model))
}

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

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

// assertAnthropicPairing checks adjacent tool-call/result pairs required by Anthropic.
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

func hasToolUse(blocks []protocolanthropic.AnthropicContentBlock, id string) bool {
	for _, b := range blocks {
		if b.Type == "tool_use" && b.ID == id {
			return true
		}
	}
	return false
}

func hasToolResult(blocks []protocolanthropic.AnthropicContentBlock, toolUseID string) bool {
	for _, b := range blocks {
		if b.Type == "tool_result" && b.ToolUseID == toolUseID {
			return true
		}
	}
	return false
}

// parseContentBlocks decodes blocks for tool-pairing assertions.
func parseContentBlocks(raw json.RawMessage) []protocolanthropic.AnthropicContentBlock {
	var blocks []protocolanthropic.AnthropicContentBlock
	_ = json.Unmarshal(raw, &blocks)
	return blocks
}

// ccChainToAnthropic follows the Chat forwarding sequence for an Anthropic group:
// ChatCompletionsToResponses, then ResponsesToAnthropicRequest. Tests check pairing after both conversions.
func ccChainToAnthropic(t *testing.T, ccReq *protocolopenai.ChatCompletionsRequest) []protocolanthropic.AnthropicMessage {
	t.Helper()
	respReq, err := ChatCompletionsToResponses(ccReq)
	require.NoError(t, err)
	anthReq, err := protocolbridge.ResponsesToAnthropicRequest(respReq)
	require.NoError(t, err)
	assertAnthropicPairing(t, anthReq.Messages)
	return anthReq.Messages
}
