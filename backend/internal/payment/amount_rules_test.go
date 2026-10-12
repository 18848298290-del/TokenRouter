package payment

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPaymentAmountToleranceForThreeDecimalCurrency compares CNY, JPY,
// and KWD margins. It keeps the legacy tolerance for the first two
// currencies and checks a half-minor-unit margin for KWD.
func TestPaymentAmountToleranceForThreeDecimalCurrency(t *testing.T) {
	t.Parallel()

	assert.Equal(t, ProviderAmountTolerance, PaymentAmountToleranceForCurrency("CNY"))
	assert.Equal(t, ProviderAmountTolerance, PaymentAmountToleranceForCurrency("JPY"))
	// Allow binary representation noise in the half-minor-unit float comparison.
	assert.InDelta(t, 0.0005, PaymentAmountToleranceForCurrency("KWD"), 1e-12)
}

// TestCalculateCreditedBalanceStillUsesRechargeMultiplier applies a
// fractional multiplier and a tenfold multiplier to recharge amounts.
// It checks the two exact credited results and keeps the paid amounts
// distinguishable from the resulting credited reasoning points.
func TestCalculateCreditedBalanceStillUsesRechargeMultiplier(t *testing.T) {
	t.Parallel()

	got := CalculateCreditedBalance(10, 0.14)
	if got != 1.4 {
		t.Fatalf("credited balance = %v, want 1.4", got)
	}

	got = CalculateCreditedBalance(69.90, 10)
	if got != 699 {
		t.Fatalf("credited balance = %v, want 699", got)
	}
}
