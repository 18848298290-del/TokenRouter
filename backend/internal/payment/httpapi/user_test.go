package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/payment"
)

// user_test.go checks the monetary identities published by authenticated and
// public order responses. Shared stored observations must retain subscription
// face amounts separately from channel charges, while balance quantities omit
// a monetary unit because account settings determine their presentation.

// TestOrderResultsSeparatePriceAndPaymentCurrencies supplies a converted
// subscription and a balance recharge. Both response builders retain their
// numeric amounts and publish price currency only for the subscription.
func TestOrderResultsSeparatePriceAndPaymentCurrencies(t *testing.T) {
	for _, orderType := range []string{payment.OrderTypeSubscription, payment.OrderTypeBalance} {
		t.Run(orderType, func(t *testing.T) {
			order := &payment.Order{
				OrderType: orderType, Amount: 10, PayAmount: 72, FeeAmount: 2,
				ProviderSnapshot: map[string]any{"currency": "CNY"},
			}
			order.PlanSnapshot.Currency = "USD"
			user := UserSanitizePaymentOrderForResponse(order)
			public := UserBuildPublicOrderResult(order)
			want := ""
			if orderType == payment.OrderTypeSubscription {
				want = "USD"
			}
			require.Equal(t, want, user.AmountCurrency)
			require.Equal(t, want, public.AmountCurrency)
			require.Equal(t, "CNY", user.Currency)
			require.Equal(t, "CNY", public.Currency)
			require.Equal(t, 10.0, user.Amount)
			require.Equal(t, 10.0, public.Amount)
			require.Equal(t, 72.0, user.PayAmount)
			require.Equal(t, 72.0, public.PayAmount)
			for _, result := range []any{user, public} {
				body, err := json.Marshal(result)
				require.NoError(t, err)
				if want == "" {
					require.NotContains(t, string(body), "amount_currency")
				} else {
					require.Contains(t, string(body), `"amount_currency":"USD"`)
				}
			}
		})
	}
}
