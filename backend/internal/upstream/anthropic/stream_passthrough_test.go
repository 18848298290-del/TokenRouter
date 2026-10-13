package anthropic

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Passthrough usage observation reads data lines independently of forwarded
// event lines. The extraction cases check removal of spaces after the data
// prefix and rejection of an event-name line, protecting the payload selection
// used by that reader.

// TestExtractAnthropicSSEDataLine supplies a spaced data payload and an event
// header. It checks the JSON payload is returned from the data line and the
// event line reports no payload.
func TestExtractAnthropicSSEDataLine(t *testing.T) {
	t.Run("valid data line with spaces", func(t *testing.T) {
		data, ok := ExtractSSEDataLine("data:   {\"type\":\"message_start\"}")
		require.True(t, ok)
		require.Equal(t, `{"type":"message_start"}`, data)
	})

	t.Run("non data line", func(t *testing.T) {
		data, ok := ExtractSSEDataLine("event: message_start")
		require.False(t, ok)
		require.Empty(t, data)
	})
}
