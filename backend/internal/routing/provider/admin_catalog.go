package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// admin_catalog.go supplies display metadata for configured administrative
// model choices.
// Administrator model choices already contain their request IDs before metadata
// lookup. Display names fall back to those IDs, while OpenAI and Grok output
// shapes add ownership fields and Grok supplies xai ownership for an
// unrecognized catalog entry.

// AdminCatalogOptions supplies display metadata lookup for model IDs selected
// by routing. A nil catalog uses the ID as the name; a nonempty catalog display
// name replaces it, and OpenAI or Grok choices include model-object metadata.
func AdminCatalogOptions(catalog modelcatalog.Reader) routing.AdminCatalogOptions {
	return routing.AdminCatalogOptions{Lookup: func(kind routing.AdminCatalogKind, id string) routing.AdminCatalogModel {
		entry := modelcatalog.Entry{Model: id}
		if catalog != nil {
			entry = catalog.ModelEntry(id)
		}
		name := id
		if entry.Attributes.DisplayName != nil && *entry.Attributes.DisplayName != "" {
			name = *entry.Attributes.DisplayName
		}
		model := routing.AdminCatalogModel{ID: id, Type: "model", DisplayName: name}
		if kind == routing.CatalogOpenAI || kind == routing.CatalogGrok {
			model.Object = "model"
			model.OwnedBy = entry.Provider
		}
		// Grok's unknown-entry shape uses xai ownership and an empty type; a present
		// display-name pointer, even empty, keeps the regular catalog shape.
		if kind == routing.CatalogGrok && entry.Provider == "" && entry.Attributes.DisplayName == nil {
			model.Type = ""
			model.OwnedBy = "xai"
		}
		return model
	}}
}
