package payment

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// AES256KeySize is the required key length (in bytes) for AES-256-GCM.
const AES256KeySize = 32

// Encrypt encrypts plaintext using AES-256-GCM with the given 32-byte key.
// The output format is "iv:authTag:ciphertext" where each component is base64-encoded,
// matching the Node.js crypto.ts format for cross-compatibility.
//
// Deprecated: provider configuration is stored as plaintext JSON.
// Encrypt remains available to create legacy ciphertext fixtures.
func Encrypt(plaintext string, key []byte) (string, error) {
	if len(key) != AES256KeySize {
		return "", fmt.Errorf("encryption key must be %d bytes, got %d", AES256KeySize, len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize()) // 12 bytes for GCM
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	// Seal appends the ciphertext + auth tag
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), nil)

	// Split sealed into ciphertext and auth tag (last 16 bytes)
	tagSize := gcm.Overhead()
	ciphertext := sealed[:len(sealed)-tagSize]
	authTag := sealed[len(sealed)-tagSize:]

	// Format: iv:authTag:ciphertext (all base64)
	return fmt.Sprintf("%s:%s:%s",
		base64.StdEncoding.EncodeToString(nonce),
		base64.StdEncoding.EncodeToString(authTag),
		base64.StdEncoding.EncodeToString(ciphertext),
	), nil
}

// Decrypt decrypts a ciphertext string produced by Encrypt.
// The input format is "iv:authTag:ciphertext" where each component is base64-encoded.
//
// Deprecated: provider configuration is stored as plaintext JSON.
// Decrypt reads legacy ciphertext until those records are saved again as JSON.
func Decrypt(ciphertext string, key []byte) (string, error) {
	if len(key) != AES256KeySize {
		return "", fmt.Errorf("encryption key must be %d bytes, got %d", AES256KeySize, len(key))
	}

	parts := strings.SplitN(ciphertext, ":", 3)
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid ciphertext format: expected iv:authTag:ciphertext")
	}

	nonce, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("decode IV: %w", err)
	}

	authTag, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode auth tag: %w", err)
	}

	encrypted, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	// Reconstruct the sealed data: ciphertext + authTag
	sealed := append(encrypted, authTag...)

	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}

// ConfiguredEncryptionKey validates the hexadecimal AES-256 key and warns when it is unavailable.
func ConfiguredEncryptionKey(raw string, configured bool) (EncryptionKey, string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, "payment encryption key not configured — encrypted payment config will be unavailable", nil
	}
	if !configured {
		return nil, "payment encryption/signing key is not explicitly configured; set TOTP_ENCRYPTION_KEY to enable payment resume tokens", nil
	}
	key, err := hex.DecodeString(value)
	if err != nil {
		return nil, "", fmt.Errorf("invalid payment encryption key (hex decode): %w", err)
	}
	if len(key) != 32 {
		return nil, "", fmt.Errorf("payment encryption key must be 32 bytes, got %d", len(key))
	}
	return EncryptionKey(key), "", nil
}

// parseProviderConfig reads JSON or AES-256-GCM ciphertext and reports whether it was readable.
func parseProviderConfig(stored string, encryptionKey []byte) (map[string]string, bool) {
	if stored == "" {
		return nil, true
	}
	var config map[string]string
	if err := json.Unmarshal([]byte(stored), &config); err == nil {
		return config, true
	}
	// Legacy instances can contain ciphertext encrypted with the deployment key.
	if len(encryptionKey) == AES256KeySize {
		if plaintext, err := Decrypt(stored, encryptionKey); err == nil {
			if err := json.Unmarshal([]byte(plaintext), &config); err == nil {
				return config, true
			}
		}
	}
	return nil, false
}
