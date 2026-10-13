package oauthpkce

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// OAuth exchanges use random verifier text and an S256 digest encoded for a
// URL. Ordinary integrations request unpadded base64url verifiers, while the
// OpenAI OAuth wrapper requests the longer hexadecimal form before deriving its
// challenge.

// RandomBytes returns n cryptographically random bytes with a nil error.
// A zero length returns an empty slice, and a negative length panics during
// allocation. crypto/rand.Read terminates the program when io.ReadFull reports
// an error.
func RandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Base64URL returns bytes encoded with the unpadded URL-safe base64 alphabet.
func Base64URL(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// Verifier returns an unpadded base64url verifier derived from 32 random bytes.
// The result has 43 characters and a nil error when generation returns.
// An io.ReadFull error terminates the program through RandomBytes.
func Verifier() (string, error) {
	b, err := RandomBytes(32)
	if err != nil {
		return "", err
	}
	return Base64URL(b), nil
}

// HexVerifier returns a lowercase hexadecimal verifier derived from 64 random
// bytes for the OpenAI OAuth wrapper. The result has 128 characters and a nil
// error when generation returns. An io.ReadFull error terminates the program
// through RandomBytes.
func HexVerifier() (string, error) {
	b, err := RandomBytes(64)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Challenge computes the unpadded base64url SHA-256 digest of the supplied
// verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return Base64URL(sum[:])
}
