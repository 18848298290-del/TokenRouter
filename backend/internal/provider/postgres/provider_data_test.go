package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
)

// TestProviderEntityProjectionIsolatesNestedCredentials checks that editing converted credentials leaves the source Ent row intact.
func TestProviderEntityProjectionIsolatesNestedCredentials(t *testing.T) {
	entity := &dbent.Provider{ID: 1, Credentials: map[string]any{"extension": map[string]any{"value": "original"}}}
	projected := RecordFromEntity(entity)
	copy, ok := projected.Credentials["extension"].(map[string]any)
	require.True(t, ok)
	copy["value"] = "modified"
	original, ok := entity.Credentials["extension"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "original", original["value"])
}
