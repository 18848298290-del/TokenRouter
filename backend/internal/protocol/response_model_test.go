package protocol

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResponseModelObserver checks model declaration precedence and absent-model results.
func TestResponseModelObserver(t *testing.T) {
	t.Run("responses terminal wins", func(t *testing.T) {
		var o ResponseModelObserver
		o.ObserveOpenAI([]byte(`{"response":{"model":" early "}}`), "response.created")
		o.ObserveOpenAI([]byte(`{"response":{"model":"runtime-version"}}`), "response.completed")
		o.ObserveOpenAI([]byte(`{"response":{"model":"late-delta"}}`), "response.output_text.delta")
		require.Equal(t, "runtime-version", o.Model())
	})
	t.Run("chat keeps first", func(t *testing.T) {
		var o ResponseModelObserver
		o.ObserveOpenAI([]byte(`{"model":"first"}`), "")
		o.ObserveOpenAI([]byte(`{"model":"last"}`), "")
		require.Equal(t, "first", o.Model())
	})
	t.Run("messages metadata", func(t *testing.T) {
		var o ResponseModelObserver
		o.ObserveAnthropic([]byte(`{"type":"message_start","message":{"model":"claude-runtime"}}`))
		o.ObserveAnthropic([]byte(`{"delta":{"text":"model: different"}}`))
		require.Equal(t, "claude-runtime", o.Model())
	})
	t.Run("gemini wrappers and last declaration", func(t *testing.T) {
		var o ResponseModelObserver
		o.ObserveGemini([]byte(`{"modelVersion":"first"}`))
		o.ObserveGemini([]byte(`{"response":{"response":{"modelVersion":"last"}}}`))
		require.Equal(t, "last", o.Model())
	})
	for _, body := range []string{`{`, `{"model":42}`, `{"model":null}`, `{"model":"  "}`, `{"model":"invalid",`, `{"output":[{"model":"tool-model"}]}`, `{"object":"response.compaction","output":[]}`} {
		t.Run(body, func(t *testing.T) {
			var o ResponseModelObserver
			o.ObserveOpenAI([]byte(body), "response.completed")
			require.Empty(t, o.Model())
		})
	}
	t.Run("unicode limit and independent attempt", func(t *testing.T) {
		var first, second ResponseModelObserver
		first.Observe(strings.Repeat("型", 201), false)
		require.Equal(t, 200, len([]rune(first.Model())))
		require.Empty(t, second.Model())
		var absent *ResponseModelObserver
		require.Empty(t, absent.Model())
	})
}
