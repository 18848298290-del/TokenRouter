package provider

import (
	"time"

	"github.com/google/uuid"

	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// ConvergedInstallationID returns a stable installation_id for the provider.
// It prefers the configured device_id and otherwise derives an ID from the provider seed.
func ConvergedInstallationID(value *acctcore.Record, seed string) string {
	if value == nil {
		return ""
	}
	if deviceID := value.GetOpenAIDeviceID(); deviceID != "" {
		return deviceID
	}
	if seed == "" {
		return ""
	}
	// Keep this derivation prefix stable so existing installation IDs remain valid.
	return openai.DeriveStableUUIDv4("sub2api:codex-install-id:v2:" + seed)
}

func CodexFingerprintIDs(value *acctcore.Record, clientSessionID string, mode acctcore.CodexFingerprintMode) *openai.FingerprintIDs {
	if value == nil || mode == acctcore.CodexFingerprintOff {
		return nil
	}
	seed, ok := acctcore.CodexFingerprintSeed(value.Extra)
	if !ok {
		return nil
	}
	return openai.ResolveFingerprintIDs(value.ID, seed, clientSessionID, string(mode), func(seed string) string { return ConvergedInstallationID(value, seed) }, time.Now, func() string { return uuid.Must(uuid.NewV7()).String() })
}
