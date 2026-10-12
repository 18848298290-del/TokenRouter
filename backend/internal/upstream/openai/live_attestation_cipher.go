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

type LiveAttestationCipher struct {
	key [32]byte
}

func NewLiveAttestationCipher(secret string) *LiveAttestationCipher {
	if strings.TrimSpace(secret) == "" {
		return nil
	}
	return &LiveAttestationCipher{
		key: sha256.Sum256([]byte("tokenrouter/live-attestation/v1\x00" + secret)),
	}
}

// Encrypt returns raw standard Base64 containing the nonce followed by AES-GCM ciphertext and tag.
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
	encrypted := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawStdEncoding.EncodeToString(encrypted), nil
}

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
	if len(encrypted) < gcm.NonceSize() {
		return "", errors.New("encrypted Live attestation is too short")
	}
	plaintext, err := gcm.Open(nil, encrypted[:gcm.NonceSize()], encrypted[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt Live attestation: %w", err)
	}
	return string(plaintext), nil
}
