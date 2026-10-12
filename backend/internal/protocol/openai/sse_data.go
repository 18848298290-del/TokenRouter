package openai

import (
	"strings"

	"github.com/tidwall/gjson"
)

type SSEDataAccumulator struct {
	lines []string
}

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

func (a *SSEDataAccumulator) Flush(fn func([]byte)) {
	if fn == nil || len(a.lines) == 0 {
		return
	}
	EmitSSEDataPayloads(a.lines, fn)
	a.lines = a.lines[:0]
}

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
		EmitSSEDataPayload(line, fn)
	}
}

func EmitSSEDataPayload(data string, fn func([]byte)) {
	data = strings.TrimSpace(data)
	if data == "" || data == "[DONE]" {
		return
	}
	fn([]byte(data))
}

// ExtractSSEDataLine returns the content of a data: line.
// Spaces and tabs immediately after the colon are skipped.
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

// ExtractSSEEventLine extracts and trims the content of an SSE event: line.
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
