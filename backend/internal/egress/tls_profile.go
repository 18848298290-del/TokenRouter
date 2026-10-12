package egress

import (
	"time"
)

// A saved fingerprint supplies ordered TLS ClientHello parameters to the
// transport converter. Empty lists let the dialer choose its built-in values,
// while a nonempty extension list determines which parameter lists are
// advertised and in what extension order.

// TLSFingerprintProfile configures TLS ClientHello parameters
// used to reproduce a client's handshake fingerprint.
//
// CipherSuites, Curves, PointFormats, SignatureAlgorithms, ALPNProtocols,
// SupportedVersions, KeyShareGroups, PSKModes, and Extensions use transport
// defaults when empty. A nonempty Extensions list determines which
// corresponding parameters are advertised.
type TLSFingerprintProfile struct {
	ID   int64  `json:"id"`   // stored profile database identity
	Name string `json:"name"` // administrator profile display name
	// Description is an optional editable label retained by persistence. Runtime
	// cloning allocates this pointed-to string so returned profiles can change the
	// optional value independently.
	Description *string `json:"description"` // optional profile description text
	// EnableGREASE adds GREASE bookend extensions when the dialer uses default
	// extension order. Explicit GREASE IDs in a custom Extensions list remain
	// effective independently of this flag.
	EnableGREASE bool     `json:"enable_grease"` // default-order GREASE insertion flag
	CipherSuites []uint16 `json:"cipher_suites"` // ordered cipher suite IDs
	Curves       []uint16 `json:"curves"`        // ordered supported group IDs
	// PointFormats stores sixteen-bit configuration values that the transport
	// encoder converts to eight-bit wire values. Profile validation does not
	// range-check this conversion.
	PointFormats        []uint16 `json:"point_formats"`        // ordered point format IDs
	SignatureAlgorithms []uint16 `json:"signature_algorithms"` // ordered signature scheme IDs
	ALPNProtocols       []string `json:"alpn_protocols"`       // ordered application protocol names
	SupportedVersions   []uint16 `json:"supported_versions"`   // ordered TLS version IDs
	KeyShareGroups      []uint16 `json:"key_share_groups"`     // ordered key-share group IDs
	// PSKModes stores sixteen-bit configuration values that the transport encoder
	// converts to eight-bit wire values. Profile validation does not range-check
	// this conversion.
	PSKModes   []uint16  `json:"psk_modes"`  // ordered PSK exchange modes
	Extensions []uint16  `json:"extensions"` // ordered TLS extension IDs
	CreatedAt  time.Time `json:"created_at"` // persisted profile creation time
	UpdatedAt  time.Time `json:"updated_at"` // persisted profile update time
}

// Validate rejects a profile whose Name is the empty string. It leaves
// whitespace and handshake parameter lists as supplied, so transport consumers
// decide effective defaults and whether configured values produce a usable
// handshake.
func (p *TLSFingerprintProfile) Validate() error {
	if p.Name == "" {
		return &ValidationError{Field: "name", Message: "name is required"}
	}
	return nil
}
