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

// Configured administrative candidates should require the same eligibility and
// metadata lookups when unrelated catalog metadata grows. A fixture forbids
// enumeration and counts per-ID reads, while the benchmark times model-rule
// preparation and assembly after its large metadata map is built.

// catalogueMetadataFixture stores mock entries and detects administrative
// metadata enumeration.
type catalogueMetadataFixture struct {
	entries map[string]modelcatalog.Entry // mock metadata by model ID
	lookups int                           // recorded individual metadata lookup count
}

// ModelIDs rejects global catalog enumeration for this configured-candidate
// fixture. It panics immediately when a caller requests the whole metadata
// list, making an accidental catalog-size dependency observable during the
// test.
func (c *catalogueMetadataFixture) ModelIDs() []string {
	panic("provider catalogue enumerated global metadata")
}

// ModelEntry counts a lookup and returns the stored mock entry. Missing IDs
// return the map's zero value, allowing metadata adapters to handle unknown
// configured models through their normal path.
func (c *catalogueMetadataFixture) ModelEntry(id string) modelcatalog.Entry {
	c.lookups++
	return c.entries[id]
}

// ModelVersion returns the fixed mock catalog version for these lookup
// fixtures.
func (c *catalogueMetadataFixture) ModelVersion() string { return "fixture" }

// configuredAdminCatalogue constructs metadataCount mock entries and 186
// configured provider model IDs. It returns a catalog, API-key provider, and
// lookup counter whose candidate count stays independent of the metadata map
// size.
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

// TestAdminCatalogueIndependentOfMetadataSize compares two metadata sizes with
// the same 186 configured candidates. It asserts unchanged eligibility checks
// and per-ID lookups while the fixture rejects enumeration.
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

// BenchmarkAdminConfiguredCatalogue measures rule preparation and configured
// catalog resolution for 186 IDs. It checks the returned count on every
// iteration and reports allocations with fixture construction outside the timed
// loop.
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
