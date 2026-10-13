package qoder

import (
	"testing"
)

// Fixed signing inputs make the Center digest and Authorization envelope
// observable without transport. The Gateway signature case checks digest width
// alone, while the Center fixture checks an exact hash, keeping each assertion's
// verified scope clear.

// TestSignCenterRequest hashes a fixed date through the Center signing formula.
// It compares the complete digest with the known expected value for that
// protocol input.
func TestSignCenterRequest(t *testing.T) {
	sig := SignCenterRequest("test_date")
	const expected = "d97838794e12bb6a4402dd55f14d2f8e"
	if sig != expected {
		t.Errorf("signature = %q, want %q", sig, expected)
	}
}

// TestSignQoderRequest signs fixed payload, key, date, body, and path values. It
// checks the digest has thirty-two characters, leaving byte-for-byte formula
// coverage to other request-signature fixtures.
func TestSignQoderRequest(t *testing.T) {
	sig := SignQoderRequest("payload", "key", "date", "body", "/path")
	if len(sig) != 32 {
		t.Errorf("signature length = %d, want 32", len(sig))
	}
}

// TestComposeBearer builds Authorization from fixed payload and signature text.
// It checks the exact Bearer COSY prefix, separator positions, and retained
// component spelling.
func TestComposeBearer(t *testing.T) {
	bearer := ComposeBearer("payload_b64", "signature")
	expected := "Bearer COSY.payload_b64.signature"
	if bearer != expected {
		t.Errorf("bearer = %q, want %q", bearer, expected)
	}
}
