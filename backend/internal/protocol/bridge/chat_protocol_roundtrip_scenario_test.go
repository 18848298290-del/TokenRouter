package bridge

// These scenarios cover reasoning, usage, and tool streams across chatcompletions_anthropic_bridge.go,
// chatcompletions_responses_bridge.go, responses_to_anthropic.go, and responses_to_chatcompletions.go.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChatReasoningAliasNonStreaming checks the reasoning alias in both response bridges.
func TestChatReasoningAliasNonStreaming(t *testing.T) {
	var response ChatCompletionsResponse
	require.NoError(t, json.Unmarshal([]byte(`{
		"id":"chatcmpl-alias","model":"reasoning-model",
		"choices":[{"index":0,"message":{"role":"assistant","content":"final answer","reasoning":"fallback reasoning"},"finish_reason":"stop"}]
	}`), &response))

	anthropic := ChatCompletionsResponseToAnthropic(testRuntime(), &response, "claude-sonnet")
	require.Len(t, anthropic.Content, 2)
	require.Equal(t, "thinking", anthropic.Content[0].Type)
	require.Equal(t, "fallback reasoning", anthropic.Content[0].Thinking)

	responses := ChatCompletionsResponseToResponses(testRuntime(), &response, "reasoning-model", nil, nil, false, nil)
	require.Len(t, responses.Output, 2)
	require.Equal(t, "reasoning", responses.Output[0].Type)
	require.Equal(t, "fallback reasoning", responses.Output[0].Summary[0].Text)
}

// TestChatReasoningAliasStreaming checks precedence between the reasoning field and alias.
func TestChatReasoningAliasStreaming(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "alias fallback",
			payload: `{"id":"chatcmpl-alias","model":"reasoning-model","choices":[{"index":0,"delta":{"reasoning":"streamed fallback"},"finish_reason":null}]}`,
			want:    "streamed fallback",
		},
		{
			name:    "reasoning content precedence",
			payload: `{"id":"chatcmpl-alias","model":"reasoning-model","choices":[{"index":0,"delta":{"reasoning_content":"preferred reasoning","reasoning":"fallback reasoning"},"finish_reason":null}]}`,
			want:    "preferred reasoning",
		},
		{
			name:    "explicit empty reasoning content precedence",
			payload: `{"id":"chatcmpl-alias","model":"reasoning-model","choices":[{"index":0,"delta":{"reasoning_content":"","reasoning":"must not leak"},"finish_reason":null}]}`,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var chunk ChatCompletionsChunk
			require.NoError(t, json.Unmarshal([]byte(tt.payload), &chunk))

			anthropicEvents := ChatCompletionsChunkToAnthropicEvents(testRuntime(), &chunk, NewChatCompletionsToAnthropicStreamState(testRuntime(), "reasoning-model"))
			var anthropicReasoning string
			for _, event := range anthropicEvents {
				if event.Delta != nil && event.Delta.Type == "thinking_delta" {
					anthropicReasoning += event.Delta.Thinking
				}
			}
			require.Equal(t, tt.want, anthropicReasoning)

			responseEvents := ChatCompletionsChunkToResponsesEvents(testRuntime(), &chunk, NewChatCompletionsToResponsesStreamState(testRuntime(), "reasoning-model"))
			var responsesReasoning string
			for _, event := range responseEvents {
				if event.Type == "response.reasoning_summary_text.delta" {
					responsesReasoning += event.Delta
				}
			}
			require.Equal(t, tt.want, responsesReasoning)
		})
	}
}

func TestUsageConversionsPreserveCacheWriteTokens(t *testing.T) {
	var responsesUsage ResponsesUsage
	require.NoError(t, json.Unmarshal([]byte(`{
		"input_tokens":1000,
		"output_tokens":50,
		"input_tokens_details":{"cached_tokens":100,"cache_write_tokens":200}
	}`), &responsesUsage))
	require.NotNil(t, responsesUsage.InputTokensDetails)
	require.Equal(t, 200, responsesUsage.InputTokensDetails.CacheWriteTokens)

	chatUsage := chatUsageFromResponsesUsage(&responsesUsage)
	require.NotNil(t, chatUsage.PromptTokensDetails)
	require.Equal(t, 100, chatUsage.PromptTokensDetails.CachedTokens)
	require.Equal(t, 200, chatUsage.PromptTokensDetails.CacheWriteTokens)

	roundTrip := ChatUsageToResponsesUsage(chatUsage)
	require.NotNil(t, roundTrip.InputTokensDetails)
	require.Equal(t, 200, roundTrip.CacheCreationInputTokens)
	require.Equal(t, 200, roundTrip.InputTokensDetails.CacheWriteTokens)
}

