package grok

import (
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestSanitizeGrokResponsesModelInputNormalizesReplayHistory mixes complete
// and incomplete assistant messages with reasoning replay. It asserts
// incomplete metadata and null reasoning content disappear while complete
// message metadata and reasoning summary text remain.
func TestSanitizeGrokResponsesModelInputNormalizesReplayHistory(t *testing.T) {
	body := []byte(`{
		"input":[
			{"role":"system","content":"rules"},
			{"type":"message","role":"assistant","id":"msg_partial","content":[{"type":"output_text","text":"done"}]},
			{"type":"reasoning","id":"rs_1","status":"completed","content":null,"summary":[{"type":"summary_text","text":"keep"}]},
			{"type":"message","role":"assistant","id":"msg_complete","status":"completed","content":[{"type":"output_text","text":"kept"}]}
		]}`)

	patched, err := (BodyCodec{
		NewID: uuid.NewString,
	}).SanitizeGrokResponsesModelInput(body)
	require.NoError(t, err)
	require.Equal(t, "message", gjson.GetBytes(patched, "input.0.type").String())
	require.Equal(t, "done", gjson.GetBytes(patched, "input.1.content").String())
	require.False(t, gjson.GetBytes(patched, "input.1.id").Exists())
	require.False(t, gjson.GetBytes(patched, "input.2.status").Exists())
	require.False(t, gjson.GetBytes(patched, "input.2.content").Exists())
	require.Equal(t, "keep", gjson.GetBytes(patched, "input.2.summary.0.text").String())
	require.Equal(t, "msg_complete", gjson.GetBytes(patched, "input.3.id").String())
	require.Equal(t, "completed", gjson.GetBytes(patched, "input.3.status").String())
}

// TestSanitizeGrokResponsesModelInputStripsOnlyNonPairCallIDs includes
// nonpair output records beside a function call and reply. It asserts stray
// call IDs are removed from the former and retained on both paired tool
// items.
func TestSanitizeGrokResponsesModelInputStripsOnlyNonPairCallIDs(t *testing.T) {
	body := []byte(`{"input":[
		{"type":"message","role":"user","call_id":"remove_message","content":"continue"},
		{"type":"reasoning","call_id":"remove_reasoning","summary":[]},
		{"type":"image_generation_call","call_id":"remove_image","id":"ig_1","status":"completed"},
		{"type":"function_call","call_id":"keep_function","name":"lookup","arguments":"{}"},
		{"type":"function_call_output","call_id":"keep_function","output":"ok"}
	]}`)

	patched, err := (BodyCodec{
		NewID: uuid.NewString,
	}).SanitizeGrokResponsesModelInput(body)
	require.NoError(t, err)
	for i := range 3 {
		require.False(t, gjson.GetBytes(patched, "input."+strconv.Itoa(i)+".call_id").Exists())
	}
	require.Equal(t, "keep_function", gjson.GetBytes(patched, "input.3.call_id").String())
	require.Equal(t, "keep_function", gjson.GetBytes(patched, "input.4.call_id").String())
}

// TestSanitizeGrokResponsesModelInputPairsCallsInTwoPasses places a reply
// before its call and includes alternate tool shapes. It asserts pairing,
// argument/output normalization, large integer preservation, and retention of
// an explicit orphan reply identifier.
func TestSanitizeGrokResponsesModelInputPairsCallsInTwoPasses(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"function_call_output","output":{"before":true,"large_id":9007199254740993}},
			{"type":"function_call","function":{"name":"lookup","arguments":{"query":"x","large_id":9007199254740993}}},
			{"type":"custom_tool_call","id":"custom_1","name":"apply_patch","input":"patch"},
			{"type":"custom_tool_call_output","tool_call_id":"custom_1","output":["ok"]},
			{"type":"tool_search_call","id":"search_1","arguments":{"query":"repo"}},
			{"type":"tool_search_output","call_id":"search_1","results":{"groups":["repo"]}},
			{"role":"tool","tool_call_id":"orphan","content":{"value":1}}
		]}`)

	patched, err := (BodyCodec{
		NewID: uuid.NewString,
	}).SanitizeGrokResponsesModelInput(body)
	require.NoError(t, err)
	generated := gjson.GetBytes(patched, "input.1.call_id").String()
	require.NotEmpty(t, generated)
	require.Equal(t, generated, gjson.GetBytes(patched, "input.0.call_id").String())
	require.JSONEq(t, `{"before":true,"large_id":9007199254740993}`, gjson.GetBytes(patched, "input.0.output").String())
	require.Equal(t, "9007199254740993", gjson.Get(gjson.GetBytes(patched, "input.0.output").String(), "large_id").Raw)
	require.Equal(t, "lookup", gjson.GetBytes(patched, "input.1.name").String())
	require.JSONEq(t, `{"query":"x","large_id":9007199254740993}`, gjson.GetBytes(patched, "input.1.arguments").String())
	require.Equal(t, "9007199254740993", gjson.Get(gjson.GetBytes(patched, "input.1.arguments").String(), "large_id").Raw)
	customCallID := gjson.GetBytes(patched, "input.2.call_id").String()
	require.NotEmpty(t, customCallID)
	require.NotEqual(t, "custom_1", customCallID)
	require.JSONEq(t, `{"input":"patch"}`, gjson.GetBytes(patched, "input.2.arguments").String())
	require.Equal(t, customCallID, gjson.GetBytes(patched, "input.3.call_id").String())
	require.JSONEq(t, `["ok"]`, gjson.GetBytes(patched, "input.3.output").String())
	require.Equal(t, "tool_search", gjson.GetBytes(patched, "input.4.name").String())
	searchCallID := gjson.GetBytes(patched, "input.4.call_id").String()
	require.NotEmpty(t, searchCallID)
	require.NotEqual(t, "search_1", searchCallID)
	require.Equal(t, searchCallID, gjson.GetBytes(patched, "input.5.call_id").String())
	require.JSONEq(t, `{"groups":["repo"]}`, gjson.GetBytes(patched, "input.5.output").String())
	require.Equal(t, "function_call_output", gjson.GetBytes(patched, "input.6.type").String())
	require.Equal(t, "orphan", gjson.GetBytes(patched, "input.6.call_id").String())
}

// TestSanitizeGrokResponsesModelInputMapsAllItemAliasesAndRejectsConflicts
// supplies a unique item alias and an item ID reused by two calls. It asserts
// the unique alias pairs and the conflicting output receives a distinct
// orphan identifier.
func TestSanitizeGrokResponsesModelInputMapsAllItemAliasesAndRejectsConflicts(t *testing.T) {
	body := []byte(`{"input":[
		{"type":"function_call","id":"fc_alias","call_id":"call_canonical","name":"one","arguments":"{}"},
		{"type":"function_call_output","id":"fc_alias","output":"ok"},
		{"type":"function_call","id":"duplicate_item","name":"two","arguments":"{}"},
		{"type":"function_call","id":"duplicate_item","name":"three","arguments":"{}"},
		{"type":"function_call_output","id":"duplicate_item","output":"ambiguous"}
	]}`)

	patched, err := (BodyCodec{
		NewID: uuid.NewString,
	}).SanitizeGrokResponsesModelInput(body)
	require.NoError(t, err)
	require.Equal(t, "call_canonical", gjson.GetBytes(patched, "input.0.call_id").String())
	require.Equal(t, "call_canonical", gjson.GetBytes(patched, "input.1.call_id").String())
	require.False(t, gjson.GetBytes(patched, "input.0.id").Exists())
	secondCallID := gjson.GetBytes(patched, "input.2.call_id").String()
	thirdCallID := gjson.GetBytes(patched, "input.3.call_id").String()
	ambiguousOutputID := gjson.GetBytes(patched, "input.4.call_id").String()
	require.NotEmpty(t, secondCallID)
	require.NotEmpty(t, thirdCallID)
	require.NotEmpty(t, ambiguousOutputID)
	require.NotEqual(t, "duplicate_item", secondCallID)
	require.NotEqual(t, secondCallID, thirdCallID)
	require.NotEqual(t, secondCallID, ambiguousOutputID)
	require.NotEqual(t, thirdCallID, ambiguousOutputID)
}

// TestSanitizeGrokResponsesModelInputPreservesGrokShellOutputImages includes
// two consecutive replies with separate top-level media arrays. It asserts
// their text replies remain contiguous and a following user message retains
// each labelled call's image URL.
func TestSanitizeGrokResponsesModelInputPreservesGrokShellOutputImages(t *testing.T) {
	body := []byte(`{"input":[
		{"type":"function_call","call_id":"call_a","name":"read_file","arguments":"{}"},
		{"type":"function_call","call_id":"call_b","name":"read_file","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_a","content":"Read image file: /tmp/a.png","images":[{"type":"image","url":"data:image/png;base64,QUE="}]},
		{"type":"function_call_output","call_id":"call_b","content":"Read image file: /tmp/b.png","images":[{"type":"image","url":"data:image/jpeg;base64,QkI="}]}
	]}`)

	patched, err := (BodyCodec{
		NewID: uuid.NewString,
	}).SanitizeGrokResponsesModelInput(body)
	require.NoError(t, err)
	require.Equal(t, "function_call_output", gjson.GetBytes(patched, "input.2.type").String())
	require.Equal(t, "Read image file: /tmp/a.png", gjson.GetBytes(patched, "input.2.output").String())
	require.False(t, gjson.GetBytes(patched, "input.2.images").Exists())
	require.Equal(t, "function_call_output", gjson.GetBytes(patched, "input.3.type").String())
	require.Equal(t, "message", gjson.GetBytes(patched, "input.4.type").String())
	require.Equal(t, "user", gjson.GetBytes(patched, "input.4.role").String())
	require.Equal(t, "[Tool output media for call call_a]", gjson.GetBytes(patched, "input.4.content.0.text").String())
	require.Equal(t, "input_image", gjson.GetBytes(patched, "input.4.content.1.type").String())
	require.Equal(t, "data:image/png;base64,QUE=", gjson.GetBytes(patched, "input.4.content.1.image_url").String())
	require.Equal(t, "[Tool output media for call call_b]", gjson.GetBytes(patched, "input.4.content.2.text").String())
	require.Equal(t, "data:image/jpeg;base64,QkI=", gjson.GetBytes(patched, "input.4.content.3.image_url").String())
}

// TestSanitizeGrokResponsesModelInputPreservesGrok105StructuredOutputImages
// supplies text and image parts inside structured tool output. It asserts
// readable text is separated from base64 media and the image survives in a
// subsequent user input-image message.
func TestSanitizeGrokResponsesModelInputPreservesGrok105StructuredOutputImages(t *testing.T) {
	body := []byte(`{"input":[
		{"type":"function_call","call_id":"call_read","name":"read_file","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_read","output":[
			{"type":"input_text","text":"Read image file: /tmp/example.png"},
			{"type":"input_image","detail":"auto","image_url":"data:image/png;base64,QUE="}
		]}
	]}`)

	patched, err := (BodyCodec{
		NewID: uuid.NewString,
	}).SanitizeGrokResponsesModelInput(body)
	require.NoError(t, err)
	require.Equal(t, "function_call_output", gjson.GetBytes(patched, "input.1.type").String())
	require.Equal(t, "Read image file: /tmp/example.png", gjson.GetBytes(patched, "input.1.output").String())
	require.NotContains(t, gjson.GetBytes(patched, "input.1.output").String(), "base64")
	require.Equal(t, "message", gjson.GetBytes(patched, "input.2.type").String())
	require.Equal(t, "input_image", gjson.GetBytes(patched, "input.2.content.1.type").String())
	require.Equal(t, "data:image/png;base64,QUE=", gjson.GetBytes(patched, "input.2.content.1.image_url").String())
}

// TestSanitizeGrokResponsesModelInputSkipsInvalidOutputImages supplies empty-
// base64 and missing-URL media entries beside a normal user message. It
// asserts no extra media message is emitted and both original logical
// reply/message items remain.
func TestSanitizeGrokResponsesModelInputSkipsInvalidOutputImages(t *testing.T) {
	body := []byte(`{"input":[
		{"type":"function_call_output","call_id":"call_empty","output":"done","images":[{"type":"image","url":"data:image/png;base64,"},{}]},
		{"role":"user","content":"continue"}
	]}`)

	patched, err := (BodyCodec{
		NewID: uuid.NewString,
	}).SanitizeGrokResponsesModelInput(body)
	require.NoError(t, err)
	require.Len(t, gjson.GetBytes(patched, "input").Array(), 2)
	require.Equal(t, "function_call_output", gjson.GetBytes(patched, "input.0.type").String())
	require.Equal(t, "message", gjson.GetBytes(patched, "input.1.type").String())
}
