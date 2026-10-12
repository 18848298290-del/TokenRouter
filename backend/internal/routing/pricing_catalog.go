package routing

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

type ModelPriceReader interface {
	GetModelPricing(string) (*ModelPricing, error)
}

// PricingCatalog supplies catalogue snapshots, price queries, and refresh operations.
type PricingCatalog struct {
	Query    func(DefaultPricingQuery) DefaultPricingPage
	Snapshot func() DefaultPricingSnapshot
	Update   func() error
	Prices   ModelPriceReader
}

// DefaultPricingSnapshot pins model names, prices, and update time for one query.
type DefaultPricingSnapshot struct {
	Version   string
	LastError string
	Prices    []pricing.DefaultModelPrice
	UpdatedAt time.Time
}

type ModelPricing = pricing.ModelPricing

type DefaultPricingQuery struct {
	Search, Platform, Mode string
	Offset, Limit          int
}

type DefaultPricingPage struct {
	DefaultPricingSnapshot
	Platforms []string
	Total     int
}

func (c *PricingCatalog) DefaultPricing(model string) (*ModelPricing, error) {
	return c.Prices.GetModelPricing(model)
}
