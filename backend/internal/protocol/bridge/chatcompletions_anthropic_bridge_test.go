package bridge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// assembledToolUse holds a reconstructed call's ID, name, and arguments.
type assembledToolUse struct {
	// ID, Name, and Input describe one announced tool-use fixture record.
	// Starts supply correlation/name, and only mapped input_json_delta events
	// contribute text; the start input object and stop events are not read.
	ID    string // announced call correlation spelling
	Name  string // announced tool name spelling
	Input string // concatenated argument delta text
}

// TestChatCompletionsResponseToAnthropic_TextOnly converts a first-choice text
// reply with usage. Assertions check retained ID, selected model, text
// block/content, end-turn reason, and input/output counts.
func TestChatCompletionsResponseToAnthropic_TextOnly(t *testing.T) {
	resp := &ChatCompletionsResponse{
		ID:    "chatcmpl-1",
		Model: "deepseek-v4-pro",
		Choices: []ChatChoice{{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: json.RawMessage(`"hello world"`)},
			FinishReason: "stop",
		}},
		Usage: &ChatUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7},
	}

	out := ChatCompletionsResponseToAnthropic(testRuntime(), resp, "claude-sonnet-4-20250514")
	require.Equal(t, "chatcmpl-1", out.ID)
	require.Equal(t, "claude-sonnet-4-20250514", out.Model)
	require.Len(t, out.Content, 1)
	require.Equal(t, "text", out.Content[0].Type)
	require.Equal(t, "hello world", out.Content[0].Text)
	require.Equal(t, "end_turn", AnthropicStopReasonString(out.StopReason))
	require.Equal(t, 5, out.Usage.InputTokens)
	require.Equal(t, 2, out.Usage.OutputTokens)
}

// TestChatCompletionsResponseToAnthropic_ToolUse supplies a function-only
// first choice. It checks one tool-use block with correlation/name/input and a
// tool-use stop reason in the buffered reply.
func TestChatCompletionsResponseToAnthropic_ToolUse(t *testing.T) {
	resp := &ChatCompletionsResponse{
		ID:    "chatcmpl-2",
		Model: "deepseek-v4-pro",
		Choices: []ChatChoice{{
			Index: 0,
			Message: ChatMessage{
				Role: "assistant",
				ToolCalls: []ChatToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: ChatFunctionCall{
						Name:      "get_weather",
						Arguments: `{"city":"SF"}`,
					},
				}},
			},
			FinishReason: "tool_calls",
		}},
	}

	out := ChatCompletionsResponseToAnthropic(testRuntime(), resp, "claude-sonnet-4-20250514")
	require.Len(t, out.Content, 1)
	require.Equal(t, "tool_use", out.Content[0].Type)
	require.Equal(t, "call_1", out.Content[0].ID)
	require.Equal(t, "get_weather", out.Content[0].Name)
	require.Equal(t, `{"city":"SF"}`, string(out.Content[0].Input))
	require.Equal(t, "tool_use", AnthropicStopReasonString(out.StopReason))
}

// TestChatCompletionsResponseToAnthropic_ReasoningOnlyFallback supplies
// reasoning with no answer text or calls. The result must contain thinking
// then visible text, both holding the asserted reasoning string.
func TestChatCompletionsResponseToAnthropic_ReasoningOnlyFallback(t *testing.T) {
	resp := &ChatCompletionsResponse{
		ID:    "chatcmpl-3",
		Model: "deepseek-v4-pro",
		Choices: []ChatChoice{{
			Index: 0,
			Message: ChatMessage{
				Role:             "assistant",
				ReasoningContent: "I should think about this",
			},
			FinishReason: "stop",
		}},
	}

	out := ChatCompletionsResponseToAnthropic(testRuntime(), resp, "claude-sonnet-4-20250514")
	// A reasoning-only response emits both thinking and visible text.
	require.Len(t, out.Content, 2)
	require.Equal(t, "thinking", out.Content[0].Type)
	require.Equal(t, "I should think about this", out.Content[0].Thinking)
	require.Equal(t, "text", out.Content[1].Type)
	require.Equal(t, "I should think about this", out.Content[1].Text)
}

// TestChatCompletionsResponseToAnthropic_FinishReasonLength uses an assistant
// text choice whose finish reason reports length. The converted stop value
// must be max_tokens even though visible text was returned.
func TestChatCompletionsResponseToAnthropic_FinishReasonLength(t *testing.T) {
	resp := &ChatCompletionsResponse{
		ID:    "chatcmpl-4",
		Model: "deepseek-v4-pro",
		Choices: []ChatChoice{{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: json.RawMessage(`"truncated"`)},
			FinishReason: "length",
		}},
	}

	out := ChatCompletionsResponseToAnthropic(testRuntime(), resp, "claude-sonnet-4-20250514")
	require.Equal(t, "max_tokens", AnthropicStopReasonString(out.StopReason))
}

