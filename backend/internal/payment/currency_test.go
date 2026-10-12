package payment

import (
	"math"
	"testing"
)

// TestYuanToFen parses signed, zero, integer, and decimal CNY inputs.
// It checks exact fen results for values sensitive to binary-float
// scaling and requires errors for the listed malformed amount strings.
func TestYuanToFen(t *testing.T) {
	tests := []struct {
		name    string // conversion scenario label
		input   string // CNY amount text
		want    int64  // expected converted amount
		wantErr bool   // expected parse rejection
	}{
		{name: "one yuan", input: "1.00", want: 100},
		{name: "ten yuan fifty fen", input: "10.50", want: 1050},
		{name: "one fen", input: "0.01", want: 1},
		{name: "large amount", input: "99999.99", want: 9999999},

		{name: "zero no decimal", input: "0", want: 0},
		{name: "zero with decimal", input: "0.00", want: 0},

		{name: "ieee754 precision 1.15", input: "1.15", want: 115},

		{name: "ieee754 precision 0.1", input: "0.1", want: 10},
		{name: "ieee754 precision 0.2", input: "0.2", want: 20},
		{name: "ieee754 precision 33.33", input: "33.33", want: 3333},

		{name: "hundred thousand", input: "100000.00", want: 10000000},

		{name: "integer 5", input: "5", want: 500},
		{name: "integer 100", input: "100", want: 10000},

		{name: "single decimal 1.5", input: "1.5", want: 150},

		{name: "negative one yuan", input: "-1.00", want: -100},
		{name: "negative with fen", input: "-10.50", want: -1050},

		{name: "empty string", input: "", wantErr: true},
		{name: "alphabetic", input: "abc", wantErr: true},
		{name: "double dot", input: "1.2.3", wantErr: true},
		{name: "spaces", input: "  ", wantErr: true},
		{name: "special chars", input: "$10.00", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := YuanToFen(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("YuanToFen(%q) expected error, got %d", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("YuanToFen(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("YuanToFen(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

// TestFenToYuan converts positive, negative, zero, and large fen values
// back to CNY units. It checks the expected float amounts within the
// asserted tolerance after integer-to-decimal conversion.
func TestFenToYuan(t *testing.T) {
	tests := []struct {
		name string  // conversion scenario label
		fen  int64   // table fen amount
		want float64 // expected converted amount
	}{
		{name: "one yuan", fen: 100, want: 1.0},
		{name: "ten yuan fifty fen", fen: 1050, want: 10.5},
		{name: "one fen", fen: 1, want: 0.01},
		{name: "zero", fen: 0, want: 0.0},
		{name: "large amount", fen: 9999999, want: 99999.99},
		{name: "negative", fen: -100, want: -1.0},
		{name: "negative with fen", fen: -1050, want: -10.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FenToYuan(tt.fen)
			// Allow binary representation noise when comparing decimal-valued float
			// results after currency conversion.
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("FenToYuan(%d) = %f, want %f", tt.fen, got, tt.want)
			}
		})
	}
}

// TestYuanToFenRoundTrip converts four CNY strings to their expected
// fen integers. It checks those integers first, then compares reverse
// conversion with the reverse conversion of each table's expected fen
// value.
func TestYuanToFenRoundTrip(t *testing.T) {
	cases := []struct {
		yuan string // yuan amount text
		fen  int64  // table fen amount
	}{
		{"0.01", 1},
		{"1.00", 100},
		{"10.50", 1050},
		{"99999.99", 9999999},
	}

	for _, tc := range cases {
		fen, err := YuanToFen(tc.yuan)
		if err != nil {
			t.Fatalf("YuanToFen(%q) unexpected error: %v", tc.yuan, err)
		}
		if fen != tc.fen {
			t.Errorf("YuanToFen(%q) = %d, want %d", tc.yuan, fen, tc.fen)
		}
		yuan := FenToYuan(fen)
		expectedYuan := FenToYuan(tc.fen)
		// Allow binary representation noise when comparing decimal-valued float
		// results after currency conversion.
		if math.Abs(yuan-expectedYuan) > 1e-9 {
			t.Errorf("round-trip: FenToYuan(%d) = %f, want %f", fen, yuan, expectedYuan)
		}
	}
}

// TestPaymentCurrencyHelpers converts HKD, JPY, KWD, ISK, UGX, and
// empty-currency inputs to API integers and back. It checks distinct
// API exponents, retained whole-unit legacy encoding, and the default
// CNY selection.
func TestPaymentCurrencyHelpers(t *testing.T) {
	tests := []struct {
		name      string  // conversion scenario label
		currency  string  // payment currency code
		amount    string  // payment amount text
		wantMinor int64   // expected API integer amount
		wantBack  float64 // expected major-unit amount
	}{
		{name: "hkd uses cents", currency: "hkd", amount: "12.34", wantMinor: 1234, wantBack: 12.34},
		{name: "jpy has no minor unit", currency: "JPY", amount: "12", wantMinor: 12, wantBack: 12},
		{name: "kwd uses three decimal minor units", currency: "KWD", amount: "12.345", wantMinor: 12345, wantBack: 12.345},
		{name: "isk uses Stripe legacy two-decimal API amount", currency: "ISK", amount: "12", wantMinor: 1200, wantBack: 12},
		{name: "ugx uses Stripe legacy two-decimal API amount", currency: "UGX", amount: "12.00", wantMinor: 1200, wantBack: 12},
		{name: "empty currency defaults to cny", currency: "", amount: "1.23", wantMinor: 123, wantBack: 1.23},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := AmountToMinorUnit(tt.amount, tt.currency)
			if err != nil {
				t.Fatalf("AmountToMinorUnit(%q, %q) unexpected error: %v", tt.amount, tt.currency, err)
			}
			if got != tt.wantMinor {
				t.Fatalf("AmountToMinorUnit(%q, %q) = %d, want %d", tt.amount, tt.currency, got, tt.wantMinor)
			}
			back := MinorUnitToAmount(got, tt.currency)
			// Allow binary representation noise when comparing decimal-valued float
			// results after currency conversion.
			if math.Abs(back-tt.wantBack) > 1e-9 {
				t.Fatalf("MinorUnitToAmount(%d, %q) = %f, want %f", got, tt.currency, back, tt.wantBack)
			}
		})
	}
}

// TestFormatAmountForCurrency formats CNY, JPY, KWD, and ISK amounts.
// It checks the exact strings, covering fixed trailing places,
// whole-unit display, and three-decimal amount precision.
func TestFormatAmountForCurrency(t *testing.T) {
	tests := []struct {
		currency string  // payment currency code
		amount   float64 // numeric payment amount
		want     string  // expected formatted amount
	}{
		{currency: "CNY", amount: 12.3, want: "12.30"},
		{currency: "JPY", amount: 12, want: "12"},
		{currency: "KWD", amount: 12.345, want: "12.345"},
		{currency: "ISK", amount: 12, want: "12"},
	}

	for _, tt := range tests {
		t.Run(tt.currency, func(t *testing.T) {
			if got := FormatAmountForCurrency(tt.amount, tt.currency); got != tt.want {
				t.Fatalf("FormatAmountForCurrency(%v, %q) = %q, want %q", tt.amount, tt.currency, got, tt.want)
			}
		})
	}
}

// TestAmountToMinorUnitRejectsUnsupportedPrecision supplies nonzero
// fractions beyond the accepted digits for ordinary and legacy
// currencies. It checks rejection for those inputs while permitting
// trailing zeros on a whole-number JPY amount.
func TestAmountToMinorUnitRejectsUnsupportedPrecision(t *testing.T) {
	if _, err := AmountToMinorUnit("100.50", "JPY"); err == nil {
		t.Fatal("expected fractional JPY amount to fail")
	}
	if _, err := AmountToMinorUnit("100.50", "ISK"); err == nil {
		t.Fatal("expected fractional ISK amount to fail")
	}
	if _, err := AmountToMinorUnit("100.50", "UGX"); err == nil {
		t.Fatal("expected fractional UGX amount to fail")
	}
	if _, err := AmountToMinorUnit("12.345", "HKD"); err == nil {
		t.Fatal("expected amount with more than two decimal places to fail")
	}
	if _, err := AmountToMinorUnit("12.3456", "KWD"); err == nil {
		t.Fatal("expected amount with more than three decimal places to fail")
	}
	if got, err := AmountToMinorUnit("100.00", "JPY"); err != nil || got != 100 {
		t.Fatalf("AmountToMinorUnit integer-form JPY = (%d, %v), want (100, nil)", got, err)
	}
}

// TestThreeDecimalPaymentCurrencies converts the same three-decimal
// amount for every three-decimal entry listed by the test. It checks
// the exact API integer and reverse float conversion for each code.
func TestThreeDecimalPaymentCurrencies(t *testing.T) {
	for _, currency := range []string{"BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND"} {
		t.Run(currency, func(t *testing.T) {
			got, err := AmountToMinorUnit("12.345", currency)
			if err != nil {
				t.Fatalf("AmountToMinorUnit(%q, %q) unexpected error: %v", "12.345", currency, err)
			}
			if got != 12345 {
				t.Fatalf("AmountToMinorUnit(%q, %q) = %d, want 12345", "12.345", currency, got)
			}
			// Allow binary representation noise when comparing decimal-valued float
			// results after currency conversion.
			if back := MinorUnitToAmount(got, currency); math.Abs(back-12.345) > 1e-9 {
				t.Fatalf("MinorUnitToAmount(%d, %q) = %f, want 12.345", got, currency, back)
			}
		})
	}
}

// TestNormalizePaymentCurrencyRejectsInvalidCodes supplies a two-letter
// code and a code containing a digit. It requires both inputs to
// produce errors from syntactic currency normalization.
func TestNormalizePaymentCurrencyRejectsInvalidCodes(t *testing.T) {
	if _, err := NormalizePaymentCurrency("HK"); err == nil {
		t.Fatal("expected invalid two-letter currency to fail")
	}
	if _, err := NormalizePaymentCurrency("US1"); err == nil {
		t.Fatal("expected non-letter currency to fail")
	}
}
