package routing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Administrative catalog queries retain configured IDs even when metadata is
// absent, and platform labels choose response shapes independently of the
// candidate source. These cases check display metadata, custom IDs, and
// returned scalar-value isolation across the supplied platform inputs.

// TestAdminCatalogUsesConfiguredIDs queries the same two configured candidates
// across platform labels with OAuth and passthrough input. It checks returned
// display metadata and scalar-value isolation for those supplied IDs.
func TestAdminCatalogUsesConfiguredIDs(t *testing.T) {
	for _, platform := range []string{"anthropic", "openai", "gemini", "antigravity", "qoder", "grok", "kimi", "zhipu", "deepseek"} {
		t.Run(platform, func(t *testing.T) {
			defaults := []AdminCatalogModel{{ID: "catalog-model", DisplayName: "Catalog Model"}, {ID: "blocked", DisplayName: "Blocked"}}
			catalog := NewAdminCatalog(AdminCatalogOptions{Lookup: func(_ AdminCatalogKind, id string) AdminCatalogModel {
				for _, model := range defaults {
					if model.ID == id {
						return model
					}
				}
				return AdminCatalogModel{ID: id, DisplayName: id}
			}})
			result, err := catalog.Available(AdminCatalogInput{Platform: platform, OAuth: true, Passthrough: true, Accept: func(id string) bool { return id != "blocked" }}, func() []string { return []string{"custom", "catalog-model"} })
			require.NoError(t, err)
			require.Len(t, result.Models, 2)
			require.Equal(t, "Catalog Model", result.Models[0].DisplayName)
			require.Equal(t, "custom", result.Models[1].DisplayName)
			result.Models[0].DisplayName = "changed"
			require.Equal(t, "Catalog Model", defaults[0].DisplayName)
		})
	}
}

// TestAdminCatalogUnknownConfiguredID supplies one configured ID with no
// metadata lookup callback. It checks that both the returned ID and display
// name retain that supplied unknown model.
func TestAdminCatalogUnknownConfiguredID(t *testing.T) {
	catalog := NewAdminCatalog(AdminCatalogOptions{})
	result, err := catalog.Available(AdminCatalogInput{Platform: "openai"}, func() []string { return []string{"vendor/new-model"} })
	require.NoError(t, err)
	require.Equal(t, "vendor/new-model", result.Models[0].ID)
	require.Equal(t, "vendor/new-model", result.Models[0].DisplayName)
}
