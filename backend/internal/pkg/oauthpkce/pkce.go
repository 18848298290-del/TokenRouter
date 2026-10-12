package oauthpkce

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// RandomBytes returns n cryptographically random bytes; n must be nonnegative.
func RandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Base64URL encodes bytes as unpadded URL-safe base64.
func Base64URL(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// Verifier returns a base64url verifier from 32 random bytes.
func Verifier() (string, error) {
	b, err := RandomBytes(32)
	if err != nil {
		return "", err
	}
	return Base64URL(b), nil
}

// HexVerifier returns 128 hexadecimal characters from 64 random bytes.
func HexVerifier() (string, error) {
	b, err := RandomBytes(64)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Challenge computes the S256 challenge for verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return Base64URL(sum[:])
}
