package payment

import (
	"encoding/json"
	"testing"
	"time"
)

// Payment instance support and limits use visible aliases, positive numeric
// bounds and observed daily usage before strategy selection. These pure tests
// exercise exact limit edges, first-seen minimum-usage ties, timezone midnight
// and plaintext/historical configuration parsing; equality helpers treat nil
// and empty collections as equivalent for their assertions.

// TestInstanceSupportsType supplies comma lists, whitespace, direct aliases,
// missing matches and blank support. Each listed method decision must match
// its expected result, protecting compatibility and empty-support acceptance.
func TestInstanceSupportsType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string      // parallel support case name
		supportedTypes string      // supplied comma support text
		target         PaymentType // requested payment method label
		expected       bool        // expected method support result
	}{
		{
			name:           "exact match single type",
			supportedTypes: "alipay",
			target:         "alipay",
			expected:       true,
		},
		{
			name:           "no match single type",
			supportedTypes: "wxpay",
			target:         "alipay",
			expected:       false,
		},
		{
			name:           "match in comma-separated list",
			supportedTypes: "alipay,wxpay,stripe",
			target:         "wxpay",
			expected:       true,
		},
		{
			name:           "first in comma-separated list",
			supportedTypes: "alipay,wxpay",
			target:         "alipay",
			expected:       true,
		},
		{
			name:           "last in comma-separated list",
			supportedTypes: "alipay,wxpay,stripe",
			target:         "stripe",
			expected:       true,
		},
		{
			name:           "no match in comma-separated list",
			supportedTypes: "alipay,wxpay",
			target:         "stripe",
			expected:       false,
		},
		{
			name:           "empty target",
			supportedTypes: "alipay,wxpay",
			target:         "",
			expected:       false,
		},
		{
			name:           "types with spaces are trimmed",
			supportedTypes: " alipay , wxpay ",
			target:         "alipay",
			expected:       true,
		},
		{
			name:           "legacy alipay direct supports canonical visible method",
			supportedTypes: "alipay_direct",
			target:         "alipay",
			expected:       true,
		},
		{
			name:           "legacy wxpay direct supports canonical visible method",
			supportedTypes: "wxpay_direct",
			target:         "wxpay",
			expected:       true,
		},
		{
			name:           "empty supported types means all supported",
			supportedTypes: "",
			target:         "alipay",
			expected:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := InstanceSupportsType(tt.supportedTypes, tt.target)
			if got != tt.expected {
				t.Fatalf("InstanceSupportsType(%q, %q) = %v, want %v", tt.supportedTypes, tt.target, got, tt.expected)
			}
		})
	}
}

// TestGetInstanceChannelLimitsFallsBackToLegacyDirectAliases stores bounds
// under historical Alipay/WeChat direct keys. Visible-method reads must retain
// their tested max/min values, protecting missing-exact-key alias lookup.
func TestGetInstanceChannelLimitsFallsBackToLegacyDirectAliases(t *testing.T) {
	t.Parallel()

	inst := testInstance(1, TypeAlipay, makeLimitsJSON(TypeAlipayDirect, ChannelLimits{SingleMax: 66}))
	got := getInstanceChannelLimits(inst, TypeAlipay)
	if got.SingleMax != 66 {
		t.Fatalf("getInstanceChannelLimits() = %+v, want SingleMax=66", got)
	}

	wxInst := testInstance(2, TypeWxpay, makeLimitsJSON(TypeWxpayDirect, ChannelLimits{SingleMin: 8}))
	wxGot := getInstanceChannelLimits(wxInst, TypeWxpay)
	if wxGot.SingleMin != 8 {
		t.Fatalf("getInstanceChannelLimits() = %+v, want SingleMin=8", wxGot)
	}
}

// testInstance returns a fresh enabled instance fixture with the supplied
// numeric ID, provider key and limit text. Other fields retain zero values so
// pure limit/strategy helpers consume just those scenario observations.
func testInstance(id int64, providerKey, limits string) *ProviderInstance {
	return &ProviderInstance{
		ID:          id,
		ProviderKey: providerKey,
		Limits:      limits,
		Enabled:     true,
	}
}

