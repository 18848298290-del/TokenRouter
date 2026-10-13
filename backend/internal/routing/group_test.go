package routing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Nested JSON-like group feature values can contain objects inside arrays and
// arrays inside arrays. The copy case mutates a boolean and a string through
// the returned nested containers, then checks the corresponding source values
// to detect shared storage.

// TestPricingConfigNestedSnapshotIsolation mutates a nested object and array
// returned by GroupRoutingPolicy.Clone. It checks that the source boolean and
// string remain unchanged in this supported JSON-shaped copying case.
func TestPricingConfigNestedSnapshotIsolation(t *testing.T) {
	source := &GroupRoutingPolicy{FeaturesConfig: map[string]any{"extension": []any{map[string]any{"enabled": true}, []any{"original"}}}}
	copied := source.Clone()
	values, ok := copied.FeaturesConfig["extension"].([]any)
	require.True(t, ok)
	object, ok := values[0].(map[string]any)
	require.True(t, ok)
	array, ok := values[1].([]any)
	require.True(t, ok)
	object["enabled"] = false
	array[0] = "changed"
	originalValues, ok := source.FeaturesConfig["extension"].([]any)
	require.True(t, ok)
	originalObject, ok := originalValues[0].(map[string]any)
	require.True(t, ok)
	originalArray, ok := originalValues[1].([]any)
	require.True(t, ok)
	require.Equal(t, true, originalObject["enabled"])
	require.Equal(t, "original", originalArray[0])
}
