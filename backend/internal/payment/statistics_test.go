package payment

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// statistics_test.go checks currency ownership in purchase distributions and
// balance-unit acquisition prices. Stored order observations deliberately use
// different payment currencies and converted plan prices so tests detect
// arithmetic that combines amounts before their units are identified.

// TestPurchaseStatisticsSeparateCurrencies supplies the same purchase category in
// two currencies and a converted subscription order. Distribution totals stay
// separate, and weighted unit prices use paid principal after channel fees.
func TestPurchaseStatisticsSeparateCurrencies(t *testing.T) {
	monthlyLimit := 100.0
	orders := []*Order{
		{OrderType: OrderTypeBalance, Amount: 100, PayAmount: 52, FeeAmount: 2, ProviderSnapshot: map[string]any{"currency": "CNY"}},
		{OrderType: OrderTypeBalance, Amount: 300, PayAmount: 125, FeeAmount: 5, ProviderSnapshot: map[string]any{"currency": "CNY"}},
		{OrderType: OrderTypeBalance, Amount: 100, PayAmount: 10, ProviderSnapshot: map[string]any{"currency": "USD"}},
		{OrderType: OrderTypeSubscription, Amount: 10, PayAmount: 72, FeeAmount: 2, ProviderSnapshot: map[string]any{"currency": "CNY"}, PlanSnapshot: billing.SubscriptionPlanSnapshot{MonthlyLimitUSD: &monthlyLimit}},
	}
	items := StatsBuildPurchaseDistribution(orders, nil)
	require.Len(t, items, 3)
	require.Equal(t, "CNY", items[0].Currency)
	require.Equal(t, 177.0, items[0].Amount)
	require.Equal(t, 2, items[0].Count)
	require.Equal(t, "CNY", items[1].Currency)
	require.Equal(t, 72.0, items[1].Amount)
	require.Equal(t, "USD", items[2].Currency)
	require.Equal(t, 10.0, items[2].Amount)

	stats := &DashboardStats{}
	StatsComputeReasoningPointPurchaseUnitPrice(stats, orders)
	require.Equal(t, CurrencyAmounts{"CNY": 0.48, "USD": 0.1}, stats.AvgReasoningPointPurchaseUnitPrice)
	require.Equal(t, 4, stats.ReasoningPointPurchaseOrderCount)
	StatsComputeReasoningPointPurchaseUnitPrice(stats, nil)
	require.Empty(t, stats.AvgReasoningPointPurchaseUnitPrice)
	require.Zero(t, stats.ReasoningPointPurchaseOrderCount)
}

// TestPurchaseUnitPriceExcludesMissingEntitlements supplies subscriptions without
// usable monthly quota and orders without positive paid principal or balance units.
// None may contribute a price or inflate the contributing purchase count.
func TestPurchaseUnitPriceExcludesMissingEntitlements(t *testing.T) {
	orders := []*Order{
		{OrderType: OrderTypeSubscription, Amount: 10, PayAmount: 10},
		{OrderType: OrderTypeBalance, Amount: 10, PayAmount: 2, FeeAmount: 2},
		{OrderType: OrderTypeBalance, Amount: 0, PayAmount: 10},
	}
	stats := &DashboardStats{}
	StatsComputeReasoningPointPurchaseUnitPrice(stats, orders)
	require.Empty(t, stats.AvgReasoningPointPurchaseUnitPrice)
	require.Zero(t, stats.ReasoningPointPurchaseOrderCount)
}
