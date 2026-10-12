package openai

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// These sinks retain event labels and terminal decisions produced by both
// benchmark parsing paths, exposing their results after each timed iteration.
var (
	benchmarkOpenAIResponseSSEEventTypeSink string
	benchmarkOpenAIResponseSSETerminalSink  bool
)

// TestOpenAIStreamEventIsTerminalWithTypeMatchesExistingSemantics supplies
// empty, sentinel, terminal, nonterminal, and trailing-garbage payloads. Classification
// with a preextracted type must match the data-reading helper for every asserted
// input, including its partial-JSON extraction behavior.
func TestOpenAIStreamEventIsTerminalWithTypeMatchesExistingSemantics(t *testing.T) {
	tests := []struct {
		name string // terminal classification scenario
		data string // supplied SSE payload
		want bool   // expected terminal classification
	}{
		{name: "empty", data: "", want: false},
		{name: "whitespace", data: " \t ", want: false},
		{name: "done", data: " [DONE] ", want: true},
		{name: "JSON outer whitespace", data: " \n\t {\"type\":\"response.completed\"} \r\n", want: true},
		{name: "completed", data: `{"type":"response.completed"}`, want: true},
		{name: "response done", data: `{"type":"response.done"}`, want: true},
		{name: "failed", data: `{"type":"response.failed"}`, want: true},
		{name: "incomplete", data: `{"type":"response.incomplete"}`, want: true},
		{name: "cancelled", data: `{"type":"response.cancelled"}`, want: true},
		{name: "canceled", data: `{"type":"response.canceled"}`, want: true},
		{name: "delta", data: `{"type":"response.output_text.delta"}`, want: false},
		{name: "invalid JSON", data: `{"type":`, want: false},
		{name: "terminal with trailing garbage", data: `{"type":"response.completed"} trailing`, want: true},
		{name: "nonterminal with trailing garbage", data: `{"type":"response.output_text.delta"} trailing`, want: false},
		{name: "type whitespace is normalized", data: `{"type":" response.completed "}`, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventType := gjson.GetBytes([]byte(tt.data), "type").String()
			got := OpenAIStreamEventIsTerminalWithType(tt.data, eventType)

			require.Equal(t, tt.want, got)
			require.Equal(t, OpenAIStreamEventIsTerminal(tt.data), got)
		})
	}
}

// BenchmarkOpenAIResponseSSETypeExtraction compares repeated type extraction
// with reuse of an extracted event label. Both benchmark paths retain their label
// and terminal decision in package-level sinks while reporting allocations and bytes.
func BenchmarkOpenAIResponseSSETypeExtraction(b *testing.B) {
	data := `{"type":"response.output_text.delta","sequence_number":42,"delta":"streaming response benchmark payload"}`
	dataBytes := []byte(data)

	b.Run("legacy double parse", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(dataBytes)))
		b.ResetTimer()
		for range b.N {
			benchmarkOpenAIResponseSSETerminalSink = OpenAIStreamEventIsTerminal(data)
			benchmarkOpenAIResponseSSEEventTypeSink = strings.TrimSpace(gjson.GetBytes(dataBytes, "type").String())
		}
	})

	b.Run("reused single parse", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(dataBytes)))
		b.ResetTimer()
		for range b.N {
			eventTypeRaw := gjson.GetBytes(dataBytes, "type").String()
			benchmarkOpenAIResponseSSEEventTypeSink = strings.TrimSpace(eventTypeRaw)
			benchmarkOpenAIResponseSSETerminalSink = OpenAIStreamEventIsTerminalWithType(data, eventTypeRaw)
		}
	})
}
