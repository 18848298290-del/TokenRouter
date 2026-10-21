package payment

import "testing"

// order_snapshot_test.go checks which saved currency belongs to an order's
// subscription price. Tests keep the price and channel currencies different,
// exercise legacy missing metadata, and verify that account balance quantities
// do not acquire an external currency label.

// TestPaymentOrderAmountCurrency selects price units from subscription snapshots.
// Missing or invalid historical labels retain the payment currency, while
// balance orders and absent orders expose no monetary face-amount currency.
func TestPaymentOrderAmountCurrency(t *testing.T) {
	for _, tc := range []struct {
		name         string // named currency selection case
		orderType    string // persisted order purchase category
		planCurrency string // saved subscription price currency
		paidCurrency string // saved channel payment currency
		want         string // expected amount display currency
	}{
		{"converted subscription", OrderTypeSubscription, " usd ", "CNY", "USD"},
		{"other price currency", OrderTypeSubscription, "NZD", "USD", "NZD"},
		{"legacy missing price unit", OrderTypeSubscription, "", "HKD", "HKD"},
		{"invalid historical price unit", OrderTypeSubscription, "invalid", "USD", "USD"},
		{"missing both currencies", OrderTypeSubscription, "", "", "CNY"},
		{"balance ignores plan metadata", OrderTypeBalance, "USD", "CNY", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			order := &Order{OrderType: tc.orderType, ProviderSnapshot: map[string]any{"currency": tc.paidCurrency}}
			order.PlanSnapshot.Currency = tc.planCurrency
			if got := PaymentOrderAmountCurrency(order); got != tc.want {
				t.Fatalf("amount currency = %q, want %q", got, tc.want)
			}
		})
	}
	if got := PaymentOrderAmountCurrency(nil); got != "" {
		t.Fatalf("nil order amount currency = %q, want empty", got)
	}
}
