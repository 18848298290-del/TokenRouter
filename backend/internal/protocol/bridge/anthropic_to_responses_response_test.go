package bridge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAnthropicToResponsesResponse_CacheTokensUseOpenAIInputSemantics supplies
// uncached, cache-read, cache-write, and output usage. Assertions check summed
// input/total counts, retained output, and the cached-token detail value.
func TestAnthropicToResponsesResponse_CacheTokensUseOpenAIInputSemantics(t *testing.T) {
	resp := &AnthropicResponse{
		ID:    "msg_cache",
		Model: "claude-sonnet-4-5-20250929",
		Content: []AnthropicContentBlock{
			{Type: "text", Text: "ok"},
		},
		StopReason: AnthropicStopReasonPtr("end_turn"),
		Usage: AnthropicUsage{
			InputTokens:              3318,
			OutputTokens:             123,
			CacheReadInputTokens:     50688,
			CacheCreationInputTokens: 200,
		},
	}

	out := AnthropicToResponsesResponse(testRuntime(), resp)
	require.NotNil(t, out.Usage)
	// 3318 uncached + 50688 cache read + 200 cache creation = 54206.
	assert.Equal(t, 54206, out.Usage.InputTokens)
	assert.Equal(t, 123, out.Usage.OutputTokens)
	assert.Equal(t, 54329, out.Usage.TotalTokens)
	require.NotNil(t, out.Usage.InputTokensDetails)
	assert.Equal(t, 50688, out.Usage.InputTokensDetails.CachedTokens)
}

// TestAnthropicToResponsesResponse_NoCacheTokens converts usage containing
// uncached input and output alone. Input/output/total must retain those
// counts, and cache-read detail remains nil in the returned struct.
func TestAnthropicToResponsesResponse_NoCacheTokens(t *testing.T) {
	resp := &AnthropicResponse{
		ID:    "msg_nocache",
		Model: "claude-sonnet-4-5-20250929",
		Content: []AnthropicContentBlock{
			{Type: "text", Text: "ok"},
		},
		StopReason: AnthropicStopReasonPtr("end_turn"),
		Usage: AnthropicUsage{
			InputTokens:  100,
			OutputTokens: 50,
		},
	}

	out := AnthropicToResponsesResponse(testRuntime(), resp)
	require.NotNil(t, out.Usage)
	assert.Equal(t, 100, out.Usage.InputTokens)
	assert.Equal(t, 50, out.Usage.OutputTokens)
	assert.Equal(t, 150, out.Usage.TotalTokens)
	assert.Nil(t, out.Usage.InputTokensDetails)
}

// TestAnthropicEventToResponses_CacheTokensRoundTripFromMessageStart supplies
// initial uncached/read/write counters and a later output count. The terminal
// event must retain normalized input/total usage and the inspected cache-read
// detail.
func TestAnthropicEventToResponses_CacheTokensRoundTripFromMessageStart(t *testing.T) {
	state := NewAnthropicEventToResponsesState(testRuntime())

	// message_start supplies the initial cache counters.
	AnthropicEventToResponsesEvents(testRuntime(), &AnthropicStreamEvent{
		Type: "message_start",
		Message: &AnthropicResponse{
			ID:    "msg_stream_cache",
			Model: "claude-sonnet-4-5-20250929",
			Usage: AnthropicUsage{
				InputTokens:              12,
				CacheReadInputTokens:     9,
				CacheCreationInputTokens: 3,
			},
		},
	}, state)

	AnthropicEventToResponsesEvents(testRuntime(), &AnthropicStreamEvent{
		Type: "message_delta",
		Usage: &AnthropicUsage{
			OutputTokens: 7,
		},
	}, state)

	events := AnthropicEventToResponsesEvents(testRuntime(), &AnthropicStreamEvent{Type: "message_stop"}, state)

	// response.completed includes usage normalized to OpenAI totals.
	var completed *ResponsesStreamEvent
	for i := range events {
		if events[i].Type == "response.completed" {
			completed = &events[i]
		}
	}
	require.NotNil(t, completed, "response.completed event must be emitted")
	require.NotNil(t, completed.Response)
	require.NotNil(t, completed.Response.Usage)
	// 12 uncached + 9 cache read + 3 cache creation = 24.
	assert.Equal(t, 24, completed.Response.Usage.InputTokens)
	assert.Equal(t, 7, completed.Response.Usage.OutputTokens)
	assert.Equal(t, 31, completed.Response.Usage.TotalTokens)
	require.NotNil(t, completed.Response.Usage.InputTokensDetails)
	assert.Equal(t, 9, completed.Response.Usage.InputTokensDetails.CachedTokens)
}

