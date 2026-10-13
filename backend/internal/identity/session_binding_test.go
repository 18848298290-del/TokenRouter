package identity

import (
	"testing"
)

// TestSessionBindingHash compares identical fingerprints and changes each input
// independently. It requires a nonempty digest for supplied client data and an
// empty digest for both an empty binding and a nil pointer.
func TestSessionBindingHash(t *testing.T) {
	a := &SessionBinding{IP: "1.2.3.4", UserAgent: "Mozilla/5.0"}
	b := &SessionBinding{IP: "1.2.3.4", UserAgent: "Mozilla/5.0"}
	if a.Hash() != b.Hash() {
		t.Fatalf("identical bindings must hash equal")
	}
	if a.Hash() == "" {
		t.Fatalf("non-empty binding must produce non-empty hash")
	}

	// Changing the IP changes the fingerprint.
	c := &SessionBinding{IP: "5.6.7.8", UserAgent: "Mozilla/5.0"}
	if a.Hash() == c.Hash() {
		t.Fatalf("changing IP must change hash")
	}
	// Changing the User-Agent changes the fingerprint.
	d := &SessionBinding{IP: "1.2.3.4", UserAgent: "curl/8.0"}
	if a.Hash() == d.Hash() {
		t.Fatalf("changing UA must change hash")
	}

	// Empty fingerprints have no hash, allowing unbound legacy tokens.
	empty := &SessionBinding{}
	if empty.Hash() != "" {
		t.Fatalf("empty binding must hash to empty string")
	}
	var nilBinding *SessionBinding
	if nilBinding.Hash() != "" {
		t.Fatalf("nil binding must hash to empty string")
	}
}
