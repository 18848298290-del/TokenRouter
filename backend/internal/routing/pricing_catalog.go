package routing

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// Default pricing queries and administrative refreshes share one injected
// catalog runtime. The catalog supplies a published price snapshot and
// paginated query callbacks, while individual model lookups delegate to the
// same price reader used for billing.

// ModelPriceReader retrieves normalized billing prices for a complete model ID.
type ModelPriceReader interface {
	// GetModelPricing returns the billing price for the supplied model identity.
	// Missing or ambiguous prices return the reader's error, allowing callers to
	// distinguish absent pricing from a record with explicit zero amounts.
	GetModelPricing(string) (*ModelPricing, error)
}

// PricingCatalog connects default-price queries and refreshes to an injected
// catalog runtime.
type PricingCatalog struct {
	// Query supplies filtered pages to PricingHandler.ListDefaultPricing. When
	// nil, that handler uses Snapshot and performs filtering and pagination on
	// the returned values.
	Query func(DefaultPricingQuery) DefaultPricingPage // filtered default-price page callback
	// Snapshot supplies the unfiltered catalog and status. ListDefaultPricing
	// calls it when Query is nil, so that path requires a callable snapshot
	// reader.
	Snapshot func() DefaultPricingSnapshot // published catalog snapshot callback
	// Update requests a catalog refresh for PricingHandler.UpdateDefaultPricing.
	// A nil function makes that handler report its updater as unavailable.
	Update func() error     // catalog refresh operation callback
	Prices ModelPriceReader // individual model price reader
}

// DefaultPricingSnapshot contains published catalog prices with their version
// and refresh status.
type DefaultPricingSnapshot struct {
	Version   string                      // published catalog version
	LastError string                      // latest catalog refresh error
	Prices    []pricing.DefaultModelPrice // published default price entries
	UpdatedAt time.Time                   // latest successful update time
}

// ModelPricing represents normalized billing rates supplied by the shared
// pricing package.
type ModelPricing = pricing.ModelPricing

// DefaultPricingQuery selects filters and a page of default model prices.
type DefaultPricingQuery struct {
	// Search, Platform, and Mode filter default-price rows in the app's catalog
	// query. Search and Platform are trimmed and lowercased; Mode compares the
	// billing-mode name as supplied, including its case and whitespace.
	// Search is a model-name substring; Platform is a displayed platform filter;
	// Mode is a billing-mode filter.
	Search, Platform, Mode string
	// Offset and Limit apply after matching rows have contributed to Total. The
	// app query treats negative Offset as zero and nonpositive Limit as unbounded.
	Offset, Limit int // Offset is the page start; Limit is the returned-row bound.
}

// DefaultPricingPage combines a price snapshot with pagination and available
// platform filters.
type DefaultPricingPage struct {
	DefaultPricingSnapshot // queried catalog snapshot values
	// Platforms lists displayed providers across the full catalog before search,
	// platform, or billing-mode filtering. The app's catalog query normalizes and
	// sorts those choices.
	Platforms []string // available provider platform filters
	// Total counts matching rows before Offset and Limit apply, allowing pages to
	// report the complete filtered result size.
	Total int // matching model entry count
}

// DefaultPricing delegates a complete model lookup to the configured price
// reader. Supply a catalog with a non-nil Prices implementation; its returned
// price and errors follow the reader's ownership and lookup rules.
func (c *PricingCatalog) DefaultPricing(model string) (*ModelPricing, error) {
	return c.Prices.GetModelPricing(model)
}
