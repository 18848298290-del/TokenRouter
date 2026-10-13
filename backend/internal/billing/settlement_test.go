package billing

import (
	"math"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// Request fingerprints use raw captured amounts, while monetary counters
// require eight-decimal storage precision. These scenarios isolate tie
// rounding, sign symmetry, nonfinite pass-through and normalization order,
// with decimal arithmetic modeling repeated balance and quota reconciliation.

// TestUsageBillingCommandQuantizesAPIKeyCountersIdentically supplies the same
// ninth-decimal tie to total and rolling Key counters. Normalize must return
// equal amounts with at most eight decimal places; this test performs no
// balance SQL.
func TestUsageBillingCommandQuantizesAPIKeyCountersIdentically(t *testing.T) {
	// A charge of 0.0000625 USD scaled by 1.25 reaches this ninth-decimal tie,
	// allowing counter precision to be checked independently of token
	// calculation.
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

// TestQuantizeUsageBillingAmountBoundaries samples values around an
// eight-decimal rounding tie and a longer decimal tail. Results must match
// decimal rounding, fit the stored scale and differ by at most half a stored
// unit.
func TestQuantizeUsageBillingAmountBoundaries(t *testing.T) {
	cases := []struct {
		name string  // sampled rounding case label
		in   float64 // raw supplied USD amount
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

			// Compare with the decimal library's rounded float result; discard its
			// exactness indicator because this API and the tested result both use
			// float64.
			want, _ := decimal.NewFromFloat(tc.in).Round(UsageBillingMonetaryScale).Float64()
			require.Equal(t, want, got)

			// Half an eight-decimal unit bounds this rounding difference; the assertion
			// compares the returned float to its original supplied finite amount.
			require.LessOrEqual(t, math.Abs(got-tc.in), 5e-9)
		})
	}
}

// TestQuantizedAmountsReconcileExactlyOverManyApplications repeats a quantized
// tie charge in decimal arithmetic for four application counts. Computed
// balance deductions and quota increments must remain equal and retain
// eight-decimal storage precision.
func TestQuantizedAmountsReconcileExactlyOverManyApplications(t *testing.T) {
	// Reuse a ninth-decimal tie, so repetition tests reconciliation of the
	// normalized unit instead of repeated independent result-side rounding.
	const actualCost = 0.000078125

	unit := decimal.NewFromFloat(QuantizeUsageBillingAmount(actualCost))
	for _, n := range []int64{1, 10, 100, 1000} {
		total := unit.Mul(decimal.NewFromInt(n))

		balance := decimal.NewFromInt(10000).Sub(total)
		quotaUsed := total

		// Model storage precision with decimal Round on both accumulated values.
		// These exact arithmetic assertions check scale stability without issuing
		// PostgreSQL updates.
		require.True(t, balance.Equal(balance.Round(UsageBillingMonetaryScale)),
			"n=%d 余额结果超出 NUMERIC(20,8) 刻度", n)
		require.True(t, quotaUsed.Equal(quotaUsed.Round(UsageBillingMonetaryScale)),
			"n=%d 配额结果超出 NUMERIC(20,8) 刻度", n)

		balanceDelta := decimal.NewFromInt(10000).Sub(balance)
		require.True(t, balanceDelta.Equal(quotaUsed),
			"n=%d 余额 delta 与配额 delta 不相等: %s vs %s", n, balanceDelta, quotaUsed)
	}
}

// TestNormalizeQuantizesStorageBoundMonetaryFields supplies one long-tail
// amount to all financial fields. The three directly stored counters must fit
// eight decimals while billable and base amounts retain their supplied
// precision.
func TestNormalizeQuantizesStorageBoundMonetaryFields(t *testing.T) {
	// Choose an amount finer than storage precision to distinguish the counters
	// that normalize from the billable and base amounts retained for allocation.
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

// TestNormalizeKeepsFingerprintDerivedFromRawAmounts derives the expected
// digest from a fresh raw tie-valued command before Normalize. The resulting
// fingerprint must retain that pre-quantization identity for request replay
// comparison.
func TestNormalizeKeepsFingerprintDerivedFromRawAmounts(t *testing.T) {
	// Choose a ninth-decimal tie whose raw and rounded representations differ,
	// making fingerprint-before-quantization order observable in the expected
	// digest.
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

// TestNormalizePreservesExplicitFingerprint provides a preset digest alongside
// a long-tail Key quota amount. Normalize must retain the supplied digest
// verbatim while still rounding the monetary counter to storage precision.
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

// TestQuantizeUsageBillingAmountPassesThroughNonFinite supplies zero, NaN and
// both infinities to precision normalization. Zero must remain zero, and the
// returned nonfinite values must retain their respective classifications.
func TestQuantizeUsageBillingAmountPassesThroughNonFinite(t *testing.T) {
	require.Equal(t, 0.0, QuantizeUsageBillingAmount(0))
	require.True(t, math.IsNaN(QuantizeUsageBillingAmount(math.NaN())))
	require.True(t, math.IsInf(QuantizeUsageBillingAmount(math.Inf(1)), 1))
	require.True(t, math.IsInf(QuantizeUsageBillingAmount(math.Inf(-1)), -1))
}

// TestQuantizeUsageBillingAmountHandlesNegativeAmounts rounds a negative
// ninth-decimal tie using the decimal reference. Its result must equal that
// reference and the negated rounded positive amount, protecting
// half-away-from-zero symmetry.
func TestQuantizeUsageBillingAmountHandlesNegativeAmounts(t *testing.T) {
	got := QuantizeUsageBillingAmount(-0.000078125)
	// Use the decimal library as the finite negative tie reference. Its Float64
	// exactness indicator is irrelevant to comparing these returned monetary
	// floats.
	want, _ := decimal.NewFromFloat(-0.000078125).Round(UsageBillingMonetaryScale).Float64()

	require.Equal(t, want, got)
	require.Equal(t, -QuantizeUsageBillingAmount(0.000078125), got,
		"正负金额必须对称量化")
}

// decimalPlaces reports the negated exponent of a float's shortest decimal
// representation. The rounding tests use it as a scale check; callers supply
// finite values suitable for decimal.NewFromFloat.
func decimalPlaces(v float64) int32 {
	return -decimal.NewFromFloat(v).Exponent()
}
