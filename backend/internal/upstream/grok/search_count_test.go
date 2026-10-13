package grok

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCountGrokNativeSearchCallsFromJSONBytes supplies empty output and mixed
// native/function call items. It asserts recognized web, X, and tool-search
// items contribute while an ordinary lookup function contributes no search
// call.
func TestCountGrokNativeSearchCallsFromJSONBytes(t *testing.T) {
	t.Parallel()
	require.Equal(t, 0, CountGrokNativeSearchCallsFromJSONBytes(nil))
	require.Equal(t, 0, CountGrokNativeSearchCallsFromJSONBytes([]byte(`{"output":[]}`)))
	body := []byte(`{"output":[
		{"type":"web_search_call","id":"ws1","status":"completed"},
		{"type":"x_search_call","id":"xs1"},
		{"type":"function_call","name":"tool_search","call_id":"ts1"},
		{"type":"function_call","name":"lookup","call_id":"other"}
	]}`)
	require.Equal(t, 3, CountGrokNativeSearchCallsFromJSONBytes(body))
}

// TestCountGrokNativeSearchCallsFromJSONBytes_PrefersNestedResponse repeats
// search output in root and nested response locations. It asserts only the
// nested array's two calls are counted, preventing aggregation of the
// duplicate compatibility copy.
func TestCountGrokNativeSearchCallsFromJSONBytes_PrefersNestedResponse(t *testing.T) {
	t.Parallel()
	body := []byte(`{"output":[{"type":"web_search_call","id":"duplicate"}],"response":{"output":[{"type":"web_search_call","id":"duplicate"},{"type":"x_search_call","id":"xs1"}]}}`)
	require.Equal(t, 2, CountGrokNativeSearchCallsFromJSONBytes(body))
}

// TestCountGrokNativeSearchCallsFromJSONBytes_FallsBackWhenNestedOutputNull
// supplies a null nested output beside a usable root array. It asserts the
// root search item is counted when the preferred location is not an array.
func TestCountGrokNativeSearchCallsFromJSONBytes_FallsBackWhenNestedOutputNull(t *testing.T) {
	t.Parallel()
	body := []byte(`{"output":[{"type":"web_search_call","id":"ws1"}],"response":{"output":null}}`)
	require.Equal(t, 1, CountGrokNativeSearchCallsFromJSONBytes(body))
}

// TestCountGrokNativeSearchCallsFromSSEBodyDedups repeats one identified item
// completion and includes it again in final output. It asserts the whole SSE
// body counts that call once plus the distinct X-search call.
func TestCountGrokNativeSearchCallsFromSSEBodyDedups(t *testing.T) {
	t.Parallel()
	sse := stringsJoin(
		`data: {"type":"response.output_item.done","item":{"type":"web_search_call","id":"ws1","call_id":"c1"}}`,
		`data: {"type":"response.output_item.done","item":{"type":"web_search_call","id":"ws1","call_id":"c1"}}`,
		`data: {"type":"response.completed","response":{"output":[{"type":"web_search_call","id":"ws1","call_id":"c1"},{"type":"x_search_call","id":"xs1","call_id":"c2"}]}}`,
	)
	require.Equal(t, 2, CountGrokNativeSearchCallsFromSSEBody(sse))
}

// TestCountGrokNativeSearchCallsInSSEDataDedup_LiveStreamPath processes item
// and final events through one seen map. It asserts incremental counts
// deduplicate their shared call ID while independent per-payload counts still
// include each representation.
func TestCountGrokNativeSearchCallsInSSEDataDedup_LiveStreamPath(t *testing.T) {
	t.Parallel()
	// Reuse one seen set as a live stream would, so item completion and the final
	// output representation can reference the same search call without increasing both counts.
	seen := make(map[string]struct{})
	done := []byte(`{"type":"response.output_item.done","item":{"type":"web_search_call","id":"ws1","call_id":"c1"}}`)
	completed := []byte(`{"type":"response.completed","response":{"output":[{"type":"web_search_call","id":"ws1","call_id":"c1"},{"type":"x_search_call","id":"xs1","call_id":"c2"}]}}`)
	require.Equal(t, 1, CountGrokNativeSearchCallsInSSEDataDedup(done, seen))
	require.Equal(t, 1, CountGrokNativeSearchCallsInSSEDataDedup(completed, seen))
	// Compare the independent helper on the same payloads to show why callers
	// retaining a whole-stream total need shared deduplication state across these two events.
	require.Equal(t, 1, CountGrokNativeSearchCallsInSSEData(done))
	require.Equal(t, 2, CountGrokNativeSearchCallsInSSEData(completed))
}

// TestCountGrokNativeSearchCallsInSSEDataDedup_NoIDStillDedups supplies an
// anonymous item completion and its final output. It asserts the synthetic
// key counts the first observation and prevents another count for the
// matching final array.
func TestCountGrokNativeSearchCallsInSSEDataDedup_NoIDStillDedups(t *testing.T) {
	t.Parallel()
	// Anonymous item completion and final output need one ordinal fingerprint.
	// Reuse the set so the final array proves it can recognize that prior anonymous observation.
	seen := make(map[string]struct{})
	done := []byte(`{"type":"response.output_item.done","item":{"type":"web_search_call"}}`)
	completed := []byte(`{"type":"response.completed","response":{"output":[{"type":"web_search_call"}]}}`)
	require.Equal(t, 1, CountGrokNativeSearchCallsInSSEDataDedup(done, seen))
	require.Equal(t, 0, CountGrokNativeSearchCallsInSSEDataDedup(completed, seen))
}

// TestCountGrokNativeSearchCallsInSSEDataDedup_MultipleNoIDCalls sends two
// anonymous item completions before their final array. It asserts each
// completion adds one ordinal and the final representation adds no further
// search count.
func TestCountGrokNativeSearchCallsInSSEDataDedup_MultipleNoIDCalls(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{})
	firstDone := []byte(`{"type":"response.output_item.done","item":{"type":"web_search_call"}}`)
	secondDone := []byte(`{"type":"response.output_item.done","item":{"type":"web_search_call"}}`)
	completed := []byte(`{"type":"response.completed","response":{"output":[{"type":"web_search_call"},{"type":"web_search_call"}]}}`)
	require.Equal(t, 1, CountGrokNativeSearchCallsInSSEDataDedup(firstDone, seen))
	require.Equal(t, 1, CountGrokNativeSearchCallsInSSEDataDedup(secondDone, seen))
	require.Equal(t, 0, CountGrokNativeSearchCallsInSSEDataDedup(completed, seen))
}

// stringsJoin concatenates test SSE lines with a blank-line frame separator
// after each.
func stringsJoin(lines ...string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n\n"
	}
	return out
}

// TestCountGrokNativeSearchCallsFromJSON_MessagesStyleBody supplies buffered
// Responses-shaped output alongside an assistant message. It asserts the
// single search item is counted and the nonsearch message does not affect
// that count.
func TestCountGrokNativeSearchCallsFromJSON_MessagesStyleBody(t *testing.T) {
	// Buffered Messages responses use the same Responses output counter.
	body := []byte(`{"id":"r1","output":[{"type":"web_search_call","id":"ws1"},{"type":"message","role":"assistant"}]}`)
	require.Equal(t, 1, CountGrokNativeSearchCallsFromJSONBytes(body))
}
