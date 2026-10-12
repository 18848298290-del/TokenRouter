package tlsfingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// CacheKey hashes all profile fields for transport cache isolation.
// Changing a profile must select a new transport rather than reuse its old handshake settings.
func CacheKey(profile *Profile) string {
	if profile == nil {
		return "none"
	}
	payload := struct {
		Name                string   `json:"name"`
		CipherSuites        []uint16 `json:"cipher_suites"`
		Curves              []uint16 `json:"curves"`
		PointFormats        []uint16 `json:"point_formats"`
		EnableGREASE        bool     `json:"enable_grease"`
		SignatureAlgorithms []uint16 `json:"signature_algorithms"`
		ALPNProtocols       []string `json:"alpn_protocols"`
		SupportedVersions   []uint16 `json:"supported_versions"`
		KeyShareGroups      []uint16 `json:"key_share_groups"`
		PSKModes            []uint16 `json:"psk_modes"`
		Extensions          []uint16 `json:"extensions"`
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
		return "marshal-error"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