// TestStreamingParallelToolUseNoGhostDelta covers Chat-to-Responses-to-Anthropic tool streams.
// Completed arguments must target an opened block or Claude Code reports "Content block not found".
func TestStreamingParallelToolUseNoGhostDelta(t *testing.T) {
	ccState := NewChatCompletionsToResponsesStreamState(testRuntime(), "glm-5.2")
	anthropicState := NewResponsesEventToAnthropicState(testRuntime())
	anthropicState.Model = "glm-5.2"

	// The first chunk carries the first call's ID, name, and arguments.
	chatChunk1 := &ChatCompletionsChunk{
		ID:    "chatcmpl-1",
		Model: "glm-5.2",
		Choices: []ChatChunkChoice{{
			Index: 0,
			Delta: ChatDelta{
				ToolCalls: []ChatToolCall{{
					Index: intPtr(0),
					ID:    "call_weather",
					Type:  "function",
					Function: ChatFunctionCall{
						Name:      "get_weather",
						Arguments: `{"city":"Tokyo"}`,
					},
				}},
			},
		}},
	}

	// The second chunk carries the second call's ID, name, and arguments.
	chatChunk2 := &ChatCompletionsChunk{
		ID:    "chatcmpl-1",
		Model: "glm-5.2",
		Choices: []ChatChunkChoice{{
			Index: 0,
			Delta: ChatDelta{
				ToolCalls: []ChatToolCall{{
					Index: intPtr(1),
					ID:    "call_time",
					Type:  "function",
					Function: ChatFunctionCall{
						Name:      "get_time",
						Arguments: `{}`,
					},
				}},
			},
		}},
	}

	// The third chunk finishes tool calls.
	chatChunk3 := &ChatCompletionsChunk{
		ID:    "chatcmpl-1",
		Model: "glm-5.2",
		Choices: []ChatChunkChoice{{
			Index:        0,
			Delta:        ChatDelta{},
			FinishReason: strPtr("tool_calls"),
		}},
	}

	// Convert Chat chunks to Responses events, then to Anthropic events.
	var allAnthropicEvents []AnthropicStreamEvent
	for _, chunk := range []*ChatCompletionsChunk{chatChunk1, chatChunk2, chatChunk3} {
		responsesEvents := ChatCompletionsChunkToResponsesEvents(testRuntime(), chunk, ccState)
		for _, rEvent := range responsesEvents {
			allAnthropicEvents = append(allAnthropicEvents, ResponsesEventToAnthropicEvents(&rEvent, anthropicState)...)
		}
	}

	// Finalization emits function_call_arguments.done for each tool.
	finalResponsesEvents := FinalizeChatCompletionsResponsesStream(testRuntime(), ccState)
	for _, rEvent := range finalResponsesEvents {
		allAnthropicEvents = append(allAnthropicEvents, ResponsesEventToAnthropicEvents(&rEvent, anthropicState)...)
	}

	// Record blocks announced by content_block_start.
	startedBlocks := make(map[int]string) // block index to type
	for _, e := range allAnthropicEvents {
		if e.Type == "content_block_start" && e.ContentBlock != nil {
			startedBlocks[*e.Index] = e.ContentBlock.Type
		}
	}

	// Every delta must target an opened block.
	for _, e := range allAnthropicEvents {
		if e.Type != "content_block_delta" || e.Index == nil {
			continue
		}
		idx := *e.Index
		_, ok := startedBlocks[idx]
		require.Truef(t, ok,
			"content_block_delta on index %d which was never content_block_start'ed (ghost delta bug #4193)", idx)
	}

	// Every stop must target an opened block.
	for _, e := range allAnthropicEvents {
		if e.Type != "content_block_stop" || e.Index == nil {
			continue
		}
		idx := *e.Index
		_, ok := startedBlocks[idx]
		require.Truef(t, ok,
			"content_block_stop on index %d which was never content_block_start'ed", idx)
	}

	// Both parallel tool_use blocks must be opened.
	var toolUseBlocks []int
	for idx, blockType := range startedBlocks {
		if blockType == "tool_use" {
			toolUseBlocks = append(toolUseBlocks, idx)
		}
	}
	assert.Len(t, toolUseBlocks, 2, "both parallel tool_use blocks should be opened")

	// The final stop reason must indicate tool use.
	var sawMessageDelta bool
	for _, e := range allAnthropicEvents {
		if e.Type == "message_delta" {
			sawMessageDelta = true
			assert.Equal(t, "tool_use", e.Delta.StopReason)
		}
	}
	assert.True(t, sawMessageDelta, "message_delta should be emitted")
}

func intPtr(v int) *int { return &v }