// makeLimitsJSON encodes one method's channel limits for fixture construction.
// Callers supply encodable finite bounds; JSON marshal errors are discarded
// and yield the returned encoded text, which can be empty on failure.
func makeLimitsJSON(paymentType string, cl ChannelLimits) string {
	m := map[string]ChannelLimits{paymentType: cl}
	// Fixture encoding discards marshal errors because these cases supply finite
	// JSON bounds. If another caller supplies an unencodable value, empty text
	// can become an unlimited parsing input.
	b, _ := json.Marshal(m)
	return string(b)
}

// TestFilterByLimits compares expected surviving IDs for below/at/above single
// bounds and daily usage totals. It also checks unlimited and empty
// candidates, protecting strict excess rejection and input-order retention by
// the filter itself.
func TestFilterByLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string // parallel limit scenario name
		// candidates retains the supplied instance pointers and observed usage for
		// one case. Parallel subtests read those fixture objects without changing
		// their limit configuration.
		candidates  []instanceCandidate
		paymentType PaymentType // requested channel method label
		orderAmount float64     // new order numeric amount
		wantIDs     []int64     // expected ordered surviving IDs
	}{
		{
			name: "order below SingleMin is filtered out",
			candidates: []instanceCandidate{
				{inst: testInstance(1, "easypay", makeLimitsJSON("alipay", ChannelLimits{SingleMin: 10})), dailyUsed: 0},
			},
			paymentType: "alipay",
			orderAmount: 5,
			wantIDs:     nil,
		},
		{
			name: "order at exact SingleMin boundary passes",
			candidates: []instanceCandidate{
				{inst: testInstance(1, "easypay", makeLimitsJSON("alipay", ChannelLimits{SingleMin: 10})), dailyUsed: 0},
			},
			paymentType: "alipay",
			orderAmount: 10,
			wantIDs:     []int64{1},
		},
		{
			name: "order above SingleMax is filtered out",
			candidates: []instanceCandidate{
				{inst: testInstance(1, "easypay", makeLimitsJSON("alipay", ChannelLimits{SingleMax: 100})), dailyUsed: 0},
			},
			paymentType: "alipay",
			orderAmount: 150,
			wantIDs:     nil,
		},
		{
			name: "order at exact SingleMax boundary passes",
			candidates: []instanceCandidate{
				{inst: testInstance(1, "easypay", makeLimitsJSON("alipay", ChannelLimits{SingleMax: 100})), dailyUsed: 0},
			},
			paymentType: "alipay",
			orderAmount: 100,
			wantIDs:     []int64{1},
		},
		{
			name: "daily used + orderAmount exceeding dailyLimit is filtered out",
			candidates: []instanceCandidate{
				{inst: testInstance(1, "easypay", makeLimitsJSON("alipay", ChannelLimits{DailyLimit: 500})), dailyUsed: 480},
			},
			paymentType: "alipay",
			orderAmount: 30,
			wantIDs:     nil, // 480+30=510 > 500
		},
		{
			name: "daily used + orderAmount equal to dailyLimit passes (strict greater-than)",
			candidates: []instanceCandidate{
				{inst: testInstance(1, "easypay", makeLimitsJSON("alipay", ChannelLimits{DailyLimit: 500})), dailyUsed: 480},
			},
			paymentType: "alipay",
			orderAmount: 20,
			wantIDs:     []int64{1}, // 480+20=500, 500 > 500 is false → passes
		},
		{
			name: "daily used + orderAmount below dailyLimit passes",
			candidates: []instanceCandidate{
				{inst: testInstance(1, "easypay", makeLimitsJSON("alipay", ChannelLimits{DailyLimit: 500})), dailyUsed: 400},
			},
			paymentType: "alipay",
			orderAmount: 50,
			wantIDs:     []int64{1},
		},
		{
			name: "no limits configured passes through",
			candidates: []instanceCandidate{
				{inst: testInstance(1, "easypay", ""), dailyUsed: 99999},
			},
			paymentType: "alipay",
			orderAmount: 100,
			wantIDs:     []int64{1},
		},
		{
			name: "multiple candidates with partial filtering",
			candidates: []instanceCandidate{
				// singleMax=50, order=80 → filtered out
				{inst: testInstance(1, "easypay", makeLimitsJSON("alipay", ChannelLimits{SingleMax: 50})), dailyUsed: 0},
				// no limits → passes
				{inst: testInstance(2, "easypay", ""), dailyUsed: 0},
				// singleMin=100, order=80 → filtered out
				{inst: testInstance(3, "easypay", makeLimitsJSON("alipay", ChannelLimits{SingleMin: 100})), dailyUsed: 0},
				// daily limit ok → passes (500+80=580 < 1000)
				{inst: testInstance(4, "easypay", makeLimitsJSON("alipay", ChannelLimits{DailyLimit: 1000})), dailyUsed: 500},
			},
			paymentType: "alipay",
			orderAmount: 80,
			wantIDs:     []int64{2, 4},
		},
		{
			name: "zero SingleMin and SingleMax means no single-transaction limit",
			candidates: []instanceCandidate{
				{inst: testInstance(1, "easypay", makeLimitsJSON("alipay", ChannelLimits{SingleMin: 0, SingleMax: 0, DailyLimit: 0})), dailyUsed: 0},
			},
			paymentType: "alipay",
			orderAmount: 99999,
			wantIDs:     []int64{1},
		},
		{
			name: "all limits combined - order passes all checks",
			candidates: []instanceCandidate{
				{inst: testInstance(1, "easypay", makeLimitsJSON("alipay", ChannelLimits{SingleMin: 10, SingleMax: 200, DailyLimit: 1000})), dailyUsed: 500},
			},
			paymentType: "alipay",
			orderAmount: 50,
			wantIDs:     []int64{1},
		},
		{
			name: "all limits combined - order fails SingleMin",
			candidates: []instanceCandidate{
				{inst: testInstance(1, "easypay", makeLimitsJSON("alipay", ChannelLimits{SingleMin: 10, SingleMax: 200, DailyLimit: 1000})), dailyUsed: 500},
			},
			paymentType: "alipay",
			orderAmount: 5,
			wantIDs:     nil,
		},
		{
			name:        "empty candidates returns empty",
			candidates:  nil,
			paymentType: "alipay",
			orderAmount: 10,
			wantIDs:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := filterByLimits(tt.candidates, tt.paymentType, tt.orderAmount)
			gotIDs := make([]int64, len(got))
			for i, c := range got {
				gotIDs[i] = c.inst.ID
			}
			if !int64SliceEqual(gotIDs, tt.wantIDs) {
				t.Fatalf("filterByLimits() returned IDs %v, want %v", gotIDs, tt.wantIDs)
			}
		})
	}
}