// TestAnthropicEventToResponses_CacheTokensFromMessageDelta supplies input at
// creation and cache/output counts later. The terminal snapshot must add the
// retained input and final cache buckets, with the asserted output/detail
// values.
func TestAnthropicEventToResponses_CacheTokensFromMessageDelta(t *testing.T) {
	state := NewAnthropicEventToResponsesState(testRuntime())

	AnthropicEventToResponsesEvents(testRuntime(), &AnthropicStreamEvent{
		Type: "message_start",
		Message: &AnthropicResponse{
			ID:    "msg_delta_cache",
			Model: "claude-sonnet-4-5-20250929",
			Usage: AnthropicUsage{InputTokens: 20},
		},
	}, state)

	// Withhold initial cache counts in this fixture so terminal aggregation
	// depends on the later usage observation while retaining the initial uncached
	// input.
	AnthropicEventToResponsesEvents(testRuntime(), &AnthropicStreamEvent{
		Type: "message_delta",
		Usage: &AnthropicUsage{
			OutputTokens:             8,
			CacheReadInputTokens:     11,
			CacheCreationInputTokens: 4,
		},
	}, state)

	events := AnthropicEventToResponsesEvents(testRuntime(), &AnthropicStreamEvent{Type: "message_stop"}, state)

	var completed *ResponsesStreamEvent
	for i := range events {
		if events[i].Type == "response.completed" {
			completed = &events[i]
		}
	}
	require.NotNil(t, completed)
	require.NotNil(t, completed.Response.Usage)
	// 20 uncached + 11 cache read + 4 cache creation = 35.
	assert.Equal(t, 35, completed.Response.Usage.InputTokens)
	assert.Equal(t, 8, completed.Response.Usage.OutputTokens)
	require.NotNil(t, completed.Response.Usage.InputTokensDetails)
	assert.Equal(t, 11, completed.Response.Usage.InputTokensDetails.CachedTokens)
}

// TestAnthropicEventToResponses_TextEmitsContentPart feeds one text block
// split into two deltas. It checks part-added and first-delta presence/order
// plus a part-done event, without decoding terminal payload content.
func TestAnthropicEventToResponses_TextEmitsContentPart(t *testing.T) {
	state := NewAnthropicEventToResponsesState(testRuntime())
	state.Model = "claude-sonnet-4-5"

	var types []string
	feed := func(evt *AnthropicStreamEvent) {
		for _, out := range AnthropicEventToResponsesEvents(testRuntime(), evt, state) {
			types = append(types, out.Type)
		}
	}

	idx := 0
	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_1", Model: "claude-sonnet-4-5"}})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &idx, ContentBlock: &AnthropicContentBlock{Type: "text"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "text_delta", Text: "Hel"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "text_delta", Text: "lo"}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &idx})
	feed(&AnthropicStreamEvent{Type: "message_stop"})

	posOf := func(target string) int {
		for i, ty := range types {
			if ty == target {
				return i
			}
		}
		return -1
	}

	partAdded := posOf("response.content_part.added")
	firstDelta := posOf("response.output_text.delta")

	if partAdded < 0 {
		t.Fatalf("response.content_part.added was not emitted; got %v", types)
	}
	if firstDelta < 0 {
		t.Fatalf("response.output_text.delta was not emitted; got %v", types)
	}
	if partAdded > firstDelta {
		t.Errorf("content_part.added must precede the first output_text.delta; got %v", types)
	}
	if posOf("response.content_part.done") < 0 {
		t.Errorf("response.content_part.done was not emitted; got %v", types)
	}
}

