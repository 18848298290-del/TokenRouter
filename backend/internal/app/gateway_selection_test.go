package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
)

func TestSnapshotOpenAICompatibilityFallbackMetrics(t *testing.T) {
	before := gatewayCompatibilitySnapshot(nil)
	ctx := requeststate.WithThinkingEnabled(context.Background(), true)
	_, _ = requeststate.ThinkingEnabledFromContext(ctx)

	after := gatewayCompatibilitySnapshot(nil)
	// Request-local thinking state does not add entries to the compatibility metadata count.
	require.Zero(t, after.MetadataTotal)
	require.Zero(t, before.MetadataTotal)
}

// TestProductEnvCompatibility checks legacy variable names and precedence when the new variable disables a flag.
func TestProductEnvCompatibility(t *testing.T) {
	t.Setenv("SUB2API_DEBUG_MODEL_ROUTING", "true")
	t.Setenv("TOKENROUTER_DEBUG_MODEL_ROUTING", "")
	if !selectionOptions(nil).DebugRouting {
		t.Fatal("legacy routing flag was ignored")
	}
	for _, value := range []string{"0", "false"} {
		t.Setenv("TOKENROUTER_DEBUG_MODEL_ROUTING", value)
		if selectionOptions(nil).DebugRouting {
			t.Fatalf("new value %q must disable routing debug", value)
		}
	}
}
