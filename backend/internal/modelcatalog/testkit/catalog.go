package testkit

import (
	"encoding/json"
	"slices"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

// OfflineView returns a fixed test snapshot built from the embedded catalog and supplements.
var OfflineView = sync.OnceValue(func() modelcatalog.View {
	body, err := modelcatalog.Offline()
	if err != nil {
		panic(err)
	}
	catalog, err := modelcatalog.Parse(body)
	if err != nil {
		panic(err)
	}
	supplements := make(map[string]json.RawMessage)
	if err := json.Unmarshal(modelcatalog.Supplements(), &supplements); err != nil {
		panic(err)
	}
	for model, value := range supplements {
		if err := catalog.ApplyAttributeSupplement(model, value); err != nil {
			panic(err)
		}
	}
	for model, value := range supplements {
		for _, alias := range catalog.FirstPartyAliases(model) {
			if err := catalog.ApplyAttributeSupplement(alias, value); err != nil {
				panic(err)
			}
		}
	}
	return catalog.View()
})

// Catalog supplies test entries and a replaceable version for catalog consumers.
type Catalog struct {
	Entries map[string]modelcatalog.Entry
	Version string
}

// New constructs a catalog containing the supplied IDs, using each ID as its display name.
func New(ids ...string) *Catalog {
	c := &Catalog{Entries: map[string]modelcatalog.Entry{}, Version: "test"}
	for _, id := range ids {
		name := id
		c.Entries[id] = modelcatalog.Entry{Model: id, Attributes: modelcatalog.Attributes{DisplayName: &name}}
	}
	return c
}

// ModelIDs returns fixture candidates sorted by ID.
func (c *Catalog) ModelIDs() []string {
	ids := []string{}
	for id := range c.Entries {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// ModelEntry returns the metadata declared by the test.
func (c *Catalog) ModelEntry(id string) modelcatalog.Entry { return c.Entries[id] }

// ModelVersion returns the version controlled by the test.
func (c *Catalog) ModelVersion() string { return c.Version }
