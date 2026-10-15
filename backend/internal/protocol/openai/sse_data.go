package openai

import (
	"strings"

	"github.com/tidwall/gjson"
)

// sse_data.go accumulates SSE data lines and emits joined JSON values or
// separate compatibility payloads.
// A standard SSE event can split one JSON value across several data lines,
// while compatible streams can place independent values on successive lines.
// Accumulation waits for a blank line or an explicit flush before deciding
// which representation the collected data uses.
//
// Joined data is emitted together when it forms valid JSON. Otherwise each
// line is emitted separately, with empty text and DONE sentinels removed by
// the payload emitter. Individual emitted lines are not required to be valid
// JSON; downstream consumers choose that validation. Callbacks run synchronously
// before the accumulator clears its pending slice, so callers serialize access
// and avoid reentering the same accumulator while its flush is in progress.

// SSEDataAccumulator retains data-line fragments until one frame can be emitted.
type SSEDataAccumulator struct {
	// lines holds accepted data fields until a blank line or Flush processes
	// them. A nil callback leaves this pending state unchanged.
	lines []string
}

// AddLine accepts a physical SSE line, removing trailing CR/LF before parsing
// its data prefix. Data fields are buffered, whitespace-only lines flush, and
// other fields are ignored. A nil callback skips both buffering and flushing.
func (a *SSEDataAccumulator) AddLine(line string, fn func([]byte)) {
	if fn == nil {
		return
	}
	trimmedLine := strings.TrimRight(line, "\r\n")
	if data, ok := ExtractSSEDataLine(trimmedLine); ok {
		a.lines = append(a.lines, data)
		return
	}
	if strings.TrimSpace(trimmedLine) == "" {
		a.Flush(fn)
	}
}

// Flush synchronously emits pending data and then clears its retained line
// count for the next frame. Nil callbacks and empty state leave the accumulator
// unchanged; callers avoid callback reentry and serialize use of this mutable state.
func (a *SSEDataAccumulator) Flush(fn func([]byte)) {
	if fn == nil || len(a.lines) == 0 {
		return
	}
	// Callbacks run while this frame is pending. Reentering Flush from a
	// callback would emit the same frame again before the outer flush clears it.
	EmitSSEDataPayloads(a.lines, fn)
	a.lines = a.lines[:0]
}

// ForEachSSEDataPayload emits data payloads from physical SSE lines and flushes
// a final unterminated frame at EOF. Nil callbacks or blank bodies do nothing;
// emitted payloads follow the accumulator's joined-JSON or separate-line rule.
func ForEachSSEDataPayload(body string, fn func([]byte)) {
	if fn == nil || strings.TrimSpace(body) == "" {
		return
	}
	var acc SSEDataAccumulator
	for line := range strings.SplitSeq(body, "\n") {
		acc.AddLine(line, fn)
	}
	acc.Flush(fn)
}

// EmitSSEDataPayloads emits a single line directly or joins multiple lines
// when their combined text is valid JSON. Nil callbacks and empty input return
// without output; blank text and DONE are filtered by EmitSSEDataPayload.
//
// When joined text is invalid, each line is passed separately because compatible
// streams can place independent event documents on successive data lines.
// Separate lines are not validated here, allowing the owning parser to choose
// how to handle malformed events. Callback invocation follows the supplied line
// order and completes before this helper returns.
func EmitSSEDataPayloads(lines []string, fn func([]byte)) {
	if fn == nil || len(lines) == 0 {
		return
	}
	if len(lines) == 1 {
		EmitSSEDataPayload(lines[0], fn)
		return
	}
	joined := strings.Join(lines, "\n")
	if gjson.Valid(joined) {
		EmitSSEDataPayload(joined, fn)
		return
	}
	for _, line := range lines {
		// Invalid joined JSON can still contain separate event documents.
		// Emit each candidate in order so downstream validation can handle them independently.
		EmitSSEDataPayload(line, fn)
	}
}

// EmitSSEDataPayload trims a data value and synchronously invokes fn for
// nonblank values other than DONE. Callers supply a nonnil callback for emitted
// data; this helper converts text to bytes without establishing complete JSON validity.
func EmitSSEDataPayload(data string, fn func([]byte)) {
	data = strings.TrimSpace(data)
	// A frame can collect the DONE sentinel beside ordinary data lines.
	// Omit its standalone marker so downstream event parsers receive candidate payloads.
	if data == "" || data == "[DONE]" {
		return
	}
	fn([]byte(data))
}

// ExtractSSEDataLine returns content after a leading data: prefix, skipping
// adjacent spaces and tabs. Other prefixes return false; remaining payload
// whitespace is retained for the emitter, and callers remove physical line
// endings separately.
func ExtractSSEDataLine(line string) (string, bool) {
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	start := len("data:")
	for start < len(line) {
		if line[start] != ' ' && line[start] != '	' {
			break
		}
		start++
	}
	return line[start:], true
}

// ExtractSSEEventLine returns trimmed content after a leading event: prefix,
// skipping adjacent spaces and tabs. Other prefixes return false; callers use
// the returned label separately from the data payload's JSON type.
func ExtractSSEEventLine(line string) (string, bool) {
	if !strings.HasPrefix(line, "event:") {
		return "", false
	}
	start := len("event:")
	for start < len(line) {
		if line[start] != ' ' && line[start] != '	' {
			break
		}
		start++
	}
	return strings.TrimSpace(line[start:]), true
}
