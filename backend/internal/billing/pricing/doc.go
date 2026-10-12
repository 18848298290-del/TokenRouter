// Package pricing parses model catalogs and price cards and calculates usage
// costs in USD.
// Price presence remains separate from numeric zero, so callers can distinguish
// unavailable prices from free usage across token, request, image, and video
// billing.
//
// Reading entry points:
//   - card_resolution.go combines cards and base prices and selects
//     billing mode and source.
//   - calculate.go computes costs, display prices, and context
//     intervals.
//   - catalog_parse.go parses catalog JSON into prices and validation
//     diagnostics.
//
// File groups:
//   - card.go, card_resolution.go, and resolve.go define cards, match
//     models, validate intervals, and apply overrides.
//   - calculate.go and types.go calculate costs and describe display and
//     settlement records.
//   - catalog_types.go, catalog_parse.go, catalog_rules.go,
//     catalog_extensions.go, and catalog_query.go retain catalog
//     presence, parse entries, derive rules, and query full identities.
//   - models_dev.go, model_price.go, and default_catalog.go convert
//     catalog amounts and describe administrator default-price rows.
//   - media.go, image_unit_price.go, image_billing_size.go, and
//     video_billing_resolution.go select media units, sizes, and fixed
//     prices.
//   - time.go and settings.go validate daily schedules and evaluate
//     billing policy using supplied clocks and locations.
//   - provider_stats.go selects independent provider cost rules and
//     computes their amounts.
package pricing