// TestAnthropicEventToResponses_DoneEventsCarryFullText stops a text block
// after two fragments. Both text-done and part-done events must appear with
// the complete concatenated text in their inspected fields.
func TestAnthropicEventToResponses_DoneEventsCarryFullText(t *testing.T) {
	state := NewAnthropicEventToResponsesState(testRuntime())
	state.Model = "claude-sonnet-4-5"

	var events []ResponsesStreamEvent
	feed := func(evt *AnthropicStreamEvent) {
		events = append(events, AnthropicEventToResponsesEvents(testRuntime(), evt, state)...)
	}

	idx := 0
	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_1"}})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &idx, ContentBlock: &AnthropicContentBlock{Type: "text"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "text_delta", Text: "Hello "}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "text_delta", Text: "world"}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &idx})

	const want = "Hello world"
	var sawTextDone, sawPartDone bool
	for _, e := range events {
		switch e.Type {
		case "response.output_text.done":
			sawTextDone = true
			if e.Text != want {
				t.Errorf("output_text.done text = %q, want %q", e.Text, want)
			}
		case "response.content_part.done":
			sawPartDone = true
			if e.Part == nil || e.Part.Text != want {
				t.Errorf("content_part.done part = %+v, want text %q", e.Part, want)
			}
		}
	}
	if !sawTextDone || !sawPartDone {
		t.Errorf("missing done events: output_text.done=%v content_part.done=%v", sawTextDone, sawPartDone)
	}
}

// TestAnthropicEventToResponses_CompletedCarriesOutput completes a single text
// stream. Its terminal snapshot must contain a message with content and the
// expected text value in the first part.
func TestAnthropicEventToResponses_CompletedCarriesOutput(t *testing.T) {
	state := NewAnthropicEventToResponsesState(testRuntime())
	state.Model = "claude-sonnet-4-5"

	var events []ResponsesStreamEvent
	feed := func(evt *AnthropicStreamEvent) {
		events = append(events, AnthropicEventToResponsesEvents(testRuntime(), evt, state)...)
	}

	idx := 0
	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_1"}})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &idx, ContentBlock: &AnthropicContentBlock{Type: "text"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "text_delta", Text: "4826"}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &idx})
	feed(&AnthropicStreamEvent{Type: "message_stop"})

	var completed *ResponsesStreamEvent
	for i := range events {
		if events[i].Type == "response.completed" {
			completed = &events[i]
		}
	}
	if completed == nil || completed.Response == nil {
		t.Fatalf("response.completed was not emitted")
	}
	if len(completed.Response.Output) == 0 {
		t.Fatalf("response.completed carries an empty output; clients would see no result")
	}
	msg := completed.Response.Output[0]
	if msg.Type != "message" || len(msg.Content) == 0 {
		t.Fatalf("output[0] = %+v, want a message with content", msg)
	}
	if msg.Content[0].Text != "4826" {
		t.Errorf("output[0].content[0].text = %q, want %q", msg.Content[0].Text, "4826")
	}
}

// TestAnthropicEventToResponses_ToolCallCompletedCarriesArguments splits
// function JSON across two deltas and closes the stream. The terminal output
// must contain a function call with its complete argument text and selected
// name.
func TestAnthropicEventToResponses_ToolCallCompletedCarriesArguments(t *testing.T) {
	state := NewAnthropicEventToResponsesState(testRuntime())
	state.Model = "claude-sonnet-4-5"

	var events []ResponsesStreamEvent
	feed := func(evt *AnthropicStreamEvent) {
		events = append(events, AnthropicEventToResponsesEvents(testRuntime(), evt, state)...)
	}

	idx := 0
	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_1"}})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &idx, ContentBlock: &AnthropicContentBlock{
		Type: "tool_use", ID: "toolu_1", Name: "get_weather",
	}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{
		Type: "input_json_delta", PartialJSON: `{"city":`,
	}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{
		Type: "input_json_delta", PartialJSON: `"SH"}`,
	}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &idx})
	feed(&AnthropicStreamEvent{Type: "message_stop"})

	var completed *ResponsesStreamEvent
	for i := range events {
		if events[i].Type == "response.completed" {
			completed = &events[i]
		}
	}
	if completed == nil || completed.Response == nil || len(completed.Response.Output) == 0 {
		t.Fatalf("response.completed carries no output")
	}
	fc := completed.Response.Output[0]
	if fc.Type != "function_call" {
		t.Fatalf("output[0].type = %q, want function_call", fc.Type)
	}
	if fc.Arguments != `{"city":"SH"}` {
		t.Errorf("arguments = %q, want %q", fc.Arguments, `{"city":"SH"}`)
	}
	if fc.Name != "get_weather" {
		t.Errorf("name = %q, want get_weather", fc.Name)
	}
}

