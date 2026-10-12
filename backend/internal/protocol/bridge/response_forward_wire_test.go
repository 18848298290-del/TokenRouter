package bridge

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestNormalizeResponsesStreamingTerminalOutputPreservesReportedItems checks reconstruction from done items.
// The delta accumulator models one reasoning item and one message, so it cannot retain
// the identities and structure of an arbitrary multi-item turn.
func TestNormalizeResponsesStreamingTerminalOutputPreservesReportedItems(t *testing.T) {
	doneItems := NewResponsesStreamOutputItems()

	doneItems.Observe([]byte(`{
		"type":"response.output_item.done",
		"output_index":0,
		"item":{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}],"encrypted_content":"opaque"}
	}`))
	doneItems.Observe([]byte(`{
		"type":"response.output_item.done",
		"output_index":1,
		"item":{"id":"msg_1","type":"message","status":"completed","phase":"final_answer","role":"assistant","content":[{"type":"output_text","text":"shipped","annotations":[],"logprobs":[]}]}
	}`))

	normalized, changed := NormalizeResponsesStreamingTerminalOutput(
		[]byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`),
		nil,
		doneItems,
		nil,
	)
	require.True(t, changed)

	output := gjson.GetBytes(normalized, "response.output")
	require.True(t, output.IsArray())
	require.Len(t, output.Array(), 2, "both reported items must survive")

	require.Equal(t, "reasoning", gjson.GetBytes(normalized, "response.output.0.type").String())
	require.Equal(t, "rs_1", gjson.GetBytes(normalized, "response.output.0.id").String())
	require.Equal(t, "opaque", gjson.GetBytes(normalized, "response.output.0.encrypted_content").String(),
		"fields the gateway does not model must survive verbatim")

	require.Equal(t, "message", gjson.GetBytes(normalized, "response.output.1.type").String())
	require.Equal(t, "msg_1", gjson.GetBytes(normalized, "response.output.1.id").String(),
		"the reported id must be reused, not regenerated")
	require.Equal(t, "completed", gjson.GetBytes(normalized, "response.output.1.status").String())
	require.Equal(t, "final_answer", gjson.GetBytes(normalized, "response.output.1.phase").String())
	require.Equal(t, "shipped", gjson.GetBytes(normalized, "response.output.1.content.0.text").String())
}

func TestResponsesStreamOutputItemsOrderByOutputIndex(t *testing.T) {
	doneItems := NewResponsesStreamOutputItems()
	doneItems.Observe([]byte(`{"type":"response.output_item.done","output_index":2,"item":{"id":"c","type":"message"}}`))
	doneItems.Observe([]byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"a","type":"reasoning"}}`))

	built, ok := doneItems.BuildOutput()
	require.True(t, ok)
	require.Equal(t, "a", gjson.GetBytes(built, "0.id").String())
	require.Equal(t, "c", gjson.GetBytes(built, "1.id").String())
}

// TestNormalizeResponsesStreamingTerminalOutputIgnoresNonDoneEvents verifies that added and delta events do not count as completed items.
func TestNormalizeResponsesStreamingTerminalOutputIgnoresNonDoneEvents(t *testing.T) {
	doneItems := NewResponsesStreamOutputItems()
	doneItems.Observe([]byte(`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message"}}`))
	doneItems.Observe([]byte(`{"type":"response.output_text.delta","output_index":0,"delta":"hi"}`))
	require.False(t, doneItems.HasItems())

	raw := []byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`)
	normalized, changed := NormalizeResponsesStreamingTerminalOutput(raw, nil, doneItems, nil)
	require.False(t, changed)
	require.Equal(t, string(raw), string(normalized))
}

// TestNormalizeResponsesStreamingTerminalOutputRepairsTruncatedOutput covers a terminal output that
// contains one synthetic item after the stream has reported two real items.
func TestNormalizeResponsesStreamingTerminalOutputRepairsTruncatedOutput(t *testing.T) {
	doneItems := NewResponsesStreamOutputItems()
	doneItems.Observe([]byte(`{
		"type":"response.output_item.done","output_index":0,
		"item":{"id":"rs_real","type":"reasoning","status":"in_progress","summary":[]}
	}`))
	doneItems.Observe([]byte(`{
		"type":"response.output_item.done","output_index":1,
		"item":{"id":"msg_real","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"shipped","annotations":[],"logprobs":[]}]}
	}`))

	normalized, changed := NormalizeResponsesStreamingTerminalOutput([]byte(`{
		"type":"response.completed",
		"response":{"status":"completed","output":[{"type":"message","role":"assistant","id":"msg_fabricated","status":"completed","content":[{"type":"output_text","text":"shipped","annotations":[],"logprobs":[]}]}]}
	}`), nil, doneItems, nil)
	require.True(t, changed)

	require.Len(t, gjson.GetBytes(normalized, "response.output").Array(), 2)
	require.Equal(t, "reasoning", gjson.GetBytes(normalized, "response.output.0.type").String())
	require.Equal(t, "rs_real", gjson.GetBytes(normalized, "response.output.0.id").String())
	require.Equal(t, "msg_real", gjson.GetBytes(normalized, "response.output.1.id").String(),
		"the id the stream reported must replace the fabricated one")
}

func TestNormalizeResponsesStreamingTerminalOutputLeavesCompleteOutputAlone(t *testing.T) {
	doneItems := NewResponsesStreamOutputItems()
	doneItems.Observe([]byte(`{
		"type":"response.output_item.done","output_index":0,
		"item":{"id":"msg_real","type":"message","status":"completed"}
	}`))

	raw := []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"msg_upstream","status":"completed","vendor":"keep"}]}}`)
	normalized, changed := NormalizeResponsesStreamingTerminalOutput(raw, nil, doneItems, nil)
	require.False(t, changed)
	require.Equal(t, string(raw), string(normalized))
}