// TestChatCompletionsResponseToAnthropic_EmptyChoices supplies a reply with no
// choices. It checks one empty text block and end_turn, protecting the modeled
// content and nonempty stop defaults.
func TestChatCompletionsResponseToAnthropic_EmptyChoices(t *testing.T) {
	resp := &ChatCompletionsResponse{
		ID:      "chatcmpl-5",
		Model:   "deepseek-v4-pro",
		Choices: []ChatChoice{},
	}

	out := ChatCompletionsResponseToAnthropic(testRuntime(), resp, "claude-sonnet-4-20250514")
	require.Len(t, out.Content, 1)
	require.Equal(t, "text", out.Content[0].Type)
	require.Equal(t, "", out.Content[0].Text)
	require.Equal(t, "end_turn", AnthropicStopReasonString(out.StopReason), "empty choices must not produce an empty stop_reason")
}

// TestChatCompletionsResponseToAnthropic_CacheTokens supplies prompt input
// with read/creation cache counts. Returned usage must subtract both from
// uncached input and retain output plus the two separate cache buckets.
func TestChatCompletionsResponseToAnthropic_CacheTokens(t *testing.T) {
	resp := &ChatCompletionsResponse{
		ID:    "chatcmpl-6",
		Model: "deepseek-v4-pro",
		Choices: []ChatChoice{{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: json.RawMessage(`"hi"`)},
			FinishReason: "stop",
		}},
		Usage: &ChatUsage{
			PromptTokens:     100,
			CompletionTokens: 5,
			TotalTokens:      105,
			PromptTokensDetails: &ChatTokenDetails{
				CachedTokens:        30,
				CacheCreationTokens: 10,
			},
		},
	}

	out := ChatCompletionsResponseToAnthropic(testRuntime(), resp, "claude-sonnet-4-20250514")
	// input = prompt(100) - cached(30) - cacheCreation(10) = 60.
	require.Equal(t, 60, out.Usage.InputTokens)
	require.Equal(t, 5, out.Usage.OutputTokens)
	require.Equal(t, 30, out.Usage.CacheReadInputTokens)
	require.Equal(t, 10, out.Usage.CacheCreationInputTokens)
}

// TestChatCompletionsResponseToAnthropic_NilResponse converts a nil source
// reply with an injected runtime. It checks one text block, end-turn reason,
// and a nonempty generated response ID.
func TestChatCompletionsResponseToAnthropic_NilResponse(t *testing.T) {
	out := ChatCompletionsResponseToAnthropic(testRuntime(), nil, "claude-sonnet-4-20250514")
	require.Len(t, out.Content, 1)
	require.Equal(t, "text", out.Content[0].Type)
	require.Equal(t, "end_turn", AnthropicStopReasonString(out.StopReason), "nil response must not produce an empty stop_reason")
	require.NotEmpty(t, out.ID)
}

// TestChatCompletionsChunkToAnthropicEvents_TextOnly feeds two text fragments
// and final usage. It checks the complete event-type order, retained fragment
// list, and inspected final stop/input/output values.
func TestChatCompletionsChunkToAnthropicEvents_TextOnly(t *testing.T) {
	events := collectAnthropicStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":" world"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
	})

	types := anthropicEventTypes(events)
	// Check the complete text-block and message lifecycle.
	require.Equal(t, []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}, types)

	// Check the emitted text delta.
	var texts []string
	for _, e := range events {
		if e.Type == "content_block_delta" && e.Delta != nil {
			texts = append(texts, e.Delta.Text)
		}
	}
	require.Equal(t, []string{"hello", " world"}, texts)

	// Check the final stop reason.
	for _, e := range events {
		if e.Type == "message_delta" {
			require.Equal(t, "end_turn", e.Delta.StopReason)
			require.Equal(t, 5, e.Usage.InputTokens)
			require.Equal(t, 2, e.Usage.OutputTokens)
		}
	}
}

// TestChatCompletionsChunkToAnthropicEvents_ReasoningThenContent feeds
// reasoning before answer text and finalizes. Its exact event-type sequence
// and opened block-kind order must close thinking before text begins.
func TestChatCompletionsChunkToAnthropicEvents_ReasoningThenContent(t *testing.T) {
	events := collectAnthropicStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"reasoning_content":"thinking..."}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"answer"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
	})

	types := anthropicEventTypes(events)
	// The thinking block must close before text opens.
	require.Equal(t, []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}, types)

	// Thinking precedes the text block.
	var blockTypes []string
	for _, e := range events {
		if e.Type == "content_block_start" && e.ContentBlock != nil {
			blockTypes = append(blockTypes, e.ContentBlock.Type)
		}
	}
	require.Equal(t, []string{"thinking", "text"}, blockTypes)
}