// TestAnthropicEventToResponses_ThinkingAfterTextKeepsMessageOutput places
// thinking after a stopped text block. The terminal snapshot must retain
// message then reasoning outputs, the message text, and two distinct added-
// item indexes.
func TestAnthropicEventToResponses_ThinkingAfterTextKeepsMessageOutput(t *testing.T) {
	state := NewAnthropicEventToResponsesState(testRuntime())
	state.Model = "claude-sonnet-4-5"

	var events []ResponsesStreamEvent
	feed := func(evt *AnthropicStreamEvent) {
		events = append(events, AnthropicEventToResponsesEvents(testRuntime(), evt, state)...)
	}

	i0, i1 := 0, 1
	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_1"}})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &i0, ContentBlock: &AnthropicContentBlock{Type: "text"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &i0, Delta: &AnthropicDelta{Type: "text_delta", Text: "answer"}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &i0})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &i1, ContentBlock: &AnthropicContentBlock{Type: "thinking"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &i1, Delta: &AnthropicDelta{Type: "thinking_delta", Thinking: "hmm"}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &i1})
	feed(&AnthropicStreamEvent{Type: "message_stop"})

	var completed *ResponsesStreamEvent
	for i := range events {
		if events[i].Type == "response.completed" {
			completed = &events[i]
		}
	}
	if completed == nil || completed.Response == nil {
		t.Fatalf("response.completed was not emitted")
	}
	outputs := completed.Response.Output
	if len(outputs) != 2 {
		t.Fatalf("response.completed carries %d output items, want 2 (message + reasoning): %+v", len(outputs), outputs)
	}
	if outputs[0].Type != "message" {
		t.Fatalf("output[0].type = %q, want message", outputs[0].Type)
	}
	if len(outputs[0].Content) != 1 || outputs[0].Content[0].Text != "answer" {
		t.Errorf("assistant text lost: output[0].content = %+v", outputs[0].Content)
	}
	if outputs[1].Type != "reasoning" {
		t.Errorf("output[1].type = %q, want reasoning", outputs[1].Type)
	}

	// Message and reasoning items need distinct output indexes.
	var addedIndexes []int
	for _, evt := range events {
		if evt.Type == "response.output_item.added" {
			addedIndexes = append(addedIndexes, evt.OutputIndex)
		}
	}
	if len(addedIndexes) != 2 || addedIndexes[0] == addedIndexes[1] {
		t.Errorf("output_item.added indexes = %v, want two distinct values", addedIndexes)
	}
}

// TestAnthropicEventToResponses_MultipleTextBlocksAdvanceContentIndex feeds
// two stopped text blocks under one message. It checks added-part indexes
// zero/one and groups delta text by those indexes to retain both
// contributions.
func TestAnthropicEventToResponses_MultipleTextBlocksAdvanceContentIndex(t *testing.T) {
	state := NewAnthropicEventToResponsesState(testRuntime())
	state.Model = "claude-sonnet-4-5"

	var events []ResponsesStreamEvent
	feed := func(evt *AnthropicStreamEvent) {
		events = append(events, AnthropicEventToResponsesEvents(testRuntime(), evt, state)...)
	}

	i0, i1 := 0, 1
	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_1"}})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &i0, ContentBlock: &AnthropicContentBlock{Type: "text"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &i0, Delta: &AnthropicDelta{Type: "text_delta", Text: "first"}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &i0})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &i1, ContentBlock: &AnthropicContentBlock{Type: "text"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &i1, Delta: &AnthropicDelta{Type: "text_delta", Text: "second"}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &i1})
	feed(&AnthropicStreamEvent{Type: "message_stop"})

	var partAdded []int
	for _, evt := range events {
		if evt.Type == "response.content_part.added" {
			partAdded = append(partAdded, evt.ContentIndex)
		}
	}
	if len(partAdded) != 2 {
		t.Fatalf("content_part.added emitted %d times, want 2", len(partAdded))
	}
	if partAdded[0] != 0 || partAdded[1] != 1 {
		t.Errorf("content_part.added indexes = %v, want [0 1]", partAdded)
	}

	// Group the asserted delta text by the emitted content position.
	byIndex := map[int]string{}
	for _, evt := range events {
		if evt.Type == "response.output_text.delta" {
			byIndex[evt.ContentIndex] += evt.Delta
		}
	}
	if byIndex[0] != "first" || byIndex[1] != "second" {
		t.Errorf("output_text.delta grouped by content_index = %v, want {0:first 1:second}", byIndex)
	}
}

