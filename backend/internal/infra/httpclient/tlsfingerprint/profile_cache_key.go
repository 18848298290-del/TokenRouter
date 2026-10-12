package tlsfingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Transport reuse follows the configured TLS profile identity, including its
// name and ordered handshake lists. JSON serialization retains
// nil-versus-empty list distinctions before SHA-256 hashing, so raw profile
// changes can select separate entries even when handshake construction later
// chooses equivalent defaults.

// CacheKey returns a SHA-256 identity for every configured profile field,
// using none for nil. JSON list ordering and nil-versus-empty spelling
// participate before handshake defaults are applied; callers keep profile
// slices stable while hashing. An unexpected marshal error selects the fixed
// marshal-error identity.
func CacheKey(profile *Profile) string {
	if profile == nil {
		return "none"
	}
	payload := struct {
		Name                string   `json:"name"`                 // configured profile identity name
		CipherSuites        []uint16 `json:"cipher_suites"`        // ordered cipher suite IDs
		Curves              []uint16 `json:"curves"`               // ordered supported group IDs
		PointFormats        []uint16 `json:"point_formats"`        // ordered point format IDs
		EnableGREASE        bool     `json:"enable_grease"`        // configured GREASE insertion flag
		SignatureAlgorithms []uint16 `json:"signature_algorithms"` // ordered signature scheme IDs
		ALPNProtocols       []string `json:"alpn_protocols"`       // ordered application protocol names
		SupportedVersions   []uint16 `json:"supported_versions"`   // ordered TLS version IDs
		KeyShareGroups      []uint16 `json:"key_share_groups"`     // ordered key share group IDs
		PSKModes            []uint16 `json:"psk_modes"`            // ordered PSK exchange mode IDs
		Extensions          []uint16 `json:"extensions"`           // ordered TLS extension IDs
	}{
		Name:                profile.Name,
		CipherSuites:        profile.CipherSuites,
		Curves:              profile.Curves,
		PointFormats:        profile.PointFormats,
		EnableGREASE:        profile.EnableGREASE,
		SignatureAlgorithms: profile.SignatureAlgorithms,
		ALPNProtocols:       profile.ALPNProtocols,
		SupportedVersions:   profile.SupportedVersions,
		KeyShareGroups:      profile.KeyShareGroups,
		PSKModes:            profile.PSKModes,
		Extensions:          profile.Extensions,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		// Current payload fields are JSON-supported values. Keep a fixed identity
		// for an unexpected encoding failure because this cache-key API has no error result.
		return "marshal-error"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
