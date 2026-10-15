package testkit

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// compact_sse.go parses compact SSE test output into event names and data
// strings for assertions.
// Compact output fixtures use one event line and one JSON data line per block.
// Parsing keeps that deliberately narrow shape visible to assertions, rejecting
// extra lines or missing prefixes so malformed synthetic output cannot silently
// enter later protocol comparisons.

// ParseCompactSSE extracts event and data strings from synthetic two-line SSE
// blocks. Each block must contain exactly one event line followed by one data
// line, and malformed fixtures fail the test; this helper does not assemble
// general multiline SSE data.
func ParseCompactSSE(t *testing.T, body string) [][2]string {
	t.Helper()
	var events [][2]string
	for block := range strings.SplitSeq(strings.TrimSpace(body), "\n\n") {
		lines := strings.Split(block, "\n")
		// Require the fixture's exact event/data pair so extra SSE lines cannot be
		// silently omitted from protocol-output assertions.
		require.Len(t, lines, 2, "每个 SSE 事件应为 event+data 两行: %q", block)
		require.True(t, strings.HasPrefix(lines[0], "event: "), "缺少 event 行: %q", block)
		require.True(t, strings.HasPrefix(lines[1], "data: "), "缺少 data 行: %q", block)
		events = append(events, [2]string{
			strings.TrimPrefix(lines[0], "event: "),
			strings.TrimPrefix(lines[1], "data: "),
		})
	}
	return events
}