// TestAnthropicEventToResponses_ItemLifecycleIsBalanced covers five
// text/thinking/tool block orders. Added/done counts balance per item ID, and
// terminal output IDs must match announcement order with one output per added
// item.
func TestAnthropicEventToResponses_ItemLifecycleIsBalanced(t *testing.T) {
	for _, tc := range []struct {
		name string // block-order fixture case label
		// blocks supplies one sequential block lifecycle per entry. The fixture
		// injects matching deltas and stops so identity balance can be compared with
		// terminal order.
		blocks []*AnthropicContentBlock // ordered source block starts
	}{
		{"text then thinking", []*AnthropicContentBlock{{Type: "text"}, {Type: "thinking"}}},
		{"thinking then text", []*AnthropicContentBlock{{Type: "thinking"}, {Type: "text"}}},
		{"text then tool_use", []*AnthropicContentBlock{{Type: "text"}, {Type: "tool_use", ID: "toolu_1", Name: "t"}}},
		{"text thinking text", []*AnthropicContentBlock{{Type: "text"}, {Type: "thinking"}, {Type: "text"}}},
		{"thinking text tool_use", []*AnthropicContentBlock{{Type: "thinking"}, {Type: "text"}, {Type: "tool_use", ID: "toolu_2", Name: "t"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := NewAnthropicEventToResponsesState(testRuntime())
			state.Model = "claude-sonnet-4-5"

			var events []ResponsesStreamEvent
			feed := func(evt *AnthropicStreamEvent) {
				events = append(events, AnthropicEventToResponsesEvents(testRuntime(), evt, state)...)
			}

			feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_1"}})
			for i, block := range tc.blocks {
				idx := i
				feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &idx, ContentBlock: block})
				switch block.Type {
				case "text":
					feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "text_delta", Text: "t"}})
				case "thinking":
					feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "thinking_delta", Thinking: "r"}})
				case "tool_use":
					feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: "{}"}})
				}
				feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &idx})
			}
			feed(&AnthropicStreamEvent{Type: "message_stop"})

			openIDs := map[string]int{}
			var order []string
			for _, evt := range events {
				switch evt.Type {
				case "response.output_item.added":
					if evt.Item == nil {
						t.Fatalf("output_item.added without item")
					}
					openIDs[evt.Item.ID]++
					order = append(order, evt.Item.ID)
				case "response.output_item.done":
					if evt.Item == nil {
						t.Fatalf("output_item.done without item")
					}
					openIDs[evt.Item.ID]--
				}
			}
			for id, n := range openIDs {
				if n != 0 {
					t.Errorf("item %s: output_item.added/done imbalance %+d", id, n)
				}
			}

			var completed *ResponsesStreamEvent
			for i := range events {
				if events[i].Type == "response.completed" {
					completed = &events[i]
				}
			}
			if completed == nil || completed.Response == nil {
				t.Fatalf("response.completed was not emitted")
			}
			if len(completed.Response.Output) != len(order) {
				t.Fatalf("response.completed carries %d outputs, want %d (one per announced item)",
					len(completed.Response.Output), len(order))
			}
			for i, id := range order {
				if completed.Response.Output[i].ID != id {
					t.Errorf("output[%d].id = %q, want %q (announcement order must be preserved)",
						i, completed.Response.Output[i].ID, id)
				}
			}
		})
	}
}

// TestResponsesEventToSSE_CustomToolCallItemCarriesAllFields serializes one
// completed custom item. The frame must contain its client kind, call ID,
// name, and input strings in the inspected JSON fields.
func TestResponsesEventToSSE_CustomToolCallItemCarriesAllFields(t *testing.T) {
	evt := ResponsesStreamEvent{
		Type:        "response.output_item.done",
		OutputIndex: 1,
		Item: &ResponsesOutput{
			Type:   "custom_tool_call",
			ID:     "item_1",
			CallID: "call_1",
			Name:   "exec",
			Input:  "dir",
			Status: "completed",
		},
	}

	sse, err := ResponsesEventToSSE(evt)
	require.NoError(t, err)

	assert.Contains(t, sse, `"call_id":"call_1"`)
	assert.Contains(t, sse, `"name":"exec"`)
	assert.Contains(t, sse, `"input":"dir"`)
	assert.Contains(t, sse, `"type":"custom_tool_call"`)
}