// TestPickLeastAmount supplies different, equal, single and zero-usage
// candidates to the pure minimum helper. Each case must select the expected
// ID, including the first candidate when lowest usage ties.
func TestPickLeastAmount(t *testing.T) {
	t.Parallel()

	t.Run("picks candidate with lowest dailyUsed", func(t *testing.T) {
		t.Parallel()
		candidates := []instanceCandidate{
			{inst: testInstance(1, "easypay", ""), dailyUsed: 300},
			{inst: testInstance(2, "easypay", ""), dailyUsed: 100},
			{inst: testInstance(3, "easypay", ""), dailyUsed: 200},
		}
		got := pickLeastAmount(candidates)
		if got.inst.ID != 2 {
			t.Fatalf("pickLeastAmount() picked instance %d, want 2", got.inst.ID)
		}
	})

	t.Run("with equal dailyUsed picks the first one", func(t *testing.T) {
		t.Parallel()
		candidates := []instanceCandidate{
			{inst: testInstance(1, "easypay", ""), dailyUsed: 100},
			{inst: testInstance(2, "easypay", ""), dailyUsed: 100},
			{inst: testInstance(3, "easypay", ""), dailyUsed: 200},
		}
		got := pickLeastAmount(candidates)
		if got.inst.ID != 1 {
			t.Fatalf("pickLeastAmount() picked instance %d, want 1 (first with lowest)", got.inst.ID)
		}
	})

	t.Run("single candidate returns that candidate", func(t *testing.T) {
		t.Parallel()
		candidates := []instanceCandidate{
			{inst: testInstance(42, "easypay", ""), dailyUsed: 999},
		}
		got := pickLeastAmount(candidates)
		if got.inst.ID != 42 {
			t.Fatalf("pickLeastAmount() picked instance %d, want 42", got.inst.ID)
		}
	})

	t.Run("zero usage among non-zero picks zero", func(t *testing.T) {
		t.Parallel()
		candidates := []instanceCandidate{
			{inst: testInstance(1, "easypay", ""), dailyUsed: 500},
			{inst: testInstance(2, "easypay", ""), dailyUsed: 0},
			{inst: testInstance(3, "easypay", ""), dailyUsed: 300},
		}
		got := pickLeastAmount(candidates)
		if got.inst.ID != 2 {
			t.Fatalf("pickLeastAmount() picked instance %d, want 2", got.inst.ID)
		}
	})
}

