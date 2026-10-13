package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
)

// TestNormalizeOpenAIResponsesLiteTools_MovesNamespacesAndKeepsSupportedTools
// mixes ordinary tools and a namespace already declared in input. It checks
// supported top-level retention, namespace deduplication, and unchanged
// namespace tool-choice identity.
func TestNormalizeOpenAIResponsesLiteTools_MovesNamespacesAndKeepsSupportedTools(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.6-terra",
		"tools": []any{
			map[string]any{"type": "function", "name": "shell"},
			map[string]any{"type": "custom", "name": "exec"},
			map[string]any{"type": "tool_search"},
			map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{
				map[string]any{"type": "function", "name": "spawn_agent"},
			}},
		},
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hello"},
			map[string]any{"type": "additional_tools", "role": "developer", "tools": []any{
				map[string]any{"type": "namespace", "name": "image_gen"},
				map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{
					map[string]any{"type": "function", "name": "spawn_agent"},
				}},
			}},
		},
		"tool_choice": map[string]any{"type": "namespace", "name": "collaboration"},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.True(t, changed)
	tools := testassert.MustType[[]any](reqBody["tools"])
	require.Len(t, tools, 3)
	require.Equal(t, "function", testassert.MustType[map[string]any](tools[0])["type"])
	require.Equal(t, "custom", testassert.MustType[map[string]any](tools[1])["type"])
	require.Equal(t, "tool_search", testassert.MustType[map[string]any](tools[2])["type"])
	input := testassert.MustType[[]any](reqBody["input"])
	require.Len(t, input, 2)
	additional := testassert.MustType[[]any](testassert.MustType[map[string]any](input[1])["tools"])
	require.Len(t, additional, 2)
	require.Equal(t, "image_gen", testassert.MustType[map[string]any](additional[0])["name"])
	require.Equal(t, "collaboration", testassert.MustType[map[string]any](additional[1])["name"], "existing namespace must not be duplicated")
	require.Equal(t, map[string]any{"type": "namespace", "name": "collaboration"}, reqBody["tool_choice"])
}

// TestNormalizeOpenAIResponsesLiteTools_PreservesDeferredFlagsWithToolSearch
// supplies tool search beside a deferred function declaration. It checks both
// tool-search presence and defer_loading retention after Lite normalization.
func TestNormalizeOpenAIResponsesLiteTools_PreservesDeferredFlagsWithToolSearch(t *testing.T) {
	reqBody := map[string]any{
		"tools": []any{
			map[string]any{"type": "tool_search"},
			map[string]any{"type": "function", "name": "shell", "defer_loading": true},
		},
	}

	_, err := normalizeOpenAIResponsesLiteTools(reqBody)
	require.NoError(t, err)
	tools := testassert.MustType[[]any](reqBody["tools"])
	require.Equal(t, "tool_search", testassert.MustType[map[string]any](tools[0])["type"])
	require.Equal(t, true, testassert.MustType[map[string]any](tools[1])["defer_loading"])
}