// TestStream_SSEWireComplete collects a reasoning/tool Chat stream and
// serializes its Responses events. The function-item added frame must exist
// and contain the inspected empty arguments member plus call correlation.
func TestStream_SSEWireComplete(t *testing.T) {
	events := collectStreamEvents(t, []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"plan"}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"exec","arguments":"{}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	})

	var addedLine string
	for _, e := range events {
		sse, err := ResponsesEventToSSE(e)
		require.NoError(t, err)
		if e.Type == "response.output_item.added" && e.Item != nil && e.Item.Type == "function_call" {
			addedLine = sse
		}
	}
	require.NotEmpty(t, addedLine)
	// The serialized added event must include an empty arguments field.
	require.True(t, strings.Contains(addedLine, `"arguments":""`), "added line missing arguments: %s", addedLine)
	require.Contains(t, addedLine, `"call_id":"call_a"`)
}

// TestAnthropicToResponsesResponse_CacheCreation combines all input buckets
// with output in a buffered reply. Assertions check normalized input and the
// top-level cache-creation counter in the returned usage.
func TestAnthropicToResponsesResponse_CacheCreation(t *testing.T) {
	resp := AnthropicResponse{
		ID:    "msg_test",
		Type:  "message",
		Role:  "assistant",
		Model: "claude-opus-4-6",
		Usage: AnthropicUsage{
			InputTokens:              10,
			OutputTokens:             5,
			CacheReadInputTokens:     4,
			CacheCreationInputTokens: 6,
		},
		StopReason: AnthropicStopReasonPtr("end_turn"),
	}

	out := AnthropicToResponsesResponse(testRuntime(), &resp)

	require.NotNil(t, out.Usage)
	assert.Equal(t, 20, out.Usage.InputTokens, "total = input(10) + cache_read(4) + cache_creation(6)")
	assert.Equal(t, 6, out.Usage.CacheCreationInputTokens, "cache creation must round-trip")
}

// TestAnthropicToResponsesResponse_StampsCreatedAt converts a buffered
// Messages reply without a source timestamp. The result must contain a
// positive creation time from the supplied runtime.
func TestAnthropicToResponsesResponse_StampsCreatedAt(t *testing.T) {
	out := AnthropicToResponsesResponse(testRuntime(), &AnthropicResponse{
		ID:      "msg_1",
		Type:    "message",
		Role:    "assistant",
		Model:   "claude-sonnet-4-20250514",
		Content: []AnthropicContentBlock{{Type: "text", Text: "hi"}},
	})
	require.Greater(t, out.CreatedAt, int64(0),
		"Anthropic 响应不带时间戳，网关必须自己盖一个")
}

