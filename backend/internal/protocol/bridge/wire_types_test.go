package bridge

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesUsageUnmarshalAcceptsChatUsageShape(t *testing.T) {
	var usage ResponsesUsage
	err := json.Unmarshal([]byte(`{"prompt_tokens":31,"completion_tokens":9,"prompt_tokens_details":{"cached_tokens":11},"completion_tokens_details":{"reasoning_tokens":4}}`), &usage)
	require.NoError(t, err)

	assert.Equal(t, 31, usage.InputTokens)
	assert.Equal(t, 9, usage.OutputTokens)
	assert.Equal(t, 40, usage.TotalTokens)
	require.NotNil(t, usage.InputTokensDetails)
	assert.Equal(t, 11, usage.InputTokensDetails.CachedTokens)
	require.NotNil(t, usage.OutputTokensDetails)
	assert.Equal(t, 4, usage.OutputTokensDetails.ReasoningTokens)
}

func TestResponsesUsageNestedCacheWritePresenceOverridesTopLevelAlias(t *testing.T) {
	tests := []struct {
		name       string
		nestedJSON string
		want       int
	}{
		{name: "explicit zero", nestedJSON: `{"cache_write_tokens":0}`, want: 0},
		{name: "nonzero", nestedJSON: `{"cache_write_tokens":7}`, want: 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var usage ResponsesUsage
			payload := []byte(`{"input_tokens":20,"output_tokens":2,"cache_creation_input_tokens":19,"input_tokens_details":` + tt.nestedJSON + `}`)
			require.NoError(t, json.Unmarshal(payload, &usage))
			require.Equal(t, tt.want, usage.CacheCreationInputTokens)
		})
	}
}

