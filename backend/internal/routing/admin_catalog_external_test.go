package routing_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	routingprovider "github.com/TokenFlux/TokenRouter/internal/routing/provider"
)

// catalogueMetadataFixture records metadata lookups and fails if a caller enumerates the catalog.
type catalogueMetadataFixture struct {
	entries map[string]modelcatalog.Entry
	lookups int
}

func (c *catalogueMetadataFixture) ModelIDs() []string {
	panic("provider catalogue enumerated global metadata")
}

func (c *catalogueMetadataFixture) ModelEntry(id string) modelcatalog.Entry {
	c.lookups++
	return c.entries[id]
}
func (c *catalogueMetadataFixture) ModelVersion() string { return "fixture" }

func configuredAdminCatalogue(metadataCount int) (*routing.AdminCatalog, *provider.Record, *catalogueMetadataFixture) {
	metadata := &catalogueMetadataFixture{entries: make(map[string]modelcatalog.Entry, metadataCount)}
	for i := range metadataCount {
		id := fmt.Sprintf("probe-model-%03d", i)
		metadata.entries[id] = modelcatalog.Entry{Model: id}
	}
	whitelist := make([]any, 186)
	mapping := map[string]any{}
	for i := range whitelist {
		id := fmt.Sprintf("probe-model-%03d", i)
		whitelist[i] = id
		mapping[id] = id
	}
	record := &provider.Record{Platform: "openai", Type: "apikey", Credentials: map[string]any{"model_whitelist": whitelist, "model_mapping": mapping}}
	return routing.NewAdminCatalog(routingprovider.AdminCatalogOptions(metadata)), record, metadata
}

// TestAdminCatalogueIndependentOfMetadataSize checks constant eligibility and lookup counts when metadata
// grows tenfold.
func TestAdminCatalogueIndependentOfMetadataSize(t *testing.T) {
	for _, size := range []int{11270, 112700} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			catalogue, record, metadata := configuredAdminCatalogue(size)
			rules := (gatewayprovider.ModelPolicy{Record: record}).CatalogueRules()
			checks := 0
			result, err := catalogue.Available(routing.AdminCatalogInput{Platform: "openai", Accept: func(id string) bool {
				checks++
				return rules.Supports(context.Background(), id)
			}}, rules.ConfiguredModels)
			require.NoError(t, err)
			require.Len(t, result.Models, 186)
			require.Equal(t, 186, checks)
			require.Equal(t, 186, metadata.lookups)
		})
	}
}

// BenchmarkAdminConfiguredCatalogue measures a provider test catalog, including model-rule preparation.
func BenchmarkAdminConfiguredCatalogue(b *testing.B) {
	catalogue, record, _ := configuredAdminCatalogue(11270)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rules := (gatewayprovider.ModelPolicy{Record: record}).CatalogueRules()
		result, err := catalogue.Available(routing.AdminCatalogInput{Platform: "openai", Accept: func(id string) bool { return rules.Supports(context.Background(), id) }}, rules.ConfiguredModels)
		if err != nil {
			b.Fatal(err)
		}
		if len(result.Models) != 186 {
			b.Fatalf("models=%d", len(result.Models))
		}
	}
}