// TestAnthropicEventToResponsesStream_CreatedAtStableAcrossEvents decodes and
// completes a text stream using one constructed state. Created and completed
// snapshots must both carry the captured positive state timestamp.
func TestAnthropicEventToResponsesStream_CreatedAtStableAcrossEvents(t *testing.T) {
	state := NewAnthropicEventToResponsesState(testRuntime())
	state.Model = "claude-sonnet-4-20250514"
	require.Greater(t, state.Created, int64(0), "前提：state 早就采集了时间戳")

	var events []ResponsesStreamEvent
	for _, raw := range []string{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`{"type":"message_stop"}`,
	} {
		var evt AnthropicStreamEvent
		require.NoError(t, json.Unmarshal([]byte(raw), &evt))
		events = append(events, AnthropicEventToResponsesEvents(testRuntime(), &evt, state)...)
	}
	events = append(events, FinalizeAnthropicResponsesStream(state)...)

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

// TestAnthropicStreamingMaxTokens_MapsToIncomplete supplies max_tokens stop
// evidence before message stop. It checks retained stop state and the terminal
// incomplete label/status with max_output_tokens details.
func TestAnthropicStreamingMaxTokens_MapsToIncomplete(t *testing.T) {
	state := NewAnthropicEventToResponsesState(testRuntime())

	AnthropicEventToResponsesEvents(testRuntime(), &AnthropicStreamEvent{
		Type:    "message_start",
		Message: &AnthropicResponse{ID: "msg_test", Model: "claude-opus-4-6", Role: "assistant"},
	}, state)

	AnthropicEventToResponsesEvents(testRuntime(), &AnthropicStreamEvent{
		Type: "message_delta",
		Delta: &AnthropicDelta{
			StopReason: "max_tokens",
		},
		Usage: &AnthropicUsage{OutputTokens: 4096},
	}, state)

	require.Equal(t, "max_tokens", state.StopReason)

	events := AnthropicEventToResponsesEvents(testRuntime(), &AnthropicStreamEvent{
		Type: "message_stop",
	}, state)

	var completed *ResponsesStreamEvent
	for i := range events {
		if events[i].Type == "response.completed" || events[i].Type == "response.incomplete" {
			completed = &events[i]
			break
		}
	}
	require.NotNil(t, completed, "should have terminal event")
	assert.Equal(t, "response.incomplete", completed.Type)
	require.NotNil(t, completed.Response)
	assert.Equal(t, "incomplete", completed.Response.Status)
	require.NotNil(t, completed.Response.IncompleteDetails)
	assert.Equal(t, "max_output_tokens", completed.Response.IncompleteDetails.Reason)
}

// TestAnthropicStreamingMaxTokens_FinalizeMapsToIncompleteWithoutMessageStop
// finalizes after max_tokens without a source stop event. One incomplete
// terminal with details must appear, and repeated finalization must return no
// events.
func TestAnthropicStreamingMaxTokens_FinalizeMapsToIncompleteWithoutMessageStop(t *testing.T) {
	state := NewAnthropicEventToResponsesState(testRuntime())

	AnthropicEventToResponsesEvents(testRuntime(), &AnthropicStreamEvent{
		Type:    "message_start",
		Message: &AnthropicResponse{ID: "msg_test", Model: "claude-opus-4-6", Role: "assistant"},
	}, state)
	AnthropicEventToResponsesEvents(testRuntime(), &AnthropicStreamEvent{
		Type:  "message_delta",
		Delta: &AnthropicDelta{StopReason: "max_tokens"},
		Usage: &AnthropicUsage{OutputTokens: 4096},
	}, state)

	events := FinalizeAnthropicResponsesStream(state)
	require.Len(t, events, 1)
	assert.Equal(t, "response.incomplete", events[0].Type)
	require.NotNil(t, events[0].Response)
	assert.Equal(t, "incomplete", events[0].Response.Status)
	require.NotNil(t, events[0].Response.IncompleteDetails)
	assert.Equal(t, "max_output_tokens", events[0].Response.IncompleteDetails.Reason)
	assert.Empty(t, FinalizeAnthropicResponsesStream(state), "repeated finalization must be idempotent")
}

// TestAnthropicStreamingEndTurn_MapsToCompleted supplies end_turn evidence
// before message stop. A completed terminal snapshot must appear with
// completed status and no incomplete-details pointer.
func TestAnthropicStreamingEndTurn_MapsToCompleted(t *testing.T) {
	state := NewAnthropicEventToResponsesState(testRuntime())

	AnthropicEventToResponsesEvents(testRuntime(), &AnthropicStreamEvent{
		Type:    "message_start",
		Message: &AnthropicResponse{ID: "msg_test", Model: "claude-opus-4-6", Role: "assistant"},
	}, state)

	AnthropicEventToResponsesEvents(testRuntime(), &AnthropicStreamEvent{
		Type:  "message_delta",
		Delta: &AnthropicDelta{StopReason: "end_turn"},
		Usage: &AnthropicUsage{OutputTokens: 100},
	}, state)

	events := AnthropicEventToResponsesEvents(testRuntime(), &AnthropicStreamEvent{
		Type: "message_stop",
	}, state)

	var completed *ResponsesStreamEvent
	for i := range events {
		if events[i].Type == "response.completed" {
			completed = &events[i]
			break
		}
	}
	require.NotNil(t, completed)
	assert.Equal(t, "completed", completed.Response.Status)
	assert.Nil(t, completed.Response.IncompleteDetails)
}
