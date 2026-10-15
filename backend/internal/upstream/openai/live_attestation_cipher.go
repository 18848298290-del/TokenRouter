package openai

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// live_attestation_cipher.go encrypts and decrypts stored Live attestation
// values using a server-derived AES-GCM key.
// Stored Live attestation is encrypted under a key derived from the server
// secret and a versioned domain string. Each encryption creates a fresh AES-GCM
// nonce and stores it before the authenticated ciphertext, letting later
// decryption recover the nonce from the unpadded standard-Base64 value.
//
// Secret whitespace is used only to detect a blank configuration; the actual
// key derivation includes the supplied secret verbatim. Rotating that value
// changes the decryption key for previously stored records. Authentication
// failure is returned to the caller instead of producing an empty usable token,
// and the cipher keeps only the derived fixed-size key.

// LiveAttestationCipher holds the derived AES key for stored attestation
// records.
type LiveAttestationCipher struct {
	key [32]byte // derived AES-256 key bytes
}

// NewLiveAttestationCipher derives a versioned SHA-256 key from secret. A
// whitespace-only secret disables encryption by returning nil, while a
// configured secret is hashed verbatim and must remain stable to decrypt
// existing records.
func NewLiveAttestationCipher(secret string) *LiveAttestationCipher {
	if strings.TrimSpace(secret) == "" {
		return nil
	}
	return &LiveAttestationCipher{
		key: sha256.Sum256([]byte("tokenrouter/live-attestation/v1\x00" + secret)),
	}
}

// Encrypt returns unpadded standard Base64 containing a fresh nonce, AES-GCM
// ciphertext, and tag. Callers use a configured nonnil cipher and retain the
// complete encoded value for decryption with the same key derived from the
// secret.
//
// The nonce occupies the prefix consumed by Decrypt, while GCM appends the
// authentication tag to the encrypted bytes. Cipher construction errors are
// returned before nonce generation. With Go's default rand.Reader, an
// operating-system random-source failure terminates the process; a replaced
// reader can return an io.ReadFull error before encoding.
func (c *LiveAttestationCipher) Encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate Live attestation nonce: %w", err)
	}
	// Prefix the fresh nonce to its authenticated ciphertext so storage retains
	// the complete input required for opening this record under the same derived key.
	encrypted := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawStdEncoding.EncodeToString(encrypted), nil
}

// Decrypt authenticates and opens the nonce-prefixed unpadded standard-Base64
// attestation. A nonnil cipher with the same derived key is required; malformed
// Base64, a missing nonce, and failed authentication return errors. Cipher
// construction errors are also returned.
//
// The prefix is split using the same GCM nonce size as Encrypt, and the
// remainder includes the authentication tag consumed by Open. Plaintext is
// returned after that complete remainder authenticates under the recovered
// nonce and retained key, so callers receive an error when that association
// cannot be verified.
func (c *LiveAttestationCipher) Decrypt(ciphertext string) (string, error) {
	encrypted, err := base64.RawStdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("decode Live attestation: %w", err)
	}
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	// Reject a missing nonce before slicing. The remaining bytes, including the
	// tag, are authenticated together by GCM.Open before any plaintext is returned.
	if len(encrypted) < gcm.NonceSize() {
		return "", errors.New("encrypted Live attestation is too short")
	}
	plaintext, err := gcm.Open(nil, encrypted[:gcm.NonceSize()], encrypted[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt Live attestation: %w", err)
	}
	return string(plaintext), nil
}
