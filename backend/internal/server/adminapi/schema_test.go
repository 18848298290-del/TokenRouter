package adminapi

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// schema_test.go checks the public argument rules derived from HTTP input
// types. Tests preserve partial updates, explicit null, dynamic maps, and
// required-field validation while also preventing undocumented administrative
// routes or invalid aliases from becoming available through the catalogue.

// schemaFixtureInput models the omission and binding rules used by management handlers.
type schemaFixtureInput struct {
	Name     string         `json:"name" binding:"required"` // required display label
	Enabled  *bool          `json:"enabled"`                 // optional admission switch
	Settings map[string]any `json:"settings"`                // dynamic provider settings
	IDs      []int64        `json:"ids"`                     // optional linked resource identifiers
}

// TestArgumentsRetainHTTPBindingRules checks omission, null, scalar types, and extra
// fields against a registered input shape. Required Go JSON output fields do not
// accidentally become required partial-update inputs.
func TestArgumentsRetainHTTPBindingRules(t *testing.T) {
	op := &Operation{ID: "fixture.update", Input: Arguments("/api/v1/admin/fixture/:id", []string{"search"}, (*schemaFixtureInput)(nil))}
	for _, body := range []map[string]any{
		{"name": "one"},
		{"name": "one", "enabled": nil},
		{"name": "one", "settings": nil, "ids": nil},
		{"name": "one", "settings": map[string]any{}, "ids": []any{}},
		{"name": "one", "enabled": false, "settings": map[string]any{"custom": []any{float64(1), "value"}}},
	} {
		require.NoError(t, op.Validate(map[string]any{"path": map[string]any{"id": "1"}, "body": body}))
	}
	for _, arguments := range []map[string]any{
		{"path": map[string]any{"id": "1"}, "body": map[string]any{"enabled": true}},
		{"path": map[string]any{"id": "1"}, "body": map[string]any{"name": float64(1)}},
		{"path": map[string]any{"id": "1"}, "body": map[string]any{"name": "one", "unexpected": true}},
		{"path": map[string]any{"id": "1"}, "query": map[string]any{"unknown": "value"}},
	} {
		require.Error(t, op.Validate(arguments))
	}
}

// TestCatalogRejectsMissingRoutesAndInvalidAliases introduces an undocumented route,
// duplicate identity, and alias to a mutation. Each fails catalogue validation
// before any MCP tool can expose the inconsistent operation.
func TestCatalogRejectsMissingRoutesAndInvalidAliases(t *testing.T) {
	_, err := FromRoutes(gin.RoutesInfo{{Method: "POST", Path: "/api/v1/admin/unregistered-feature"}})
	require.ErrorContains(t, err, "missing management operation")
	_, err = NewCatalog([]*Operation{{ID: "same"}, {ID: "same"}})
	require.ErrorContains(t, err, "duplicate")
	_, err = NewCatalog([]*Operation{{ID: "read", ReadOnly: true, Alias: "write"}, {ID: "write"}})
	require.ErrorContains(t, err, "invalid management alias")
	for _, operation := range []*Operation{
		{ID: "stream", Response: ResponseSSE},
		{ID: "browser", Response: ResponseBrowser, BrowserPath: "https://external.invalid"},
		{ID: "unknown", Response: "custom"},
	} {
		_, err = NewCatalog([]*Operation{operation})
		require.Error(t, err)
	}
}
