// Package pricing parses model catalogs and price cards and computes costs from usage, billing modes, and multipliers.
//
// Reading entry points:
//   - card_resolution.go combines cards and base prices and selects billing mode and source.
//   - calculate.go computes costs, display prices, and context intervals.
//   - catalog_parse.go parses catalog JSON into prices and validation diagnostics.
//
// File groups:
//   - card.go, card_resolution.go, resolve.go: card fields, model matching, interval validation, and overrides.
//   - calculate.go, types.go: cost calculations, display prices, and calculation inputs and outputs.
//   - catalog_types.go, catalog_parse.go, catalog_rules.go, catalog_extensions.go, catalog_query.go: catalog entries, parsing, supplemental rules, and model lookup.
//   - models_dev.go, model_price.go, default_catalog.go: models.dev conversion, model prices, and administrator default-price types.
//   - media.go, image_unit_price.go, image_billing_size.go, video_billing_resolution.go: media costs, unit prices, and billing sizes.
//   - time.go, settings.go: time-dependent prices, timezone validation, and billing settings.
//   - provider_stats.go: provider statistics rule matching and cost calculation.
package pricing
