package billing

import (
	"math"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestUsageBillingCommandQuantizesAPIKeyCountersIdentically covers the rounding discrepancy in #5229:
//
//	balance    = balance - $1
//	quota_used = quota_used + $1
//
// Both columns use NUMERIC(20,8), with half-away-from-zero rounding of each result.
// When the ninth decimal is a tie, subtraction and addition can round in opposite directions,
// making the balance and quota deltas differ by 1e-8.
//
// Quantizing the amount to eight decimals before either operation prevents that discrepancy.
func TestUsageBillingCommandQuantizesAPIKeyCountersIdentically(t *testing.T) {
	// 10 input tokens * 0.00000125 + 5 output tokens * 0.00001000 = 0.0000625.
	// 0.0000625 * 1.25 (group rate) = 0.000078125.
	const actualCost = 0.000078125

	cmd := &UsageBillingCommand{
		RequestID:           "req-5229",
		UserID:              1,
		APIKeyID:            2,
		ProviderID:          3,
		APIKeyQuotaCost:     actualCost,
		APIKeyRateLimitCost: actualCost,
	}
	cmd.Normalize()

	require.Equal(t, cmd.APIKeyQuotaCost, cmd.APIKeyRateLimitCost,
		"API Key 配额与窗口用量必须使用同一个规范金额")
	require.LessOrEqual(t, decimalPlaces(cmd.APIKeyQuotaCost), int32(UsageBillingMonetaryScale),
		"金额超过 NUMERIC(20,8) 刻度时 PostgreSQL 仍会在存储阶段舍入")
}

// TestQuantizeUsageBillingAmountBoundaries covers rounding around a ninth-decimal tie.
func TestQuantizeUsageBillingAmountBoundaries(t *testing.T) {
	cases := []struct {
		name string
		in   float64
	}{
		{"below_half", 0.000078120},
		{"just_below_half", 0.000078124},
		{"exact_half", 0.000078125},
		{"just_above_half", 0.000078126},
		{"above_half", 0.000078130},
		{"long_tail", 0.0000781234567},
		{"already_quantized", 0.00007813},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := QuantizeUsageBillingAmount(tc.in)

			require.LessOrEqual(t, decimalPlaces(got), int32(UsageBillingMonetaryScale))

			want, _ := decimal.NewFromFloat(tc.in).Round(UsageBillingMonetaryScale).Float64()
			require.Equal(t, want, got)

			// Rounding error cannot exceed half an eight-decimal unit (5e-9).
			require.LessOrEqual(t, math.Abs(got-tc.in), 5e-9)
		})
	}
}

// TestQuantizedAmountsReconcileExactlyOverManyApplications checks exact balance/quota parity after repeated charges.
func TestQuantizedAmountsReconcileExactlyOverManyApplications(t *testing.T) {
	const actualCost = 0.000078125

	unit := decimal.NewFromFloat(QuantizeUsageBillingAmount(actualCost))
	for _, n := range []int64{1, 10, 100, 1000} {
		total := unit.Mul(decimal.NewFromInt(n))

		balance := decimal.NewFromInt(10000).Sub(total)
		quotaUsed := total

		// Storing at NUMERIC(20,8) precision leaves both results unchanged.
		require.True(t, balance.Equal(balance.Round(UsageBillingMonetaryScale)),
			"n=%d 余额结果超出 NUMERIC(20,8) 刻度", n)
		require.True(t, quotaUsed.Equal(quotaUsed.Round(UsageBillingMonetaryScale)),
			"n=%d 配额结果超出 NUMERIC(20,8) 刻度", n)

		balanceDelta := decimal.NewFromInt(10000).Sub(balance)
		require.True(t, balanceDelta.Equal(quotaUsed),
			"n=%d 余额 delta 与配额 delta 不相等: %s vs %s", n, balanceDelta, quotaUsed)
	}
}