// TestChatCompletionsChunkToAnthropicEvents_ToolCallAggregation splits one
// function's arguments after an empty first fragment. It checks complete
// lifecycle order, correlation/name, tool-use stop, and one joined argument
// delta.
func TestChatCompletionsChunkToAnthropicEvents_ToolCallAggregation(t *testing.T) {
	events := collectAnthropicStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"SF\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
	})

	types := anthropicEventTypes(events)
	// Argument fragments become one complete JSON delta at finalization.
	require.Equal(t, []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}, types)

	// Check the tool identity and stop reason.
	for _, e := range events {
		if e.Type == "content_block_start" && e.ContentBlock != nil {
			require.Equal(t, "tool_use", e.ContentBlock.Type)
			require.Equal(t, "call_1", e.ContentBlock.ID)
			require.Equal(t, "get_weather", e.ContentBlock.Name)
		}
		if e.Type == "message_delta" {
			require.Equal(t, "tool_use", e.Delta.StopReason)
		}
	}

	// The empty initial fragment must not affect assembled arguments.
	var partials []string
	for _, e := range events {
		if e.Type == "content_block_delta" && e.Delta != nil {
			partials = append(partials, e.Delta.PartialJSON)
		}
	}
	require.Equal(t, []string{`{"city":"SF"}`}, partials)
}

// TestChatCompletionsChunkToAnthropicEvents_LengthMapsToMaxTokens supplies
// partial text followed by length. Any collected message_delta must carry
// max_tokens, checking the finish mapping in the finalized fixture.
func TestChatCompletionsChunkToAnthropicEvents_LengthMapsToMaxTokens(t *testing.T) {
	events := collectAnthropicStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"content":"partial"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
	})

	for _, e := range events {
		if e.Type == "message_delta" {
			require.Equal(t, "max_tokens", e.Delta.StopReason)
		}
	}
}

