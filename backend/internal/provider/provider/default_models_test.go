package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDefaultSparkShadowModelMapping checks the native Spark identity mapping
// and mutates its result. A later defaults call must return the original model
// target from an independent map.
func TestDefaultSparkShadowModelMapping(t *testing.T) {
	mapping := DefaultSparkShadowModels()

	require.Len(t, mapping, 1, "Spark 只公开完整原生型号")
	require.Equal(t, "gpt-5.3-codex-spark", mapping["gpt-5.3-codex-spark"])
	mapping["gpt-5.3-codex-spark"] = "changed"
	require.Equal(t, "gpt-5.3-codex-spark", DefaultSparkShadowModels()["gpt-5.3-codex-spark"])
}

// TestSparkModelVariants reads the finite candidates used by Spark shadow
// defaults. The set must contain the full native Spark model ID advertised by
// the shadow mapping.
func TestSparkModelVariants(t *testing.T) {
	got := sparkModelVariants()
	require.ElementsMatch(t, []string{
		"gpt-5.3-codex-spark",
	}, got)
}
