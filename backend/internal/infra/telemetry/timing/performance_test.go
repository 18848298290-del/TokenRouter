package timing

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPerformanceSeparatesMetadataAndContent compares the two policies on the same
// stream. Usage and repeated terminal payloads must not extend its TPS interval.
func TestPerformanceSeparatesMetadataAndContent(t *testing.T) {
	p := NewPerformance(FirstOutput, 100)
	p.Observe(`{"type":"response.created"}`, "", 150)
	p.Observe(`{"type":"response.output_text.delta","item_id":"m","delta":"hello"}`, "", 300)
	p.Observe(`{"type":"response.output_text.delta","item_id":"m","delta":" world"}`, "", 800)
	p.Observe(`{"type":"response.output_text.done","item_id":"m","text":"hello world"}`, "", 950)
	p.Observe(`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"hello world"}]}],"usage":{"output_tokens":20}}}`, "", 1100)
	require.Equal(t, 200, *p.TTFTMs())
	require.Equal(t, 40.0, *p.TPS(20))
	require.Equal(t, int64(800), p.Values["upstream_last_output_ms"])
	p.Mode = FirstEvent
	require.Equal(t, 50, *p.TTFTMs())
	require.Equal(t, 40.0, *p.TPS(20))
}

// TestPerformanceRejectsIncompleteThroughput exercises partial usage and empty intervals.
// A completion marker alone cannot create a throughput estimate or useful output.
func TestPerformanceRejectsIncompleteThroughput(t *testing.T) {
	p := NewPerformance(FirstOutput, 0)
	p.Observe(`{"type":"message_start","message":{"usage":{"output_tokens":0}}}`, "", 0)
	require.Nil(t, p.TTFTMs())
	p.Observe(`{"type":"content_block_delta","delta":{"text":"a"}}`, "", 10)
	p.Observe(`{"type":"message_stop"}`, "", 20)
	require.Nil(t, p.TPS(10))
	p.UsageComplete = true
	require.Nil(t, p.TPS(10))
	p.Observe(`{"type":"content_block_delta","delta":{"text":"b"}}`, "", 10)
	require.Nil(t, p.TPS(10))
	p.Observe(`{"type":"error","error":{"message":"lost"}}`, "", 40)
	require.False(t, p.Complete)
	p.Observe("[DONE]", "", 50)
	require.False(t, p.Complete)
	require.Nil(t, p.TPS(10))
}

