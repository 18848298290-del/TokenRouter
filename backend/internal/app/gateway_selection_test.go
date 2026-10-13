package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
)

// Routing debug settings support TOKENROUTER and compatibility environment
// names, with any nonempty current value taking precedence. The accompanying
// compatibility snapshot scenario checks that request-local thinking state does
// not populate the snapshot's metadata count when shared counters are absent.

// TestSnapshotOpenAICompatibilityFallbackMetrics reads empty compatibility
// snapshots around request-local thinking access. Both metadata totals must
// remain zero, protecting the absence of metadata accounting in the snapshot
// helper.
func TestSnapshotOpenAICompatibilityFallbackMetrics(t *testing.T) {
	before := gatewayCompatibilitySnapshot(nil)
	ctx := requeststate.WithThinkingEnabled(context.Background(), true)
	_, _ = requeststate.ThinkingEnabledFromContext(ctx)

	after := gatewayCompatibilitySnapshot(nil)
	// Request-local thinking state does not add entries to the compatibility metadata count.
	require.Zero(t, after.MetadataTotal)
	require.Zero(t, before.MetadataTotal)
}

// TestProductEnvCompatibility enables routing debug through the legacy name
// with an empty current variable. Nonempty current values "0" and "false" must
// then take precedence and disable the debug flag.
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
