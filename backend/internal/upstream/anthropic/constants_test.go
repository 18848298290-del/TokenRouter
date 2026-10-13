package anthropic

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFullClaudeCodeMimicryBetas_DoesNotDefaultRedactThinking reads the
// built-in mimic token slice. It contains Claude Code, OAuth, and interleaved
// thinking while leaving redact-thinking out of the default set.
func TestFullClaudeCodeMimicryBetas_DoesNotDefaultRedactThinking(t *testing.T) {
	required := FullClaudeCodeMimicryBetas()

	require.NotContains(t, required, BetaRedactThinking)
	require.Contains(t, required, BetaClaudeCode)
	require.Contains(t, required, BetaOAuth)
	require.Contains(t, required, BetaInterleavedThinking)
}