// TestChatCompletionsChunkToAnthropicEvents_EmptyStream supplies an empty-
// delta finish chunk with usage. The collected type list must contain message
// start and stop even though visible content is absent.
func TestChatCompletionsChunkToAnthropicEvents_EmptyStream(t *testing.T) {
	events := collectAnthropicStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}}`,
	})

	types := anthropicEventTypes(events)
	// An empty completion still emits the message lifecycle.
	require.Contains(t, types, "message_start")
	require.Contains(t, types, "message_stop")
}

// TestChatCompletionsChunkToAnthropicEvents_MessageStartEmittedOnce feeds two
// text chunks and a finish chunk. Counting the collected events must find
// exactly one message_start across conversion and finalization.
func TestChatCompletionsChunkToAnthropicEvents_MessageStartEmittedOnce(t *testing.T) {
	events := collectAnthropicStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"content":"a"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"b"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
	})

	count := 0
	for _, e := range events {
		if e.Type == "message_start" {
			count++
		}
	}
	require.Equal(t, 1, count, "message_start should only be emitted once")
}

// TestChatCompletionsChunkToAnthropicEvents_ParallelToolCalls interleaves
// declarations/fragments for two reversed source indexes. It checks ordered
// reconstructed calls, each block's open/delta/closed lifecycle, final closed
// states, and tool-use stop evidence.
func TestChatCompletionsChunkToAnthropicEvents_ParallelToolCalls(t *testing.T) {
	events := collectAnthropicStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"tool_b","arguments":"{\"b\":"}},{"index":0,"id":"call_1","type":"function","function":{"name":"tool_a","arguments":"{\"a\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1"}},{"index":1,"function":{"arguments":"2"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"}"}},{"index":0,"function":{"arguments":"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
	})

	// Interleaved declarations and arguments produce complete calls in index order.
	tools := assembleToolUseBlocks(events)
	require.Len(t, tools, 2)
	require.Equal(t, assembledToolUse{ID: "call_1", Name: "tool_a", Input: `{"a":1}`}, tools[0])
	require.Equal(t, assembledToolUse{ID: "call_2", Name: "tool_b", Input: `{"b":2}`}, tools[1])

	// Each block follows start, delta, stop with no later deltas.
	blockState := make(map[int]string)
	for _, e := range events {
		if e.Index == nil {
			continue
		}
		idx := *e.Index
		switch e.Type {
		case "content_block_start":
			require.Empty(t, blockState[idx], "block %d must start once", idx)
			blockState[idx] = "open"
		case "content_block_delta":
			require.Equal(t, "open", blockState[idx], "block %d delta must occur while open", idx)
		case "content_block_stop":
			require.Equal(t, "open", blockState[idx], "block %d stop must follow start", idx)
			blockState[idx] = "closed"
		}
	}
	require.Equal(t, map[int]string{0: "closed", 1: "closed"}, blockState)

	for _, e := range events {
		if e.Type == "message_delta" {
			require.Equal(t, "tool_use", e.Delta.StopReason)
		}
	}
}

// TestChatCompletionsChunkToAnthropicEvents_BuffersToolArgumentFragments
// supplies opening/closing JSON around 1024 small body pieces. Stored full
// arguments remain empty, fragment count retains every piece, and finalization
// emits the asserted joined argument string.
func TestChatCompletionsChunkToAnthropicEvents_BuffersToolArgumentFragments(t *testing.T) {
	state := NewChatCompletionsToAnthropicStreamState(testRuntime(), "test")
	toolIndex := 0

	// Keep opening, repeated body, and closing pieces in separate fragment slots.
	// The assertions inspect storage and final text, without measuring allocation
	// or elapsed time.
	ChatCompletionsChunkToAnthropicEvents(testRuntime(), &ChatCompletionsChunk{
		Choices: []ChatChunkChoice{{
			Delta: ChatDelta{ToolCalls: []ChatToolCall{{
				Index: &toolIndex,
				ID:    "call_many_fragments",
				Function: ChatFunctionCall{
					Name:      "Write",
					Arguments: `{"content":"`,
				},
			}}},
		}},
	}, state)
	const fragmentCount = 1024
	for range fragmentCount {
		ChatCompletionsChunkToAnthropicEvents(testRuntime(), &ChatCompletionsChunk{
			Choices: []ChatChunkChoice{{
				Delta: ChatDelta{ToolCalls: []ChatToolCall{{
					Index:    &toolIndex,
					Function: ChatFunctionCall{Arguments: "x"},
				}}},
			}},
		}, state)
	}
	ChatCompletionsChunkToAnthropicEvents(testRuntime(), &ChatCompletionsChunk{
		Choices: []ChatChunkChoice{{
			Delta: ChatDelta{ToolCalls: []ChatToolCall{{
				Index:    &toolIndex,
				Function: ChatFunctionCall{Arguments: `"}`},
			}}},
		}},
	}, state)

	require.Empty(t, state.toolCalls[0].Function.Arguments)
	require.Len(t, state.toolArgumentFragments[0], fragmentCount+2)

	events := FinalizeChatCompletionsAnthropicStream(testRuntime(), state)
	require.Len(t, events, 5)
	require.Equal(t, `{"content":"`+strings.Repeat("x", fragmentCount)+`"}`, events[1].Delta.PartialJSON)
}

// TestFinalizeChatCompletionsAnthropicStream_NoOpAfterStop marks constructed
// state as already stopped. Finalization must return nil, protecting
// suppression of additional termination for that lifecycle flag.
func TestFinalizeChatCompletionsAnthropicStream_NoOpAfterStop(t *testing.T) {
	state := NewChatCompletionsToAnthropicStreamState(testRuntime(), "test")
	state.MessageStopSent = true

	events := FinalizeChatCompletionsAnthropicStream(testRuntime(), state)
	require.Nil(t, events, "finalize should be a no-op after message_stop")
}

// TestFinalizeChatCompletionsAnthropicStream_EmitsMessageStartIfMissing
// finalizes constructed state before any chunk arrives. The returned type list
// must contain both message_start and message_stop for that empty stream.
func TestFinalizeChatCompletionsAnthropicStream_EmitsMessageStartIfMissing(t *testing.T) {
	state := NewChatCompletionsToAnthropicStreamState(testRuntime(), "test")
	// No chunk has arrived, so message_start has not been emitted.

	events := FinalizeChatCompletionsAnthropicStream(testRuntime(), state)
	types := anthropicEventTypes(events)
	require.Contains(t, types, "message_start")
	require.Contains(t, types, "message_stop")
}