// TestPerformanceClassifiesNativeProtocols verifies equal treatment of content across
// native wire formats. Metadata, empty deltas and tool identifiers cannot start output.
func TestPerformanceClassifiesNativeProtocols(t *testing.T) {
	for _, tc := range []struct {
		name    string // protocol fixture name
		data    string // complete decoded event
		content bool   // expected useful evidence
	}{
		{"anthropic_metadata", `{"type":"message_start","message":{"id":"m"}}`, false},
		{"anthropic_reasoning", `{"type":"content_block_delta","delta":{"thinking":"why"}}`, true},
		{"empty_delta", `{"type":"content_block_delta","delta":{"type":"text_delta","text":""}}`, false},
		{"tool_id", `{"type":"content_block_start","content_block":{"type":"tool_use","id":"t"}}`, false},
		{"tool_name", `{"type":"content_block_start","content_block":{"type":"tool_use","id":"t","name":"lookup"}}`, true},
		{"chat_role", `{"choices":[{"delta":{"role":"assistant"}}]}`, false},
		{"chat_text", `{"choices":[{"delta":{"content":"hello"}}]}`, true},
		{"gemini", `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`, true},
		{"qoder", `{"body":"{\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPerformance(FirstOutput, 0)
			p.Observe(tc.data, "", 25)
			require.Equal(t, tc.content, p.TTFTMs() != nil)
		})
	}
}

// TestPerformanceSnapshotSurvivesRetryAndJSON preserves a captured exchange while a
// later provider starts. Stored JSON and detached completion retain the same observations.
func TestPerformanceSnapshotSurvivesRetryAndJSON(t *testing.T) {
	ctx, stages := WithStages(context.Background(), time.Now().Add(-time.Second), nil)
	stages.SetMode(FirstEvent)
	first := stages.BeginAttempt()
	stages.ObserveEvent(first, `{"type":"message_start"}`, "")
	stages.FinishAttempt(first)
	captured := CopyPerformance(context.Background(), ctx)
	before := PerformanceFromContext(captured)
	second := stages.BeginAttempt()
	stages.ObserveEvent(first, `{"type":"content_block_delta","delta":{"text":"late"}}`, "")
	require.Equal(t, second, stages.Attempt())
	require.Equal(t, before, PerformanceFromContext(captured))
	body, err := json.Marshal(before)
	require.NoError(t, err)
	var restored Performance
	require.NoError(t, json.Unmarshal(body, &restored))
	require.Equal(t, before.TTFTMs(), restored.TTFTMs())
	require.Nil(t, restored.TPS(100))
}

// TestPerformanceCountsIncrementalFinalContent distinguishes Google's final delta from
// repeated Responses and Qoder snapshots. Only newly arriving content extends the
// interval, while all three protocols retain their actual completion observation.
func TestPerformanceCountsIncrementalFinalContent(t *testing.T) {
	p := NewPerformance(FirstOutput, 0)
	p.Observe(`{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`, "", 100)
	p.Observe(`{"candidates":[{"content":{"parts":[{"text":" world"}]},"finishReason":"STOP"}],"usageMetadata":{"candidatesTokenCount":10}}`, "", 600)
	require.Equal(t, int64(2), p.Values["upstream_output_event_count"])
	require.Equal(t, 20.0, *p.TPS(10))
	q := NewPerformance(FirstOutput, 0)
	q.Observe(`{"body":"{\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}"}`, "", 100)
	q.Observe(`{"body":"{\"choices\":[{\"delta\":{\"content\":\" world\"}}]}"}`, "", 600)
	q.Observe(`{"body":"{\"choices\":[{\"message\":{\"content\":\"hello world\"},\"finish_reason\":\"stop\"}],\"usage\":{\"output_tokens\":10}}"}`, "", 900)
	require.True(t, q.Complete)
	require.Equal(t, int64(600), q.Values["upstream_last_output_ms"])
	require.Equal(t, 20.0, *q.TPS(10))
}

// TestPerformanceIncompleteTerminalCannotProduceTPS supplies completed framing with an
// incomplete provider status. Its counters remain visible but cannot qualify throughput.
func TestPerformanceIncompleteTerminalCannotProduceTPS(t *testing.T) {
	p := NewPerformance(FirstOutput, 0)
	p.Observe(`{"type":"response.output_text.delta","delta":"a"}`, "", 100)
	p.Observe(`{"type":"response.output_text.delta","delta":"b"}`, "", 200)
	p.Observe(`{"type":"response.done","response":{"status":"incomplete","usage":{"output_tokens":5}}}`, "", 300)
	require.False(t, p.Complete)
	require.Nil(t, p.TPS(5))
}

// TestPerformanceFirstEventRequiresValidProtocolData rejects malformed and heartbeat
// frames, then accepts a complete protocol error as the first upstream event.
// Error arrival measures a response delay but never establishes throughput eligibility.
func TestPerformanceFirstEventRequiresValidProtocolData(t *testing.T) {
	p := NewPerformance(FirstEvent, 10)
	p.Observe(`{"type":`, "", 20)
	p.Observe(`{"type":"ping"}`, "", 30)
	require.Nil(t, p.TTFTMs())
	p.Observe(`{"type":"error","error":{"message":"upstream failed"}}`, "", 40)
	require.Equal(t, 30, *p.TTFTMs())
	require.False(t, p.Complete)
	require.Nil(t, p.TPS(10))
}

// TestPerformanceChatCompletionWithoutDone accepts the final signals used by Chat
// readers, independently of a DONE sentinel. Final usage enables throughput while
// metadata, missing counters and error terminals retain their unavailable estimates.
func TestPerformanceChatCompletionWithoutDone(t *testing.T) {
	for _, tc := range []struct {
		name          string   // terminal fixture identity
		tail          []string // final protocol observations
		complete      bool     // expected successful terminal evidence
		usageComplete bool     // expected final output counters
	}{
		{"finish_and_usage", []string{`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"completion_tokens":20}}`}, true, true},
		{"finish_then_usage", []string{`{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `{"choices":[],"usage":{"completion_tokens":20}}`}, true, true},
		{"usage_only", []string{`{"choices":[],"usage":{"completion_tokens":20}}`}, true, true},
		{"finish_without_usage", []string{`{"choices":[{"delta":{},"finish_reason":"length"}]}`}, true, false},
		{"partial_usage", []string{`{"choices":[{"delta":{},"finish_reason":null}],"usage":{"completion_tokens":20}}`}, false, false},
		{"error_then_usage", []string{`{"type":"error","error":{"message":"failed"}}`, `{"choices":[],"usage":{"completion_tokens":20}}`}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPerformance(FirstOutput, 0)
			p.Observe(`{"choices":[{"delta":{"content":"hello"}}]}`, "", 100)
			p.Observe(`{"choices":[{"delta":{"content":" world"}}]}`, "", 600)
			for i, event := range tc.tail {
				p.Observe(event, "", int64(700+i*100))
			}
			require.Equal(t, tc.complete, p.Complete)
			require.Equal(t, tc.usageComplete, p.UsageComplete)
			if tc.complete && tc.usageComplete {
				require.Equal(t, 40.0, *p.TPS(20))
			} else {
				require.Nil(t, p.TPS(20))
			}
			require.Equal(t, int64(600), p.Values["upstream_last_output_ms"])
		})
	}
}

