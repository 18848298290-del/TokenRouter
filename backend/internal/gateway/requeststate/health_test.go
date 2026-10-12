package requeststate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHealthHintsPreserveAttemptAndExplicitInputs combines explicit model
// evidence, derived contexts, and an explicitly false thinking hint. Reads must
// honor the first model, retain parent state, and return independently mutable
// thinking pointers.
func TestHealthHintsPreserveAttemptAndExplicitInputs(t *testing.T) {
	// Check missing-input behavior with a nil context.
	cases := []struct {
		ctx context.Context /* context supplied to health helpers */
	}{{ctx: nil}}
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
	// Change the returned pointer to expose accidental sharing with the saved
	// explicit-false hint; later reads must still observe the context value.
	*value = true
	require.False(t, *HealthThinking(child))
	restored := WithExecutionHints(context.Background(), ExecutionHintsFromContext(child))
	require.Equal(t, "upstream-a", HealthModel(restored, nil))
}