// TestDirectBridge_NonStreamingMatchesDoubleConversion compares direct and
// Responses-mediated conversion of one mixed first choice. Assertions compare
// stop/model, selected block fields, and all four usage buckets for that
// fixture.
func TestDirectBridge_NonStreamingMatchesDoubleConversion(t *testing.T) {
	resp := &ChatCompletionsResponse{
		ID:    "chatcmpl-eq",
		Model: "deepseek-v4-pro",
		Choices: []ChatChoice{{
			Index: 0,
			Message: ChatMessage{
				Role:             "assistant",
				Content:          json.RawMessage(`"hello"`),
				ReasoningContent: "reasoning text",
				ToolCalls: []ChatToolCall{{
					ID:   "call_eq",
					Type: "function",
					Function: ChatFunctionCall{
						Name:      "search",
						Arguments: `{"q":"test"}`,
					},
				}},
			},
			FinishReason: "tool_calls",
		}},
		Usage: &ChatUsage{
			PromptTokens:        50,
			CompletionTokens:    10,
			TotalTokens:         60,
			PromptTokensDetails: &ChatTokenDetails{CachedTokens: 5},
		},
	}

	// Convert directly to Anthropic.
	direct := ChatCompletionsResponseToAnthropic(testRuntime(), resp, "claude-sonnet-4-20250514")

	// Convert through Responses for comparison.
	responsesResp := ChatCompletionsResponseToResponses(testRuntime(), resp, "claude-sonnet-4-20250514", nil, nil, false, nil)
	double := ResponsesToAnthropic(responsesResp, "claude-sonnet-4-20250514")

	// Compare stop reason, model, content, and usage.
	require.Equal(t, AnthropicStopReasonString(direct.StopReason), AnthropicStopReasonString(double.StopReason))
	require.Equal(t, direct.Model, double.Model)
	require.Len(t, direct.Content, len(double.Content))
	for i := range direct.Content {
		require.Equal(t, double.Content[i].Type, direct.Content[i].Type, "block %d type mismatch", i)
		require.Equal(t, double.Content[i].Text, direct.Content[i].Text, "block %d text mismatch", i)
		require.Equal(t, double.Content[i].Thinking, direct.Content[i].Thinking, "block %d thinking mismatch", i)
		require.Equal(t, double.Content[i].Name, direct.Content[i].Name, "block %d name mismatch", i)
		require.Equal(t, double.Content[i].ID, direct.Content[i].ID, "block %d id mismatch", i)
		require.Equal(t, string(double.Content[i].Input), string(direct.Content[i].Input), "block %d input mismatch", i)
	}
	require.Equal(t, double.Usage.InputTokens, direct.Usage.InputTokens)
	require.Equal(t, double.Usage.OutputTokens, direct.Usage.OutputTokens)
	require.Equal(t, double.Usage.CacheReadInputTokens, direct.Usage.CacheReadInputTokens)
	require.Equal(t, double.Usage.CacheCreationInputTokens, direct.Usage.CacheCreationInputTokens)
}

// TestChatCompletionsToAnthropicStreamState_ToolCallNameArrivesLate sends call
// identity before name and arguments. The collected tool-use start must
// contain the name supplied in the later chunk.
func TestChatCompletionsToAnthropicStreamState_ToolCallNameArrivesLate(t *testing.T) {
	// The upstream may send an index before the tool name.
	events := collectAnthropicStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_late"}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"late_tool"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
	})

	// The buffered tool block must use the name supplied later.
	var toolName string
	for _, e := range events {
		if e.Type == "content_block_start" && e.ContentBlock != nil && e.ContentBlock.Type == "tool_use" {
			toolName = e.ContentBlock.Name
		}
	}
	require.Equal(t, "late_tool", toolName)
}

// TestChatCompletionsToAnthropicStreamState_ToolCallIDAndNameArriveLate sends
// arguments, Read name, and identity separately. Reconstruction must retain
// the late correlation/name and JSON equality must exclude empty pages.
func TestChatCompletionsToAnthropicStreamState_ToolCallIDAndNameArriveLate(t *testing.T) {
	// Arguments, name, and ID arrive separately; retain the upstream ID.
	events := collectAnthropicStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"Read","arguments":"\"README.md\","}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_late","function":{"arguments":"\"pages\":\"\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	})

	tools := assembleToolUseBlocks(events)
	require.Len(t, tools, 1)
	require.Equal(t, "call_late", tools[0].ID)
	require.Equal(t, "Read", tools[0].Name)
	require.JSONEq(t, `{"path":"README.md"}`, tools[0].Input)
}

// TestChatCompletionsToAnthropicStreamState_ToolCallArgsArriveBeforeName
// splits arguments before the name arrives. It checks retained
// correlation/name/input and requires every collected delta to follow a start
// at its index.
func TestChatCompletionsToAnthropicStreamState_ToolCallArgsArriveBeforeName(t *testing.T) {
	// Buffer arguments arriving before the name and emit them after the tool
	// block opens.
	events := collectAnthropicStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_early","function":{"arguments":"{\"city\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"SF\""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"get_weather","arguments":"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
	})

	tools := assembleToolUseBlocks(events)
	require.Len(t, tools, 1)
	require.Equal(t, "call_early", tools[0].ID)
	require.Equal(t, "get_weather", tools[0].Name)
	require.JSONEq(t, `{"city":"SF"}`, tools[0].Input)

	// A delta must follow its block's content_block_start.
	started := map[int]bool{}
	for _, e := range events {
		switch e.Type {
		case "content_block_start":
			started[*e.Index] = true
		case "content_block_delta":
			require.True(t, started[*e.Index], "delta before content_block_start on index %d", *e.Index)
		}
	}
}

