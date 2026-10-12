package requeststate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHealthHintsPreserveAttemptAndExplicitInputs checks derived hints, explicit false, and parent isolation.
func TestHealthHintsPreserveAttemptAndExplicitInputs(t *testing.T) {
	// Check missing-input behavior with a nil context.
	cases := []struct{ ctx context.Context }{{ctx: nil}}
	for _, item := range cases {
		require.Nil(t, WithHealthModel(item.ctx, nil))
		require.Empty(t, HealthModel(item.ctx, nil))
	}
	parent := WithThinkingEnabled(context.Background(), false)
	child := WithHealthModel(parent, []string{" upstream-a ", "ignored"})
	require.Empty(t, HealthModel(parent, nil))
	require.Equal(t, "upstream-a", HealthModel(child, nil))
	require.Equal(t, "explicit", HealthModel(child, []string{" explicit "}))
	require.Equal(t, "upstream-a", HealthModel(child, []string{" ", "ignored"}))
	require.Equal(t, "upstream-a", HealthModel(WithHealthModel(child, nil), nil))
	require.Nil(t, HealthThinking(context.Background()))
	value := HealthThinking(child)
	require.NotNil(t, value)
	require.False(t, *value)
	*value = true
	require.False(t, *HealthThinking(child))
	restored := WithExecutionHints(context.Background(), ExecutionHintsFromContext(child))
	require.Equal(t, "upstream-a", HealthModel(restored, nil))
}
