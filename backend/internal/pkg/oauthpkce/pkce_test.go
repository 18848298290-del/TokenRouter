package oauthpkce

import (
	"encoding/base64"
	"encoding/hex"
	"testing"
)

// TestPlatformVerifierFormats checks base64url and hexadecimal verifier lengths and encoding.
func TestPlatformVerifierFormats(t *testing.T) {
	verifier, err := Verifier()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(verifier)
	if err != nil || len(decoded) != 32 || len(verifier) != 43 {
		t.Fatalf("invalid base64 verifier: length=%d decoded=%d err=%v", len(verifier), len(decoded), err)
	}
	hexVerifier, err := HexVerifier()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = hex.DecodeString(hexVerifier)
	if err != nil || len(decoded) != 64 || len(hexVerifier) != 128 {
		t.Fatalf("invalid hex verifier: length=%d decoded=%d err=%v", len(hexVerifier), len(decoded), err)
	}
}

// TestS256RFCVector checks the digest and unpadded encoding against the RFC test vector.
func TestS256RFCVector(t *testing.T) {
	if got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("unexpected challenge: %s", got)
	}
}