func TestResponsesTool_StrictFalseIsSerialized(t *testing.T) {
	strict := false
	payload, err := json.Marshal(ResponsesTool{
		Type:   "function",
		Strict: &strict,
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"function","strict":false}`, string(payload))
}

func TestWire_CreatedAtPresentEvenAtZero(t *testing.T) {
	resp := responseObjectOf(t, ResponsesStreamEvent{
		Type:     "response.created",
		Response: &ResponsesResponse{ID: "resp_1", Object: "response", Status: "in_progress"},
	})
	require.Contains(t, resp, "created_at", "created_at 不得带 omitempty")
	require.EqualValues(t, 0, resp["created_at"])
}

func TestResponsesStreamEvent_CreatedAtSurvivesUnmarshalRemarshal(t *testing.T) {
	upstream := []byte(`{"type":"response.completed","response":{"id":"resp_9","object":"response",` +
		`"created_at":1700000123,"model":"gpt-5.5","status":"completed","output":[]}}`)

	var evt ResponsesStreamEvent
	require.NoError(t, json.Unmarshal(upstream, &evt))
	require.EqualValues(t, 1700000123, evt.Response.CreatedAt)

	require.EqualValues(t, 1700000123, requireCreatedAt(t, responseObjectOf(t, evt)))
}

// TestWire_IndexFieldsPresentAtZero protects required zero-valued indexes from omitempty.
func TestWire_IndexFieldsPresentAtZero(t *testing.T) {
	m := marshalEvent(t, ResponsesStreamEvent{
		Type: "response.output_text.delta", OutputIndex: 0, ContentIndex: 0, ItemID: "msg_1", Delta: "hi",
	})
	require.Contains(t, m, "output_index")
	require.Contains(t, m, "content_index")
	require.EqualValues(t, 0, m["output_index"])

	r := marshalEvent(t, ResponsesStreamEvent{
		Type: "response.reasoning_summary_text.delta", OutputIndex: 0, SummaryIndex: 0, ItemID: "rs_1", Delta: "think",
	})
	require.Contains(t, r, "output_index")
	require.Contains(t, r, "summary_index")
}

// TestWire_FunctionCallItemAlwaysComplete checks required call_id, name, and arguments,
// including empty arguments on added events.
func TestWire_FunctionCallItemAlwaysComplete(t *testing.T) {
	added := marshalEvent(t, ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 1,
		Item:        &ResponsesOutput{Type: "function_call", ID: "fc_1", CallID: "call_a", Name: "exec", Status: "in_progress"},
	})
	item, ok := added["item"].(map[string]any)
	require.True(t, ok, "item must be an object")
	for _, k := range []string{"call_id", "name", "arguments"} {
		require.Containsf(t, item, k, "function_call item missing %q", k)
	}
	require.Equal(t, "", item["arguments"])
}

func TestWire_MessageItemContentAlwaysArray(t *testing.T) {
	m := marshalEvent(t, ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 0,
		Item:        &ResponsesOutput{Type: "message", ID: "msg_1", Role: "assistant", Status: "in_progress"},
	})
	item, ok := m["item"].(map[string]any)
	require.True(t, ok, "item must be an object")
	require.Contains(t, item, "content")
	_, ok = item["content"].([]any)
	require.True(t, ok, "content must be an array")
}

func TestWire_ReasoningItemSummaryAlwaysArray(t *testing.T) {
	m := marshalEvent(t, ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 0,
		Item:        &ResponsesOutput{Type: "reasoning", ID: "rs_1", Status: "in_progress"},
	})
	item, ok := m["item"].(map[string]any)
	require.True(t, ok, "item must be an object")
	require.Contains(t, item, "summary")
	_, ok = item["summary"].([]any)
	require.True(t, ok, "summary must be an array")
}

func TestWire_ContentPartCarriesAnnotationsLogprobs(t *testing.T) {
	m := marshalEvent(t, ResponsesStreamEvent{
		Type: "response.content_part.added", OutputIndex: 0, ContentIndex: 0, ItemID: "msg_1",
		Part: &ResponsesContentPart{Type: "output_text", Text: ""},
	})
	part, ok := m["part"].(map[string]any)
	require.True(t, ok, "part must be an object")
	require.Equal(t, "output_text", part["type"])
	require.Contains(t, part, "text")
	require.Contains(t, part, "annotations")
	require.Contains(t, part, "logprobs")
}

func TestWire_ArgumentsDonePresentEvenEmpty(t *testing.T) {
	m := marshalEvent(t, ResponsesStreamEvent{
		Type: "response.function_call_arguments.done", OutputIndex: 1, ItemID: "fc_1", CallID: "call_a", Name: "exec", Arguments: "",
	})
	require.Contains(t, m, "arguments")
	require.Equal(t, "", m["arguments"])
}

// TestWire_CustomToolCallInputIndexPresentAtZero checks output_index serialization
// for custom tool delta and done events at the first output position.
func TestWire_CustomToolCallInputIndexPresentAtZero(t *testing.T) {
	d := marshalEvent(t, ResponsesStreamEvent{
		Type: "response.custom_tool_call_input.delta", OutputIndex: 0, ItemID: "ct_1", Delta: "dir",
	})
	require.Contains(t, d, "output_index")
	require.EqualValues(t, 0, d["output_index"])
	require.Equal(t, "dir", d["delta"])

	done := marshalEvent(t, ResponsesStreamEvent{
		Type: "response.custom_tool_call_input.done", OutputIndex: 0, ItemID: "ct_1", CallID: "call_1", Name: "exec", Input: "dir",
	})
	require.Contains(t, done, "output_index")
	require.EqualValues(t, 0, done["output_index"])
	require.Equal(t, "dir", done["input"])
	require.NotContains(t, done, "delta")
}

// TestWire_UnknownEventFallsBackToDefault checks default serialization of a response event.
func TestWire_UnknownEventFallsBackToDefault(t *testing.T) {
	m := marshalEvent(t, ResponsesStreamEvent{
		Type:     "response.completed",
		Response: &ResponsesResponse{ID: "resp_1", Object: "response", Status: "completed"},
	})
	require.Contains(t, m, "response")
}

// TestResponsesOutputUnmarshal_ToolSearchObjectArguments checks that object arguments
// remain objects after a JSON round trip.
func TestResponsesOutputUnmarshal_ToolSearchObjectArguments(t *testing.T) {
	var item ResponsesOutput
	require.NoError(t, json.Unmarshal([]byte(`{
		"type":"tool_search_call",
		"id":"item_1",
		"call_id":"call_1",
		"execution":"client",
		"arguments":{"query":"gmail","limit":2}
	}`), &item))
	require.Equal(t, "tool_search_call", item.Type)
	require.Equal(t, `{"query":"gmail","limit":2}`, item.Arguments)

	wire, err := json.Marshal(item)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(wire, &decoded))
	args, ok := decoded["arguments"].(map[string]any)
	require.True(t, ok, "tool_search_call arguments must remain an object")
	require.Equal(t, "gmail", args["query"])
}

func TestResponsesResponseUnmarshal_ToolSearchObjectArguments(t *testing.T) {
	var response ResponsesResponse
	require.NoError(t, json.Unmarshal([]byte(`{
		"id":"response_1",
		"object":"response",
		"status":"completed",
		"output":[{
			"type":"tool_search_call",
			"id":"item_1",
			"call_id":"call_1",
			"arguments":{"query":"gmail"}
		}]
	}`), &response))
	require.Len(t, response.Output, 1)
	require.Equal(t, `{"query":"gmail"}`, response.Output[0].Arguments)
}

// TestResponsesStreamEventUnmarshal_ToolSearchObjectArguments decodes an argument object
// into the internal string representation used by stream conversion.
func TestResponsesStreamEventUnmarshal_ToolSearchObjectArguments(t *testing.T) {
	var event ResponsesStreamEvent
	require.NoError(t, json.Unmarshal([]byte(`{
		"type":"response.output_item.done",
		"item":{
			"type":"tool_search_call",
			"id":"item_1",
			"call_id":"call_1",
			"arguments":{"query":"gmail"}
		}
	}`), &event))
	require.NotNil(t, event.Item)
	require.Equal(t, `{"query":"gmail"}`, event.Item.Arguments)
}

func TestResponsesResponse_UnmarshalPreservesServiceTier(t *testing.T) {
	var resp ResponsesResponse
	require.NoError(t, json.Unmarshal([]byte(`{"id":"resp_1","object":"response","model":"gpt-5.5","status":"completed","service_tier":"flex","output":[]}`), &resp))
	require.Equal(t, "flex", resp.ServiceTier)
}
