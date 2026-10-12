package bootstrap

import (
	"encoding/hex"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/config"
	cryptoinfra "github.com/TokenFlux/TokenRouter/internal/infra/crypto"
)

// AESEncryptor aliases the infrastructure AES encryptor.
type AESEncryptor = cryptoinfra.AESEncryptor

func NewAESEncryptor(cfg *config.Config) (*AESEncryptor, error) {
	key, err := hex.DecodeString(cfg.Totp.EncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("invalid totp encryption key: %w", err)
	}

	if len(key) != 32 {
		return nil, fmt.Errorf("totp encryption key must be 32 bytes (64 hex chars), got %d bytes", len(key))
	}

	return cryptoinfra.NewAESEncryptor(key)
}
