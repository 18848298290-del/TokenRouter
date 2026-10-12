package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
)

// Converting an Ent provider copies supported nested JSON credential
// containers before callers edit the result. The isolated map test covers
// provider_data.go with one extension object, protecting the source Ent row
// from mutation through that converted credential path.

// TestProviderEntityProjectionIsolatesNestedCredentials converts one Ent row
// with a nested extension map and edits the converted value. The source map
// retains its text, covering that supported container shape rather than every
// possible credential value.
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
