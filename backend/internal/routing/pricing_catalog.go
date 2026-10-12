package routing

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

type ModelPriceReader interface {
	GetModelPricing(string) (*ModelPricing, error)
}

// PricingCatalog 提供价格目录快照、单价查询和更新函数。
type PricingCatalog struct {
	Query    func(DefaultPricingQuery) DefaultPricingPage
	Snapshot func() DefaultPricingSnapshot
	Update   func() error
	Prices   ModelPriceReader
}

// DefaultPricingSnapshot 固定一次查询的模型名、价格和更新时间。
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
