package forward

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type conversionTestLines struct {
	lines []string
	next  int
	err   error
}

func (l *conversionTestLines) Scan() bool {
	if l.next == len(l.lines) {
		return false
	}
	l.next++
	return true
}

func (l *conversionTestLines) Text() string { return l.lines[l.next-1] }
func (l *conversionTestLines) Err() error   { return l.err }

// TestConversionSSEScannerReadError checks delivery of complete JSON alongside a connection error.
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

// TestConversionSSEScannerFrameLimit checks accumulated short lines, per-frame budget reset, and stopping at the limit.
func TestConversionSSEScannerFrameLimit(t *testing.T) {
	for _, line := range []string{"data: x", ": comment", "id: 123"} {
		t.Run(line, func(t *testing.T) {
			lines := &conversionTestLines{lines: []string{line, line, line, line, line}}
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
