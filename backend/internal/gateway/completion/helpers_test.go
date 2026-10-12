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

// i64p allocates a pointer to an identifier fixture value for tests.
func i64p(v int64) *int64 {
	return &v
}

// newOpenAIImageConfigPricingResolverForTest installs one image card for the
// specified group and model. The supplied dollar price is retained in the card,
// and the resolver combines that fixture configuration with historical test catalogue prices.
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

// NewBillingService creates a calculator using the supplied unstarted test catalogue.
// A nil catalogue selects billingtestkit's historical token and media fixtures;
// these fixed inputs make expected costs independent of live catalogue downloads.
func NewBillingService(catalog *provider.Service) *billing.Calculator {
	return billingtestkit.Calculator(catalog, nil)
}
