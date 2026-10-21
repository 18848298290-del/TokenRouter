package payment

import "testing"

// checkout_draft_test.go checks the price currency retained when checkout
// prepares a subscription order. The fixtures separate channel configuration,
// optional plan labels, and enabled conversion so later order rendering can
// use saved facts after global settings change.

// TestBuildCheckoutDraftCapturesSubscriptionPriceCurrency prepares subscriptions
// with explicit and unspecified price labels. Only an enabled CNY conversion
// supplies USD for an unspecified label; direct charges keep legacy selection.
func TestBuildCheckoutDraftCapturesSubscriptionPriceCurrency(t *testing.T) {
	for _, tc := range []struct {
		name         string  // named checkout currency case
		planCurrency string  // optional plan price label
		paidCurrency string  // selected channel currency code
		rate         float64 // configured CNY per USD
		wantSnapshot string  // saved subscription currency label
		wantDisplay  string  // resolved amount display currency
	}{
		{"explicit price", "USD", "CNY", 7, "USD", "USD"},
		{"unlabeled converted price", "", "CNY", 7, "USD", "USD"},
		{"unlabeled direct price", "", "CNY", 0, "", "CNY"},
		{"non-CNY direct price", "NZD", "NZD", 7, "NZD", "NZD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := &SubscriptionPlan{ID: 1, Name: "Plan", Price: 10, Currency: tc.planCurrency, ValidityDays: 30}
			draft := BuildCheckoutDraft(
				CreateOrderRequest{OrderType: OrderTypeSubscription, PaymentType: TypeStripe, Locale: "en"},
				&Buyer{ID: 1}, plan, &PaymentConfig{SubscriptionUSDToCNYRate: tc.rate}, 10, 10,
				FeeBreakdown{PayAmount: 70},
				&InstanceSelection{ProviderKey: TypeStripe, Config: map[string]string{"currency": tc.paidCurrency}},
			)
			if got := draft.Order.PlanSnapshot.Currency; got != tc.wantSnapshot {
				t.Fatalf("saved price currency = %q, want %q", got, tc.wantSnapshot)
			}
			if got := PaymentOrderAmountCurrency(&draft.Order); got != tc.wantDisplay {
				t.Fatalf("display currency = %q, want %q", got, tc.wantDisplay)
			}
			if plan.Currency != tc.planCurrency {
				t.Fatal("checkout changed the current plan currency")
			}
		})
	}
}
