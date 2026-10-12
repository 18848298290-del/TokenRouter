package payment

import (
	"testing"
)

// TestCalculatePayAmount formats two-decimal channel charges across
// zero, negative, fractional, and large percentage-fee cases. It checks
// the expected strings, including upward rounding of small nonzero fees.
func TestCalculatePayAmount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string  // fee scenario label
		amount   float64 // channel base amount
		feeRate  float64 // configured fee percentage
		expected string  // expected channel charge string
	}{
		{
			name:     "zero fee rate returns same amount",
			amount:   100.00,
			feeRate:  0,
			expected: "100.00",
		},
		{
			name:     "negative fee rate returns same amount",
			amount:   50.00,
			feeRate:  -5,
			expected: "50.00",
		},
		{
			name:     "1 percent fee rate",
			amount:   100.00,
			feeRate:  1,
			expected: "101.00",
		},
		{
			name:     "5 percent fee on 200",
			amount:   200.00,
			feeRate:  5,
			expected: "210.00",
		},
		{
			name:     "fee rounds UP to 2 decimal places",
			amount:   100.00,
			feeRate:  3,
			expected: "103.00",
		},
		{
			name:     "fee rounds UP small remainder",
			amount:   10.00,
			feeRate:  3.33,
			expected: "10.34", // Ceiling a 0.333 fee gives 0.34 before adding the base.
		},
		{
			name:     "very small amount",
			amount:   0.01,
			feeRate:  1,
			expected: "0.02", // A nonzero 0.0001 fee rounds up to one minor unit.
		},
		{
			name:     "large amount",
			amount:   99999.99,
			feeRate:  10,
			expected: "109999.99", // Ceiling the 9999.999 fee adds 10000.00 to the base.
		},
		{
			name:     "100 percent fee rate doubles amount",
			amount:   50.00,
			feeRate:  100,
			expected: "100.00",
		},
		{
			name:     "precision 0.01 fee difference",
			amount:   100.00,
			feeRate:  1.01,
			expected: "101.01", // An exact 1.01 fee retains its two-decimal amount.
		},
		{
			name:     "precision 0.02 fee",
			amount:   100.00,
			feeRate:  1.02,
			expected: "101.02",
		},
		{
			name:     "zero amount with positive fee",
			amount:   0,
			feeRate:  5,
			expected: "0.00",
		},
		{
			name:     "fractional amount no fee",
			amount:   19.99,
			feeRate:  0,
			expected: "19.99",
		},
		{
			name:     "fractional fee that causes rounding up",
			amount:   33.33,
			feeRate:  7.77,
			expected: "35.92", // Ceiling the 2.589741 fee gives 2.59 before addition.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := CalculatePayAmountForCurrency(tt.amount, tt.feeRate, DefaultPaymentCurrency)
			if got != tt.expected {
				t.Fatalf("CalculatePayAmount(%v, %v) = %q, want %q", tt.amount, tt.feeRate, got, tt.expected)
			}
		})
	}
}

// TestCalculatePayAmountWithFee combines fixed and percentage fees in
// a two-decimal currency. It compares the complete breakdown for
// independent, combined, rounded, and absent fee components.
func TestCalculatePayAmountWithFee(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string       // fee scenario label
		base float64      // channel base amount
		fee  FeeConfig    // configured fee components
		want FeeBreakdown // expected full fee breakdown
	}{
		{
			name: "fixed fee only",
			base: 100,
			fee:  FeeConfig{FixedFee: 2.5},
			want: FeeBreakdown{BaseAmount: 100, FixedFee: 2.5, FeeAmount: 2.5, PayAmount: 102.5},
		},
		{
			name: "rate fee only",
			base: 100,
			fee:  FeeConfig{FeeRate: 2.2},
			want: FeeBreakdown{BaseAmount: 100, FeeRate: 2.2, FeeRateAmount: 2.2, FeeAmount: 2.2, PayAmount: 102.2},
		},
		{
			name: "fixed and rate fee",
			base: 100,
			fee:  FeeConfig{FixedFee: 2.5, FeeRate: 2.2},
			want: FeeBreakdown{BaseAmount: 100, FixedFee: 2.5, FeeRate: 2.2, FeeRateAmount: 2.2, FeeAmount: 4.7, PayAmount: 104.7},
		},
		{
			name: "rate fee rounds up",
			base: 10,
			fee:  FeeConfig{FeeRate: 3.33},
			want: FeeBreakdown{BaseAmount: 10, FeeRate: 3.33, FeeRateAmount: 0.34, FeeAmount: 0.34, PayAmount: 10.34},
		},
		{
			name: "zero fee",
			base: 88,
			fee:  FeeConfig{},
			want: FeeBreakdown{BaseAmount: 88, PayAmount: 88},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := CalculatePayAmountWithFeeForCurrency(tt.base, tt.fee, DefaultPaymentCurrency)
			if got != tt.want {
				t.Fatalf("CalculatePayAmountWithFee() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestCalculatePayAmountForCurrency applies the same percentage-fee
// calculation to JPY, KWD, ISK, and CNY. It checks whole-unit,
// three-decimal, legacy whole-unit display, and ordinary two-decimal
// formatted charge strings.
func TestCalculatePayAmountForCurrency(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string  // fee scenario label
		amount   float64 // channel base amount
		feeRate  float64 // configured fee percentage
		currency string  // channel currency code
		expected string  // expected channel charge string
	}{
		{
			name:     "zero decimal currency rounds fee up to whole unit",
			amount:   100,
			feeRate:  2.5,
			currency: "JPY",
			expected: "103",
		},
		{
			name:     "three decimal currency keeps three decimal places",
			amount:   12.345,
			feeRate:  1,
			currency: "KWD",
			expected: "12.469",
		},
		{
			name:     "stripe legacy zero decimal currency displays whole unit",
			amount:   100,
			feeRate:  2.5,
			currency: "ISK",
			expected: "103",
		},
		{
			name:     "default currency keeps existing two decimal behavior",
			amount:   10,
			feeRate:  3.33,
			currency: "CNY",
			expected: "10.34",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := CalculatePayAmountForCurrency(tt.amount, tt.feeRate, tt.currency)
			if got != tt.expected {
				t.Fatalf("CalculatePayAmountForCurrency(%v, %v, %q) = %q, want %q", tt.amount, tt.feeRate, tt.currency, got, tt.expected)
			}
		})
	}
}