// TestNormalizeQuantizesStorageBoundMonetaryFields checks counter rounding while retaining settlement amount precision.
func TestNormalizeQuantizesStorageBoundMonetaryFields(t *testing.T) {
	const raw = 0.0000781234567

	cmd := &UsageBillingCommand{
		RequestID:           "req-5229-fields",
		UserID:              1,
		APIKeyID:            2,
		ProviderID:          3,
		BillableAmountUSD:   raw,
		BaseAmountUSD:       raw,
		APIKeyQuotaCost:     raw,
		APIKeyRateLimitCost: raw,
		ProviderQuotaCost:   raw,
	}
	cmd.Normalize()

	for name, got := range map[string]float64{
		"APIKeyQuotaCost":     cmd.APIKeyQuotaCost,
		"APIKeyRateLimitCost": cmd.APIKeyRateLimitCost,
		"ProviderQuotaCost":   cmd.ProviderQuotaCost,
	} {
		require.LessOrEqual(t, decimalPlaces(got), int32(UsageBillingMonetaryScale), name)
	}
	require.Equal(t, raw, cmd.BillableAmountUSD)
	require.Equal(t, raw, cmd.BaseAmountUSD)
}

// TestNormalizeKeepsFingerprintDerivedFromRawAmounts checks that fingerprinting precedes quantization.
// Reversing that order could change the fingerprint on a retry across an upgrade,
// producing a false fingerprint conflict for the same request ID.
func TestNormalizeKeepsFingerprintDerivedFromRawAmounts(t *testing.T) {
	const raw = 0.000078125

	newCmd := func() *UsageBillingCommand {
		return &UsageBillingCommand{
			RequestID:         "req-5229-fp",
			UserID:            1,
			APIKeyID:          2,
			ProviderID:        3,
			BillableAmountUSD: raw,
			APIKeyQuotaCost:   raw,
		}
	}

	cmd := newCmd()
	expected := buildUsageBillingFingerprint(newCmd())

	cmd.Normalize()
	require.Equal(t, expected, cmd.RequestFingerprint)
}

// TestNormalizePreservesExplicitFingerprint checks that preset fingerprints survive storage-precision rounding.
func TestNormalizePreservesExplicitFingerprint(t *testing.T) {
	cmd := &UsageBillingCommand{
		RequestID:          "req-5229-explicit",
		RequestFingerprint: "preset-fingerprint",
		APIKeyQuotaCost:    0.0000781234567,
	}
	cmd.Normalize()

	require.Equal(t, "preset-fingerprint", cmd.RequestFingerprint)
	require.LessOrEqual(t, decimalPlaces(cmd.APIKeyQuotaCost), int32(UsageBillingMonetaryScale))
}

func TestQuantizeUsageBillingAmountPassesThroughNonFinite(t *testing.T) {
	require.Equal(t, 0.0, QuantizeUsageBillingAmount(0))
	require.True(t, math.IsNaN(QuantizeUsageBillingAmount(math.NaN())))
	require.True(t, math.IsInf(QuantizeUsageBillingAmount(math.Inf(1)), 1))
	require.True(t, math.IsInf(QuantizeUsageBillingAmount(math.Inf(-1)), -1))
}

// TestQuantizeUsageBillingAmountHandlesNegativeAmounts checks symmetric half-away-from-zero rounding for negative amounts.
func TestQuantizeUsageBillingAmountHandlesNegativeAmounts(t *testing.T) {
	got := QuantizeUsageBillingAmount(-0.000078125)
	want, _ := decimal.NewFromFloat(-0.000078125).Round(UsageBillingMonetaryScale).Float64()

	require.Equal(t, want, got)
	require.Equal(t, -QuantizeUsageBillingAmount(0.000078125), got,
		"正负金额必须对称量化")
}

// decimalPlaces returns the scale of the float's shortest decimal representation,
// as used by decimal.NewFromFloat.
func decimalPlaces(v float64) int32 {
	return -decimal.NewFromFloat(v).Exponent()
}