// TestChatCompletionsToAnthropicStreamState_ToolCallNameNeverArrives supplies
// arguments and ID but no name. It checks one empty-named reconstructed call
// with retained input and an exact balanced event-type sequence.
func TestChatCompletionsToAnthropicStreamState_ToolCallNameNeverArrives(t *testing.T) {
	// An unnamed call still has its argument text and balanced fixture lifecycle.
	events := collectAnthropicStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_anon","function":{"arguments":"{\"a\":1}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
	})

	tools := assembleToolUseBlocks(events)
	require.Len(t, tools, 1)
	require.Equal(t, "call_anon", tools[0].ID)
	require.Equal(t, "", tools[0].Name)
	require.JSONEq(t, `{"a":1}`, tools[0].Input)

	// Block start/stop events pair before message_stop.
	types := anthropicEventTypes(events)
	require.Equal(t, []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}, types)
}

// TestChatCompletionsToAnthropicStreamState_EmptyArgsToolEmitsPlaceholderDelta
// announces a call with empty arguments. Delta-only reconstruction must
// produce its selected name and an empty object input after finalization.
func TestChatCompletionsToAnthropicStreamState_EmptyArgsToolEmitsPlaceholderDelta(t *testing.T) {
	// Supply an empty-object delta for clients that reconstruct input from deltas
	// alone.
	events := collectAnthropicStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_empty","type":"function","function":{"name":"noop","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
	})

	tools := assembleToolUseBlocks(events)
	require.Len(t, tools, 1)
	require.Equal(t, "noop", tools[0].Name)
	require.JSONEq(t, `{}`, tools[0].Input)
}

// TestDirectBridge_NonStreamingMatchesDoubleConversion_CacheWriteTokens
// supplies positive write and creation aliases together. Direct and mediated
// usage buckets must agree, and the selected cache-creation count must equal
// the positive write value.
func TestDirectBridge_NonStreamingMatchesDoubleConversion_CacheWriteTokens(t *testing.T) {
	// Cache write and creation are aliases; prefer write when both are positive.
	resp := &ChatCompletionsResponse{
		ID:    "chatcmpl-cache",
		Model: "deepseek-v4-pro",
		Choices: []ChatChoice{{
			Message:      ChatMessage{Role: "assistant", Content: json.RawMessage(`"hi"`)},
			FinishReason: "stop",
		}},
		Usage: &ChatUsage{
			PromptTokens:     100,
			CompletionTokens: 10,
			TotalTokens:      110,
			PromptTokensDetails: &ChatTokenDetails{
				CachedTokens:        20,
				CacheCreationTokens: 7,
				CacheWriteTokens:    9,
			},
		},
	}

	direct := ChatCompletionsResponseToAnthropic(testRuntime(), resp, "claude-sonnet-4-20250514")

	responsesResp := ChatCompletionsResponseToResponses(testRuntime(), resp, "claude-sonnet-4-20250514", nil, nil, false, nil)
	double := ResponsesToAnthropic(responsesResp, "claude-sonnet-4-20250514")

	require.Equal(t, double.Usage.InputTokens, direct.Usage.InputTokens)
	require.Equal(t, double.Usage.OutputTokens, direct.Usage.OutputTokens)
	require.Equal(t, double.Usage.CacheReadInputTokens, direct.Usage.CacheReadInputTokens)
	require.Equal(t, double.Usage.CacheCreationInputTokens, direct.Usage.CacheCreationInputTokens)
	require.Equal(t, 9, direct.Usage.CacheCreationInputTokens)
}

// TestChatCompletionsResponseToAnthropic_GeneratesIDWhenMissing converts a
// text reply whose ID is absent. The resulting Messages response must have a
// nonempty identity from the injected runtime.
func TestChatCompletionsResponseToAnthropic_GeneratesIDWhenMissing(t *testing.T) {
	resp := &ChatCompletionsResponse{
		Model: "deepseek-v4-pro",
		Choices: []ChatChoice{{
			Message:      ChatMessage{Role: "assistant", Content: json.RawMessage(`"hi"`)},
			FinishReason: "stop",
		}},
	}

	out := ChatCompletionsResponseToAnthropic(testRuntime(), resp, "claude-sonnet-4-20250514")
	require.NotEmpty(t, out.ID, "response id must be generated when the upstream omits one")
}

