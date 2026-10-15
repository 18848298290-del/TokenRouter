package provider

import (
	"time"

	"github.com/google/uuid"

	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// codex_fingerprint.go derives stable installation IDs and supplies provider
// inputs for per-attempt Codex fingerprints. Codex fingerprint convergence
// keeps an installation ID stable across requests while allowing session modes
// to distinguish threads and turns. A configured device ID takes precedence
// over the stored seed; otherwise the seed enters a fixed UUID hash domain
// that determines the installation identity.
//
// The provider wrapper reads a seed before delegating attempt generation to
// OpenAI's fingerprint rules. That helper controls which mode changes sessions
// and threads, while this wrapper supplies the clock and fresh UUIDv7 turn
// IDs. Callers reuse the returned object for both headers and request metadata
// so one attempt presents consistent IDs.

// ConvergedInstallationID chooses the installation identity for a provider's
// fingerprint attempt. A nil record returns an empty string, and a configured
// device ID wins even when the supplied seed is empty.
//
// Without a device ID, an empty seed leaves convergence unavailable. A nonempty
// seed is hashed with the fixed installation domain, which keeps the derived
// UUID consistent for later attempts that use the same seed and distinguishes
// installation identity from the separate session and thread hash domains.
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
	// The hash domain separates installation UUIDs from session and thread UUIDs.
	// Changing it changes the identity sent by later attempts for the same
	// provider seed.
	return openai.DeriveStableUUIDv4("sub2api:codex-install-id:v2:" + seed)
}

// CodexFingerprintIDs asks the platform helper for one attempt's fingerprint
// IDs using the stored seed. Nil records, disabled modes, missing seeds, and
// unsupported modes return nil; callers reuse the result across the attempt's
// headers and body.
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
