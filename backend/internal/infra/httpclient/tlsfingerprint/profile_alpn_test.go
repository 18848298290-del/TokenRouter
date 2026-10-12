package tlsfingerprint

import "testing"

// ALPN selection tests use independent profile values to check HTTP/1
// defaults, an explicit h2 list, and a custom extension order lacking ALPN.
// Variant construction then checks a separate HTTP/1 protocol slice while the
// supplied h2 profile retains its protocol count.

// TestSupportsHTTP2 compares nil, empty, h2-enabled, and ALPN-omitting
// profiles in parallel cases. It requires HTTP/2 eligibility only when
// effective protocol and extension choices support the advertised h2 path.
func TestSupportsHTTP2(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string   // case display name
		profile *Profile // supplied TLS selection profile
		want    bool     // expected HTTP/2 support decision
	}{
		{name: "nil profile uses http1 default", profile: nil, want: false},
		{name: "empty profile uses http1 default", profile: &Profile{}, want: false},
		{name: "explicit h2", profile: &Profile{ALPNProtocols: []string{"h2", "http/1.1"}}, want: true},
		{
			name:    "missing alpn extension",
			profile: &Profile{ALPNProtocols: []string{"h2"}, Extensions: []uint16{0, 10, 43}},
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SupportsHTTP2(tc.profile); got != tc.want {
				t.Fatalf("SupportsHTTP2() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestHTTP1OnlyProfileRemovesH2ALPN derives an HTTP/1 variant from an h2
// profile. It requires a distinct pointer, a sole HTTP/1.1 protocol, false
// HTTP/2 support, and retained input protocol length.
func TestHTTP1OnlyProfileRemovesH2ALPN(t *testing.T) {
	t.Parallel()

	profile := &Profile{Name: "h2", ALPNProtocols: []string{"h2", "http/1.1"}}
	got := HTTP1OnlyProfile(profile)
	if got == profile {
		t.Fatal("expected cloned profile")
	}
	if SupportsHTTP2(got) {
		t.Fatal("HTTP1OnlyProfile should remove h2")
	}
	if len(got.ALPNProtocols) != 1 || got.ALPNProtocols[0] != "http/1.1" {
		t.Fatalf("ALPNProtocols = %#v, want [http/1.1]", got.ALPNProtocols)
	}
	if len(profile.ALPNProtocols) != 2 {
		t.Fatal("original profile should not be mutated")
	}
}
