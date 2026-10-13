package routing_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog/testkit"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Gemini resource IDs can carry a models/ prefix while an administrator's
// override uses the short model identity. The fixture resolves an alias through
// saved provider mapping and compares preview with runtime attributes for that
// same final route.

// previewAttributeRepo supplies the saved profile used by preview and runtime
// resolution.
type previewAttributeRepo struct {
	// unused repository operations supplied by embedding
	routing.ModelAttributeRepository
	// config supplies both preview and runtime resolution. The fixture keeps it
	// unchanged while those consumers resolve the same provider route.
	config *routing.ModelAttributeConfig // shared saved attribute profile
}

// ForGroup returns the fixture's configured profile for any requested group.
// The returned pointer is shared, allowing the test to compare preview and
// runtime against the same saved rules.
func (r previewAttributeRepo) ForGroup(context.Context, int64) (*routing.ModelAttributeConfig, error) {
	return r.config, nil
}

// TestModelAttributePreviewMatchesRuntimeForResourceID maps a Gemini alias to
// its resource-form upstream ID. Preview and runtime must both apply the
// short-ID temperature override to the resolved route.
func TestModelAttributePreviewMatchesRuntimeForResourceID(t *testing.T) {
	ctx := context.Background()
	record := provider.Record{ID: 1, Platform: "gemini", Type: "apikey", Credentials: map[string]any{"model_mapping": map[string]any{"alias": "models/gemini-2.5-flash"}}}
	providers := gatewayprovider.CatalogueProviders([]provider.Record{record})
	temperature := false
	config := &routing.ModelAttributeConfig{Status: "active", Rules: []routing.ModelAttributeRule{{Models: []string{"gemini-2.5-flash"}, Attributes: modelcatalog.Attributes{Temperature: &temperature}}}}
	svc := &routing.ModelAttributeService{Repo: previewAttributeRepo{config: config}, Catalog: routing.ModelAttributeCatalog{View: testkit.OfflineView, Candidates: func() func(string) []string { return pricing.BuildModelIdentityCandidates }}, Catalogue: &routing.RequestableCatalogue{Read: func(context.Context, *int64) ([]routing.CatalogueProvider, error) { return providers, nil }}}
	preview, err := svc.Preview(ctx, 1, "alias", "", config)
	require.NoError(t, err)
	require.Len(t, preview, 1)
	target := preview[0].Target
	runtime, err := svc.NewSession().Resolve(ctx, 1, target)
	require.NoError(t, err)
	require.Equal(t, &temperature, runtime.Attributes.Temperature)
	require.Equal(t, runtime.Attributes.Temperature, preview[0].Attributes.Temperature)
}