// TestNormalizeOpenAIResponsesLiteTools_RejectsConflictingAdditionalTool gives
// one namespace different nested definitions at top level and in input. The
// merge must fail and retain top-level tools for caller error handling.
func TestNormalizeOpenAIResponsesLiteTools_RejectsConflictingAdditionalTool(t *testing.T) {
	reqBody := map[string]any{
		"tools": []any{map[string]any{
			"type":  "namespace",
			"name":  "collaboration",
			"tools": []any{map[string]any{"type": "function", "name": "spawn_agent"}},
		}},
		"input": []any{map[string]any{
			"type": "additional_tools",
			"tools": []any{map[string]any{
				"type":  "namespace",
				"name":  "collaboration",
				"tools": []any{map[string]any{"type": "function", "name": "send_message"}},
			}},
		}},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.ErrorContains(t, err, `conflicts with migrated tool type "namespace" name "collaboration"`)
	require.False(t, changed)
	require.Len(t, reqBody["tools"], 1, "conflicts must not partially remove top-level tools")
}

// TestNormalizeOpenAIResponsesLiteTools_DeduplicatesAcrossAdditionalToolItems
// declares the migrated namespace in the second input tool item. Top-level
// removal must leave both existing items at their original lengths.
func TestNormalizeOpenAIResponsesLiteTools_DeduplicatesAcrossAdditionalToolItems(t *testing.T) {
	namespace := map[string]any{
		"type":  "namespace",
		"name":  "collaboration",
		"tools": []any{map[string]any{"type": "function", "name": "spawn_agent"}},
	}
	reqBody := map[string]any{
		"tools": []any{namespace},
		"input": []any{
			map[string]any{
				"type":  "additional_tools",
				"tools": []any{map[string]any{"type": "custom", "name": "exec"}},
			},
			map[string]any{
				"type":  "additional_tools",
				"tools": []any{namespace},
			},
		},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.NotContains(t, reqBody, "tools")
	input := testassert.MustType[[]any](reqBody["input"])
	require.Len(t, testassert.MustType[map[string]any](input[0])["tools"], 1)
	require.Len(t, testassert.MustType[map[string]any](input[1])["tools"], 1)
}

// TestNormalizeOpenAIResponsesLiteTools_ConvertsStringInput moves a namespace
// when input is plain text. It checks preservation as a user message followed
// by an additional-tools item and removal of top-level namespace tools.
func TestNormalizeOpenAIResponsesLiteTools_ConvertsStringInput(t *testing.T) {
	reqBody := map[string]any{
		"input": "hello",
		"tools": []any{map[string]any{
			"type": "namespace",
			"name": "collaboration",
		}},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.NotContains(t, reqBody, "tools")
	input := testassert.MustType[[]any](reqBody["input"])
	require.Len(t, input, 2)
	require.Equal(t, "message", testassert.MustType[map[string]any](input[0])["type"])
	require.Equal(t, "hello", testassert.MustType[map[string]any](input[0])["content"])
	require.Equal(t, "additional_tools", testassert.MustType[map[string]any](input[1])["type"])
}

// TestNormalizeOpenAIResponsesLiteTools_KeepsSupportedTopLevelTools starts with
// already-correct reasoning and parallel settings plus supported tools.
// Normalization must report no change and retain all four declarations.
func TestNormalizeOpenAIResponsesLiteTools_KeepsSupportedTopLevelTools(t *testing.T) {
	reqBody := map[string]any{
		"reasoning":           map[string]any{"context": "all_turns"},
		"parallel_tool_calls": false,
		"tools": []any{
			map[string]any{"type": "function", "name": "shell"},
			map[string]any{"type": "custom", "name": "exec"},
			map[string]any{"type": "tool_search"},
			"custom shorthand",
		},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.False(t, changed)
	require.Len(t, reqBody["tools"], 4)
	require.Equal(t, false, reqBody["parallel_tool_calls"])
}

// TestNormalizeOpenAIResponsesLiteTools_ForcesParallelToolCallsFalse covers
// top-level tools and input-only additional-tools declarations. Both requests
// must report changes and end with parallel tool calls disabled.
func TestNormalizeOpenAIResponsesLiteTools_ForcesParallelToolCallsFalse(t *testing.T) {
	tests := []struct {
		name string         // Lite normalization case description
		body map[string]any // decoded Lite request fixture
	}{
		{
			name: "top-level tools",
			body: map[string]any{
				"tools":               []any{map[string]any{"type": "function", "name": "shell"}},
				"parallel_tool_calls": true,
			},
		},
		{
			name: "input additional tools",
			body: map[string]any{
				"input": []any{map[string]any{
					"type":  "additional_tools",
					"tools": []any{map[string]any{"type": "namespace", "name": "collaboration"}},
				}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed, err := normalizeOpenAIResponsesLiteTools(tt.body)

			require.NoError(t, err)
			require.True(t, changed)
			require.Equal(t, false, tt.body["parallel_tool_calls"])
		})
	}
}

// TestNormalizeOpenAIResponsesLiteTools_RejectsNonBooleanParallelToolCalls
// supplies several nonboolean and null parallel values. It checks validation
// errors and retained invalid values, preventing implicit truth conversion
// during full Lite normalization.
func TestNormalizeOpenAIResponsesLiteTools_RejectsNonBooleanParallelToolCalls(t *testing.T) {
	for _, value := range []any{"false", float64(0), nil, map[string]any{}} {
		reqBody := map[string]any{
			"tools":               []any{map[string]any{"type": "function", "name": "shell"}},
			"parallel_tool_calls": value,
		}

		changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

		require.ErrorContains(t, err, "parallel_tool_calls to be a boolean")
		require.False(t, changed)
		require.Equal(t, value, reqBody["parallel_tool_calls"])
	}

	reqBody := map[string]any{"parallel_tool_calls": []any{}}
	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)
	require.ErrorContains(t, err, "parallel_tool_calls to be a boolean")
	require.False(t, changed)
}

// TestNormalizeOpenAIResponsesLiteTools_ParallelToolCallsIsIdempotent
// normalizes a request whose parallel flag is initially enabled. The first call
// must disable it and the second must report no further change.
func TestNormalizeOpenAIResponsesLiteTools_ParallelToolCallsIsIdempotent(t *testing.T) {
	reqBody := map[string]any{
		"reasoning":           map[string]any{"context": "all_turns"},
		"tools":               []any{map[string]any{"type": "function", "name": "shell"}},
		"parallel_tool_calls": true,
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, false, reqBody["parallel_tool_calls"])

	changed, err = normalizeOpenAIResponsesLiteTools(reqBody)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, false, reqBody["parallel_tool_calls"])
}

// TestNormalizeOpenAIResponsesLiteTools_DisablesParallelToolCalls varies
// missing, enabled, disabled, null, and string flag inputs. Valid forms end as
// boolean false with correct change flags, while invalid supplied values remain
// for the error path.
func TestNormalizeOpenAIResponsesLiteTools_DisablesParallelToolCalls(t *testing.T) {
	tests := []struct {
		name        string // Lite normalization case description
		value       any    // supplied parallel-call flag value
		setValue    bool   // flag presence fixture selector
		wantChanged bool   // expected normalization change result
	}{
		{name: "missing", wantChanged: true},
		{name: "enabled", value: true, setValue: true, wantChanged: true},
		{name: "disabled", value: false, setValue: true, wantChanged: false},
		{name: "null", value: nil, setValue: true, wantChanged: false},
		{name: "invalid type", value: "true", setValue: true, wantChanged: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqBody := map[string]any{
				"reasoning": map[string]any{"context": "all_turns"},
			}
			if tt.setValue {
				reqBody["parallel_tool_calls"] = tt.value
			}

			changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

			if tt.name == "null" || tt.name == "invalid type" {
				require.ErrorContains(t, err, "parallel_tool_calls to be a boolean")
				require.Equal(t, tt.value, reqBody["parallel_tool_calls"])
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantChanged, changed)
			parallelToolCalls, ok := reqBody["parallel_tool_calls"].(bool)
			require.True(t, ok)
			require.False(t, parallelToolCalls)
		})
	}
}

// TestNormalizeOpenAIResponsesLiteTools_EnsuresReasoningContext covers missing
// reasoning and existing objects with absent or different context. It checks
// all_turns, disabled parallel calls, and retention of supplied effort.
func TestNormalizeOpenAIResponsesLiteTools_EnsuresReasoningContext(t *testing.T) {
	tests := []struct {
		name      string // Lite normalization case description
		reasoning any    // supplied reasoning object fixture
	}{
		{name: "missing"},
		{name: "missing context", reasoning: map[string]any{"effort": "high"}},
		{name: "wrong context", reasoning: map[string]any{"effort": "medium", "context": "current_turn"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqBody := map[string]any{"input": "hello"}
			if tt.reasoning != nil {
				reqBody["reasoning"] = tt.reasoning
			}

			changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

			require.NoError(t, err)
			require.True(t, changed)
			reasoning := testassert.MustType[map[string]any](reqBody["reasoning"])
			require.Equal(t, "all_turns", reasoning["context"])
			require.Equal(t, false, reqBody["parallel_tool_calls"])
			if tt.name != "missing" {
				require.Equal(t, testassert.MustType[map[string]any](tt.reasoning)["effort"], reasoning["effort"])
			}
		})
	}
}

// TestNormalizeOpenAIResponsesLiteTools_RejectsNonObjectReasoning supplies a
// string instead of a reasoning object. It expects a typed field error,
// retained reasoning, and no parallel flag assignment after validation fails.
func TestNormalizeOpenAIResponsesLiteTools_RejectsNonObjectReasoning(t *testing.T) {
	reqBody := map[string]any{"reasoning": "high"}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.ErrorContains(t, err, "reasoning to be an object")
	require.False(t, changed)
	require.Equal(t, "high", reqBody["reasoning"])
	require.NotContains(t, reqBody, "parallel_tool_calls")
}

// TestNormalizeOpenAIResponsesLiteTools_RejectsUnsupportedTools supplies hosted
// search and image-generation declarations. It checks rejection messages and
// retained tools with no parallel-call assignment after failed validation.
func TestNormalizeOpenAIResponsesLiteTools_RejectsUnsupportedTools(t *testing.T) {
	tests := []struct {
		name string         // Lite normalization case description
		tool map[string]any // unsupported hosted tool fixture
		want string         // expected validation error fragment
	}{
		{name: "hosted web search", tool: map[string]any{"type": "web_search"}, want: `top-level tool type "web_search"`},
		{name: "hosted image generation", tool: map[string]any{"type": "image_generation"}, want: `top-level tool type "image_generation"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqBody := map[string]any{"tools": []any{tt.tool}}
			changed, err := normalizeOpenAIResponsesLiteTools(reqBody)
			require.ErrorContains(t, err, tt.want)
			require.False(t, changed)
			require.Len(t, reqBody["tools"], 1, "validation errors must not partially mutate tools")
			require.NotContains(t, reqBody, "parallel_tool_calls")
		})
	}
}

// TestNormalizeOpenAIResponsesLiteToolsPayload_PreservesResponseCreateShape
// rewrites raw create-frame JSON with a namespace and large integer metadata.
// It checks protocol shape, disabled parallel calls, moved tools, tool choice,
// and exact integer precision.
func TestNormalizeOpenAIResponsesLiteToolsPayload_PreservesResponseCreateShape(t *testing.T) {
	body := []byte(`{
		"type":"response.create",
		"model":"gpt-5.6-terra",
		"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},
		"parallel_tool_calls":true,
		"input":[{"type":"message","role":"user","content":"hello","nonce":9007199254740993}],
		"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]}],
		"tool_choice":{"type":"namespace","name":"collaboration"}
	}`)

	updated, changed, err := NormalizeResponsesLiteToolsPayload(body)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "response.create", gjson.GetBytes(updated, "type").String())
	require.True(t, gjson.GetBytes(updated, "parallel_tool_calls").Exists())
	require.False(t, gjson.GetBytes(updated, "parallel_tool_calls").Bool())
	require.False(t, gjson.GetBytes(updated, "tools").Exists())
	require.Equal(t, "collaboration", gjson.GetBytes(updated, `input.#(type=="additional_tools").tools.0.name`).String())
	require.Equal(t, "namespace", gjson.GetBytes(updated, "tool_choice.type").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(updated, "input.0.nonce").Raw)
}