// TestPerformanceToolSnapshotsSeparateNamesAndArguments receives the tool name before
// its complete arguments, with optional incremental fragments. Repeated item and
// response snapshots must preserve the last newly observed argument arrival.
func TestPerformanceToolSnapshotsSeparateNamesAndArguments(t *testing.T) {
	for _, tc := range []struct {
		name   string   // output fixture identity
		events []string // content events after the tool name
		count  int64    // expected useful arrival count
	}{
		{"arguments_done", []string{`{"type":"response.function_call_arguments.done","item_id":"call1","arguments":"{}"}`}, 2},
		{"item_done", []string{`{"type":"response.output_item.done","item":{"id":"call1","type":"function_call","name":"lookup","arguments":"{}"}}`}, 2},
		{"completed_only", []string{`{"type":"response.completed","response":{"output":[{"id":"call1","type":"function_call","name":"lookup","arguments":"{}"}],"usage":{"output_tokens":20}}}`}, 2},
		{"incremental", []string{`{"type":"response.function_call_arguments.delta","item_id":"call1","delta":"{"}`, `{"type":"response.function_call_arguments.delta","item_id":"call1","delta":"}"}`}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPerformance(FirstOutput, 0)
			p.Observe(`{"type":"response.output_item.added","item":{"id":"call1","type":"function_call","name":"lookup","arguments":""}}`, "", 100)
			for i, event := range tc.events {
				p.Observe(event, "", int64(600+i*100))
			}
			last := int64(600 + (len(tc.events)-1)*100)
			p.Observe(`{"type":"response.function_call_arguments.done","item_id":"call1","arguments":"{}"}`, "", 800)
			p.Observe(`{"type":"response.output_item.done","item":{"id":"call1","type":"function_call","name":"lookup","arguments":"{}"}}`, "", 900)
			p.Observe(`{"type":"response.completed","response":{"output":[{"id":"call1","type":"function_call","name":"lookup","arguments":"{}"}],"usage":{"output_tokens":20}}}`, "", 1000)
			require.Equal(t, tc.count, p.Values["upstream_output_event_count"])
			require.Equal(t, last, p.Values["upstream_last_output_ms"])
			require.NotNil(t, p.TPS(20))
			require.Equal(t, 100, *p.TTFTMs())
		})
	}
}

// TestPerformanceToolInputsKeepItemAndCloneOwnership interleaves separate tools and
// clones an unfinished observation. Each tool's first arguments count independently,
// and observing arguments on the clone cannot consume the original's evidence.
func TestPerformanceToolInputsKeepItemAndCloneOwnership(t *testing.T) {
	p := NewPerformance(FirstOutput, 0)
	p.Observe(`{"type":"response.output_item.added","item":{"id":"a","type":"function_call","name":"first"}}`, "", 100)
	p.Observe(`{"type":"response.output_item.added","item":{"id":"b","type":"custom_tool_call","name":"second"}}`, "", 200)
	copied := p.Clone()
	copied.Observe(`{"type":"response.function_call_arguments.done","item_id":"a","arguments":"{}"}`, "", 250)
	p.Observe(`{"type":"response.function_call_arguments.done","item_id":"a","arguments":"{}"}`, "", 300)
	p.Observe(`{"type":"response.custom_tool_call_input.done","item_id":"b","input":"actual input"}`, "", 600)
	p.Observe(`{"type":"response.completed","response":{"output":[{"id":"a","type":"function_call","name":"first","arguments":"{}"},{"id":"b","type":"custom_tool_call","name":"second","input":"actual input"}],"usage":{"output_tokens":20}}}`, "", 800)
	require.Equal(t, int64(4), p.Values["upstream_output_event_count"])
	require.Equal(t, int64(600), p.Values["upstream_last_output_ms"])
	require.Equal(t, 40.0, *p.TPS(20))
	require.Equal(t, int64(250), copied.Values["upstream_last_output_ms"])
	encoded, err := json.Marshal(p)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "seenOutputParts")
	require.NotContains(t, string(encoded), "actual input")
}
