package usage

import "testing"

// Model grouping accepts three exact source labels and uses requested for
// unsupported text. The table cases check each supported value plus empty and
// unknown input, separating validation's boolean result from normalization's
// selected default.

// TestIsValidModelSource supplies the three supported source labels plus
// empty and unknown text. It checks the expected validity result for each
// table case.
func TestIsValidModelSource(t *testing.T) {
	tests := []struct {
		name   string // subtest case label
		source string // input model source text
		want   bool   // expected helper result
	}{
		{name: "requested", source: ModelSourceRequested, want: true},
		{name: "upstream", source: ModelSourceUpstream, want: true},
		{name: "mapping", source: ModelSourceMapping, want: true},
		{name: "invalid", source: "foobar", want: false},
		{name: "empty", source: "", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidModelSource(tc.source); got != tc.want {
				t.Fatalf("IsValidModelSource(%q)=%v want %v", tc.source, got, tc.want)
			}
		})
	}
}

// TestNormalizeModelSource supplies supported, empty and unknown source text.
// It requires supported labels to remain themselves and the other fixtures to
// select requested.
func TestNormalizeModelSource(t *testing.T) {
	tests := []struct {
		name   string // subtest case label
		source string // input model source text
		want   string // expected helper result
	}{
		{name: "requested", source: ModelSourceRequested, want: ModelSourceRequested},
		{name: "upstream", source: ModelSourceUpstream, want: ModelSourceUpstream},
		{name: "mapping", source: ModelSourceMapping, want: ModelSourceMapping},
		{name: "invalid falls back", source: "foobar", want: ModelSourceRequested},
		{name: "empty falls back", source: "", want: ModelSourceRequested},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeModelSource(tc.source); got != tc.want {
				t.Fatalf("NormalizeModelSource(%q)=%q want %q", tc.source, got, tc.want)
			}
		})
	}
}
