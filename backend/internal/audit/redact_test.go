package audit

import (
	"strings"
	"testing"
)

func TestMaskAuditCredential(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"short", "abc", "****"},
		{"boundary_14", "12345678901234", "****"},
		{"long", "sk-ant-api03-abcdefghijklmnop1234", "sk-ant****1234"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MaskAuditCredential(tc.in)
			if got != tc.want {
				t.Fatalf("MaskAuditCredential(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// The masked result must not contain the complete credential.
			if len(tc.in) > 14 && strings.Contains(got, tc.in) {
				t.Fatalf("masked value leaks full credential: %q", got)
			}
		})
	}
}