// TestGetInstanceChannelLimits supplies absent/malformed/partial JSON and
// provider-specific channel keys. It checks expected bound values, including
// Stripe key precedence over the requested method label and zero results for
// missing entries.
func TestGetInstanceChannelLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string // parallel lookup scenario name
		// inst is a shared read-only instance fixture for this case. Its limit text
		// and provider key remain unchanged while the parallel lookup helper decodes
		// them.
		inst        *ProviderInstance
		paymentType PaymentType   // requested channel method label
		want        ChannelLimits // expected decoded channel bounds
	}{
		{
			name:        "empty limits string returns zero ChannelLimits",
			inst:        testInstance(1, "easypay", ""),
			paymentType: "alipay",
			want:        ChannelLimits{},
		},
		{
			name:        "invalid JSON returns zero ChannelLimits",
			inst:        testInstance(1, "easypay", "not-json{"),
			paymentType: "alipay",
			want:        ChannelLimits{},
		},
		{
			name: "valid JSON with matching payment type",
			inst: testInstance(1, "easypay",
				`{"alipay":{"singleMin":5,"singleMax":200,"dailyLimit":1000}}`),
			paymentType: "alipay",
			want:        ChannelLimits{SingleMin: 5, SingleMax: 200, DailyLimit: 1000},
		},
		{
			name: "payment type not in limits returns zero ChannelLimits",
			inst: testInstance(1, "easypay",
				`{"alipay":{"singleMin":5,"singleMax":200}}`),
			paymentType: "wxpay",
			want:        ChannelLimits{},
		},
		{
			name: "stripe provider uses stripe lookup key regardless of payment type",
			inst: testInstance(1, "stripe",
				`{"stripe":{"singleMin":10,"singleMax":500,"dailyLimit":5000}}`),
			paymentType: "alipay",
			want:        ChannelLimits{SingleMin: 10, SingleMax: 500, DailyLimit: 5000},
		},
		{
			name: "stripe provider ignores payment type key even if present",
			inst: testInstance(1, "stripe",
				`{"stripe":{"singleMin":10,"singleMax":500},"alipay":{"singleMin":1,"singleMax":100}}`),
			paymentType: "alipay",
			want:        ChannelLimits{SingleMin: 10, SingleMax: 500},
		},
		{
			name: "non-stripe provider uses payment type as lookup key",
			inst: testInstance(1, "easypay",
				`{"alipay":{"singleMin":5},"wxpay":{"singleMin":10}}`),
			paymentType: "wxpay",
			want:        ChannelLimits{SingleMin: 10},
		},
		{
			name: "valid JSON with partial limits (only dailyLimit)",
			inst: testInstance(1, "easypay",
				`{"alipay":{"dailyLimit":800}}`),
			paymentType: "alipay",
			want:        ChannelLimits{DailyLimit: 800},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := getInstanceChannelLimits(tt.inst, tt.paymentType)
			if got != tt.want {
				t.Fatalf("getInstanceChannelLimits() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestStartOfDay supplies midday, midnight, final-second and fixed-timezone
// values. The returned time must equal the expected date midnight and retain
// the tested location name, protecting calendar rather than elapsed-time
// truncation.
func TestStartOfDay(t *testing.T) {
	t.Parallel()

	// Use a fixed eight-hour offset expressed in seconds for the location case.
	tests := []struct {
		name string    // parallel calendar scenario name
		in   time.Time // supplied location time value
		want time.Time // expected date midnight value
	}{
		{
			name: "midday returns midnight of same day",
			in:   time.Date(2025, 6, 15, 14, 30, 45, 123456789, time.UTC),
			want: time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "midnight returns same time",
			in:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			want: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "last second of day returns midnight of same day",
			in:   time.Date(2025, 12, 31, 23, 59, 59, 999999999, time.UTC),
			want: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "preserves timezone location",
			in:   time.Date(2025, 3, 10, 15, 0, 0, 0, time.FixedZone("CST", 8*3600)),
			want: time.Date(2025, 3, 10, 0, 0, 0, 0, time.FixedZone("CST", 8*3600)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := startOfDay(tt.in)
			if !got.Equal(tt.want) {
				t.Fatalf("startOfDay(%v) = %v, want %v", tt.in, got, tt.want)
			}
			// Instant equality alone can accept a time represented in another location.
			// Check the location name too so this fixture protects the calendar context
			// used by later daily bounds.
			if got.Location().String() != tt.want.Location().String() {
				t.Fatalf("startOfDay() location = %v, want %v", got.Location(), tt.want.Location())
			}
		})
	}
}

// TestDecryptConfig_PlaintextAndLegacyCompat supplies plaintext, encrypted,
// wrong-key, missing-key and garbage configurations. The decoded map must be
// equal under the fixture helper, whose nil/empty equivalence protects
// readable values and empty-equivalent recovery results for these cases.
func TestDecryptConfig_PlaintextAndLegacyCompat(t *testing.T) {
	t.Parallel()

	key := make([]byte, AES256KeySize)
	for i := range key {
		key[i] = byte(i + 1)
	}
	wrongKey := make([]byte, AES256KeySize)
	for i := range wrongKey {
		wrongKey[i] = byte(0xFF - i)
	}

	plaintextJSON := `{"appId":"app-123","secret":"sec-xyz"}`

	legacyEncrypted, err := Encrypt(plaintextJSON, key)
	if err != nil {
		t.Fatalf("seed Encrypt: %v", err)
	}

	tests := []struct {
		name   string // parallel decoding scenario name
		stored string // supplied persisted configuration text
		// key retains the fixture byte slice read by historical decryption. Parallel
		// cases keep those bytes immutable while each constructs its own balancer
		// and decoded result.
		key []byte
		// want is the expected read-only scalar config map. stringMapEqual treats
		// nil and empty as equivalent, so these assertions do not require a
		// particular empty-map allocation.
		want map[string]string
	}{
		{
			name:   "empty stored returns nil map",
			stored: "",
			key:    key,
			want:   nil,
		},
		{
			name:   "plaintext JSON parses directly",
			stored: plaintextJSON,
			key:    nil,
			want:   map[string]string{"appId": "app-123", "secret": "sec-xyz"},
		},
		{
			name:   "plaintext JSON works even with key present",
			stored: plaintextJSON,
			key:    key,
			want:   map[string]string{"appId": "app-123", "secret": "sec-xyz"},
		},
		{
			name:   "legacy ciphertext with correct key decrypts",
			stored: legacyEncrypted,
			key:    key,
			want:   map[string]string{"appId": "app-123", "secret": "sec-xyz"},
		},
		{
			name:   "legacy ciphertext with no key treated as empty",
			stored: legacyEncrypted,
			key:    nil,
			want:   nil,
		},
		{
			name:   "legacy ciphertext with wrong key treated as empty",
			stored: legacyEncrypted,
			key:    wrongKey,
			want:   nil,
		},
		{
			name:   "garbage data treated as empty",
			stored: "not-json-and-not-ciphertext",
			key:    key,
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lb := NewDefaultLoadBalancer(nil, tt.key)
			got := lb.decryptConfig(tt.stored)
			if !stringMapEqual(got, tt.want) {
				t.Fatalf("decryptConfig = %v, want %v", got, tt.want)
			}
		})
	}
}

// stringMapEqual reports equal scalar key/value entries with nil and empty
// maps considered equivalent. Equal lengths alone do not suffice; every key
// must exist with matching text in the other map.
func stringMapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// int64SliceEqual compares ordered ID entries with nil and empty slices
// considered equivalent. Nonempty length/order differences return false,
// letting filter tests assert both membership and retained input sequence.
func int64SliceEqual(a, b []int64) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