// TestDirectBridge_NonStreamingMatchesDoubleConversion_EmptyChoices compares
// two conversion routes for choice-free input. Their stop reasons must agree,
// and direct conversion must select end_turn for that fixture.
func TestDirectBridge_NonStreamingMatchesDoubleConversion_EmptyChoices(t *testing.T) {
	// An empty choices response ends with end_turn.
	resp := &ChatCompletionsResponse{ID: "chatcmpl-empty", Model: "deepseek-v4-pro"}

	direct := ChatCompletionsResponseToAnthropic(testRuntime(), resp, "claude-sonnet-4-20250514")

	responsesResp := ChatCompletionsResponseToResponses(testRuntime(), resp, "claude-sonnet-4-20250514", nil, nil, false, nil)
	double := ResponsesToAnthropic(responsesResp, "claude-sonnet-4-20250514")

	require.Equal(t, AnthropicStopReasonString(double.StopReason), AnthropicStopReasonString(direct.StopReason))
	require.Equal(t, "end_turn", AnthropicStopReasonString(direct.StopReason))
}

// TestChatCompletionsResponseToAnthropic_ContentFilterWithToolUse supplies
// content_filter finish with visible text and a function call. The direct
// reply must infer tool_use from the converted tool block.
func TestChatCompletionsResponseToAnthropic_ContentFilterWithToolUse(t *testing.T) {
	// Infer content_filter and unknown finish reasons from the content blocks.
	resp := &ChatCompletionsResponse{
		ID:    "chatcmpl-cf",
		Model: "deepseek-v4-pro",
		Choices: []ChatChoice{{
			Message: ChatMessage{
				Role:    "assistant",
				Content: json.RawMessage(`"partial"`),
				ToolCalls: []ChatToolCall{{
					ID:       "call_cf",
					Type:     "function",
					Function: ChatFunctionCall{Name: "search", Arguments: `{"q":"x"}`},
				}},
			},
			FinishReason: "content_filter",
		}},
	}

	out := ChatCompletionsResponseToAnthropic(testRuntime(), resp, "claude-sonnet-4-20250514")
	require.Equal(t, "tool_use", AnthropicStopReasonString(out.StopReason))
}

// TestAnthropicChatBridge_ReasoningSurvivesOutboundInboundRoundTrip converts
// reasoning/text/tool fields to Messages blocks and replays them as assistant
// history. It checks thinking text, restored reasoning_content, and one
// retained tool call.
func TestAnthropicChatBridge_ReasoningSurvivesOutboundInboundRoundTrip(t *testing.T) {
	upstream := ChatMessage{
		Role:             "assistant",
		ReasoningContent: "step 1: need the weather tool",
		Content:          json.RawMessage(`"checking"`),
		ToolCalls: []ChatToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: ChatFunctionCall{Name: "get_weather", Arguments: `{"city":"SF"}`},
		}},
	}

	// Convert the upstream Chat response to Anthropic blocks.
	blocks := chatMessageToAnthropicBlocks(upstream)
	require.Equal(t, "thinking", blocks[0].Type)
	require.Equal(t, upstream.ReasoningContent, blocks[0].Thinking)

	// Replay those blocks as the next client request.
	raw, err := json.Marshal(blocks)
	require.NoError(t, err)

	// Convert the replayed blocks back to a Chat request.
	back, err := anthropicAssistantToChatMessages(raw)
	require.NoError(t, err)
	require.Len(t, back, 1)
	require.Equal(t, upstream.ReasoningContent, back[0].ReasoningContent,
		"出站生成的 thinking 必须能原样还原回 reasoning_content")
	require.Len(t, back[0].ToolCalls, 1)
}

