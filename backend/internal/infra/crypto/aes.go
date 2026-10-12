package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

// Stored secrets need both confidentiality and a check that their encrypted
// bytes belong to the supplied key. AES-256-GCM encrypts a string and
// authenticates it before Decrypt returns any plaintext. Each encrypted value
// carries its own random nonce at the beginning, followed by ciphertext and
// the authentication tag, and standard base64 makes that combined value
// storable as text.
//
// The constructor copies the caller's 32-byte key so later changes to the
// supplied slice cannot change this encryptor. Encryption and decryption
// recreate the cipher from that retained key; exchanging values therefore
// requires encryptors configured with the same key and nonce-prefixed format.

// AESEncryptor encrypts stored secrets and authenticates them using AES-256-GCM.
type AESEncryptor struct {
	// key is an owned constructor copy; encryption and decryption read it as
	// immutable key material after the caller may have changed the input slice.
	key []byte
}

// NewAESEncryptor constructs a secret encryptor from exactly 32 key bytes. It
// copies the supplied slice, so callers may change their input after
// construction; any other length returns an error.
func NewAESEncryptor(key []byte) (*AESEncryptor, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("AES-256 key must be 32 bytes, got %d", len(key))
	}
	return &AESEncryptor{key: append([]byte(nil), key...)}, nil
}

// Encrypt returns an authenticated, standard-base64 representation of
// plaintext using this encryptor's retained AES-256 key. It generates a fresh
// GCM nonce for each call and propagates cipher setup or random-read errors.
//
// The nonce is also the initial output buffer passed to Seal, so the
// resulting bytes contain the nonce before the ciphertext and authentication
// tag. Decrypt uses that prefix to verify the complete value with the same
// key.
func (e *AESEncryptor) Encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	// Keep the nonce beside its authenticated ciphertext so Decrypt can recover
	// the per-value nonce without any separate stored nonce or caller argument.
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)

	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt returns plaintext after decoding standard base64 and verifying the
// nonce-prefixed AES-GCM value with this encryptor's retained key. Invalid
// encoding, insufficient nonce bytes, cipher setup errors, and failed
// authentication return an error.
//
// The leading GCM nonce-size bytes identify the nonce for the remainder of
// this value. Open verifies the ciphertext and tag before plaintext is
// returned, so a changed nonce, ciphertext, tag, or key fails authentication.
func (e *AESEncryptor) Decrypt(ciphertext string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}

	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertextData := data[:nonceSize], data[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, ciphertextData, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}
