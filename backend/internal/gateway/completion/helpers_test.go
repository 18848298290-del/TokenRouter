package completion_test

import (
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingtestkit "github.com/TokenFlux/TokenRouter/internal/billing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	routingtestkit "github.com/TokenFlux/TokenRouter/internal/routing/testkit"
)

func i64p(v int64) *int64 {
	return &v
}

func newOpenAIImageConfigPricingResolverForTest(t *testing.T, groupID int64, model string, price float64) *billing.PriceResolver {
	t.Helper()
	cache := routingtestkit.NewModelConfigData()
	cache.Prices[routingtestkit.ModelKey{GroupID: groupID, Model: model}] = &routing.ModelPricingEntry{
		BillingMode:     routing.BillingModeImage,
		PerRequestPrice: &price,
	}
	cache.ByGroup[groupID] = &routingtestkit.Configuration{ID: groupID, Status: billing.StatusActive}
	cache.Platforms[groupID] = ""
	cache.LoadedAt = time.Now()

	cs := routingtestkit.ModelConfigFromData(cache)
	return billingtestkit.PriceResolver(cs, NewBillingService(nil))
}

// NewBillingService builds a calculator from the test catalog.
func NewBillingService(catalog *provider.Service) *billing.Calculator {
	return billingtestkit.Calculator(catalog, nil)
}