// TestAnthropicThinkingToReasoningContent compares seven plaintext, redacted,
// signature-only, empty, and no-tool fixtures. The helper must match each
// expected newline-joined or empty reasoning string under the supplied tool-
// presence flag.
func TestAnthropicThinkingToReasoningContent(t *testing.T) {
	blocksOf := func(t *testing.T, raw string) []AnthropicContentBlock {
		t.Helper()
		var blocks []AnthropicContentBlock
		require.NoError(t, json.Unmarshal([]byte(raw), &blocks))
		return blocks
	}

	cases := []struct {
		name string // thinking replay fixture label
		// raw, hasToolCalls, and want combine source block plaintext with the
		// assistant call-presence selection. False tool presence expects empty
		// output even when the supplied blocks contain thinking text.
		raw          string // supplied block-array JSON
		hasToolCalls bool   // assistant tool calls present
		want         string // expected replay reasoning text
	}{
		{
			name:         "single_thinking_block",
			raw:          `[{"type":"thinking","thinking":"a"}]`,
			hasToolCalls: true,
			want:         "a",
		},
		{
			// Join thinking blocks with newlines, as extractResponsesReasoningText does.
			name:         "multiple_blocks_join_with_newline",
			raw:          `[{"type":"thinking","thinking":"a"},{"type":"text","text":"x"},{"type":"thinking","thinking":"b"}]`,
			hasToolCalls: true,
			want:         "a\nb",
		},
		{
			// Redacted thinking has no plaintext to replay.
			name:         "redacted_thinking_has_no_plaintext",
			raw:          `[{"type":"redacted_thinking","signature":"abc"}]`,
			hasToolCalls: true,
			want:         "",
		},
		{
			// Signature-only thinking provides no plaintext contribution in this fixture.
			name:         "signature_only_thinking",
			raw:          `[{"type":"thinking","thinking":"","signature":"gAAAAxxx"}]`,
			hasToolCalls: true,
			want:         "",
		},
		{
			name:         "no_tool_calls_returns_empty",
			raw:          `[{"type":"thinking","thinking":"a"}]`,
			hasToolCalls: false,
			want:         "",
		},
		{
			name:         "no_thinking_blocks",
			raw:          `[{"type":"text","text":"x"}]`,
			hasToolCalls: true,
			want:         "",
		},
		{
			name:         "empty_blocks",
			raw:          `[]`,
			hasToolCalls: true,
			want:         "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want,
				anthropicThinkingToReasoningContent(blocksOf(t, tc.raw), tc.hasToolCalls))
		})
	}
}

// TestAnthropicAssistantToChatMessages_PlainStringContentUnaffected converts
// scalar assistant content. The single returned message must retain its
// encoded text and have no reasoning_content supplied by that scalar path.
func TestAnthropicAssistantToChatMessages_PlainStringContentUnaffected(t *testing.T) {
	msgs, err := anthropicAssistantToChatMessages(json.RawMessage(`"just text"`))
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Empty(t, msgs[0].ReasoningContent)
	require.Equal(t, `"just text"`, string(msgs[0].Content))
}

// collectAnthropicStreamEvents decodes fixture chunks, converts them through
// one state, and appends finalization. Decode failures stop the test. The
// fixed selected model wins over incoming labels, while testRuntime supplies
// the live clock and random reader used for generated metadata.
func collectAnthropicStreamEvents(t *testing.T, chunks []string) []AnthropicStreamEvent {
	t.Helper()
	state := NewChatCompletionsToAnthropicStreamState(testRuntime(), "deepseek-v4-pro")
	var events []AnthropicStreamEvent
	for _, payload := range chunks {
		var chunk ChatCompletionsChunk
		require.NoError(t, json.Unmarshal([]byte(payload), &chunk))
		events = append(events, ChatCompletionsChunkToAnthropicEvents(testRuntime(), &chunk, state)...)
	}
	events = append(events, FinalizeChatCompletionsAnthropicStream(testRuntime(), state)...)
	return events
}

// anthropicEventTypes returns event labels in their supplied order for
// lifecycle assertions.
func anthropicEventTypes(events []AnthropicStreamEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Type)
	}
	return out
}

// assembleToolUseBlocks reconstructs call metadata from tool-use starts and
// joins mapped input deltas in output order. Other kinds, missing indexes, and
// unregistered deltas are ignored; initial start Input and stop events do not
// contribute.
//
// Each new start appends another record and updates the block-index map. A
// repeated index therefore routes later deltas to its newest record, while
// earlier reconstructed entries remain. The helper models only the fields its
// fixture assertions inspect, and does not validate JSON completeness or
// balanced lifecycle.
func assembleToolUseBlocks(events []AnthropicStreamEvent) []assembledToolUse {
	blockByIdx := map[int]int{} // Anthropic block index to output position
	var out []assembledToolUse
	for _, e := range events {
		switch e.Type {
		case "content_block_start":
			if e.ContentBlock != nil && e.ContentBlock.Type == "tool_use" && e.Index != nil {
				blockByIdx[*e.Index] = len(out)
				out = append(out, assembledToolUse{ID: e.ContentBlock.ID, Name: e.ContentBlock.Name})
			}
		case "content_block_delta":
			if e.Delta != nil && e.Delta.Type == "input_json_delta" && e.Index != nil {
				// Argument deltas carry only a block position. Select metadata from its
				// registered tool start and ignore unrelated positions rather than
				// guessing a call identity.
				if pos, ok := blockByIdx[*e.Index]; ok {
					out[pos].Input += e.Delta.PartialJSON
				}
			}
		}
	}
	return out
}
