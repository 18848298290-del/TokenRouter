package forward

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Frame conversion can retain a complete unterminated JSON frame while exposing
// the read error that followed it. These fixtures also separate per-frame
// accumulated-byte accounting from line count and verify that a size failure
// stops the cursor before subsequent lines are consumed.

// conversionTestLines supplies deterministic line progress and a terminal scanner error.
type conversionTestLines struct {
	lines []string // fixture response lines
	next  int      // consumed fixture lines
	err   error    // terminal fixture error
}

// Scan advances one fixture line and reports whether that line exists.
// Exhaustion leaves the cursor unchanged, allowing tests to detect an unexpected
// extra read after a conversion size failure.
func (l *conversionTestLines) Scan() bool {
	if l.next == len(l.lines) {
		return false
	}
	l.next++
	return true
}

// Text returns the last line selected by a successful fixture Scan.
// Call it after Scan returns true; the zero cursor has no selected line and
// would produce an invalid index.
func (l *conversionTestLines) Text() string { return l.lines[l.next-1] }

// Err returns the fixture's terminal failure for scanner outcome assertions.
func (l *conversionTestLines) Err() error { return l.err }

// TestConversionSSEScannerReadError supplies complete and truncated JSON without
// a final blank delimiter. The complete fixture is delivered, both retain the
// injected read error, and neither permits another scan afterward.
func TestConversionSSEScannerReadError(t *testing.T) {
	readErr := errors.New("读取中断")
	for _, payload := range []string{`{"type":"message_delta","usage":{"output_tokens":25}}`, `{"type":"message_delta"`} {
		lines := &conversionTestLines{lines: []string{"data: " + payload}, err: readErr}
		scanner := newConversionSSEScanner(Response{Lines: lines})
		complete := strings.HasSuffix(payload, "}}")
		require.Equal(t, complete, scanner.Scan())
		if complete {
			require.Equal(t, payload, scanner.frame.Data)
		}
		require.ErrorIs(t, scanner.Err(), readErr)
		require.False(t, scanner.Scan())
	}
}

// TestConversionSSEScannerFrameLimit fills a budget with short data, comment,
// and ID lines. Overflow stops on the third line and clears output, while
// blank delimiters let two exact-budget data frames complete independently.
func TestConversionSSEScannerFrameLimit(t *testing.T) {
	for _, line := range []string{"data: x", ": comment", "id: 123"} {
		t.Run(line, func(t *testing.T) {
			lines := &conversionTestLines{lines: []string{line, line, line, line, line}}
			// Two lines fit exactly; the third exceeds this frame budget.
			scanner := newConversionSSEScanner(Response{Lines: lines, MaxSSEFrameBytes: 2 * (len(line) + 1)})
			require.False(t, scanner.Scan())
			require.ErrorIs(t, scanner.Err(), ErrConversionSSEFrameTooLarge)
			require.Equal(t, 3, lines.next)
			require.False(t, scanner.Scan())
			require.Equal(t, 3, lines.next)
			require.Empty(t, scanner.frame.Data)
		})
	}
	lines := &conversionTestLines{lines: []string{"data: {}", "", "data: {}", ""}}
	scanner := newConversionSSEScanner(Response{Lines: lines, MaxSSEFrameBytes: len("data: {}\n")})
	require.True(t, scanner.Scan())
	require.True(t, scanner.Scan())
	require.False(t, scanner.Scan())
	require.NoError(t, scanner.Err())
}
