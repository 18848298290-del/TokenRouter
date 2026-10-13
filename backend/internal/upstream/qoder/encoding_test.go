package qoder

import (
	"testing"
)

// Small plaintext fixtures cover COSY alphabet substitution and rearrangement
// through round trips. A separate fixed example rejects standard base64 output,
// so an encoder that merely delegates to the standard alphabet cannot satisfy
// the protocol expectations.

// TestEncodeDecodeRoundTrip encodes and decodes strings spanning different final
// base64-group lengths. Each case requires changed encoded text and exact
// recovery of the supplied plaintext.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := []string{
		"hello world",
		"test",
		"hello world!!!",
	}

	for _, tc := range cases {
		encoded := Encode([]byte(tc))
		if encoded == tc {
			t.Errorf("encode(%q) = %q, expected different", tc, encoded)
		}
		decoded, err := DecodeString(encoded)
		if err != nil {
			t.Errorf("decode(encode(%q)) error: %v", tc, err)
		}
		if decoded != tc {
			t.Errorf("decode(encode(%q)) = %q, want %q", tc, decoded, tc)
		}
	}
}

// TestEncodeNotStandardBase64 encodes the fixed text test using the COSY
// encoder. It checks that the result differs from its known standard-base64
// representation.
func TestEncodeNotStandardBase64(t *testing.T) {
	encoded := Encode([]byte("test"))
	if encoded == "dGVzdA==" {
		t.Error("encoded result should not be standard base64")
	}
}
