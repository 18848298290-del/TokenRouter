package tlsfingerprint

import (
	"net"
	"slices"
	"strings"

	utls "github.com/refraction-networking/utls" //nolint:revive // The package is named tls; the alias distinguishes it from crypto/tls.
)

// profile_alpn.go determines HTTP/2 profile support and constructs HTTP/1-only
// profile variants.
// HTTP/2 selection needs both an ALPN extension and an h2 protocol entry in
// the effective TLS profile. Empty protocol lists mean HTTP/1.1, and a custom
// extension order omitting ALPN prevents HTTP/2 selection even when a
// protocol list names h2.
//
// An HTTP/1-only variant replaces the protocol slice on a shallow copy when
// h2 is supported; other profile slices remain shared and unchanged profiles
// return their original pointer. Negotiated protocol lookup recognizes the
// uTLS connection-state interface specifically, matching the fingerprint
// transport's distinct connection-state type.

// SupportsHTTP2 reports whether the profile's effective ALPN extension
// includes trimmed, case-insensitive h2. Empty extension order selects the
// built-in ALPN presence, while nil or empty protocol lists select HTTP/1.1
// and return false.
func SupportsHTTP2(profile *Profile) bool {
	if !profileHasALPNExtension(profile) {
		return false
	}
	for _, protocol := range effectiveALPNProtocols(profile) {
		if strings.EqualFold(strings.TrimSpace(protocol), "h2") {
			return true
		}
	}
	return false
}

// HTTP1OnlyProfile returns profile unchanged when nil or unsupported for
// HTTP/2. Otherwise it shallow-copies the profile, replaces ALPN storage with
// nonempty non-h2 entries, and adds HTTP/1.1 if absent. Other mutable slices
// remain shared, so callers treat them as immutable across variants.
func HTTP1OnlyProfile(profile *Profile) *Profile {
	if profile == nil || !SupportsHTTP2(profile) {
		return profile
	}
	cloned := *profile
	protocols := make([]string, 0, len(effectiveALPNProtocols(profile)))
	hasHTTP1 := false
	// Keep non-h2 entries in their supplied spelling while removing HTTP/2
	// advertisement. Fresh ALPN storage leaves the input profile's advertised
	// list available to other callers.
	for _, protocol := range effectiveALPNProtocols(profile) {
		trimmed := strings.TrimSpace(protocol)
		if trimmed == "" || strings.EqualFold(trimmed, "h2") {
			continue
		}
		if strings.EqualFold(trimmed, "http/1.1") {
			hasHTTP1 = true
		}
		protocols = append(protocols, protocol)
	}
	if !hasHTTP1 {
		protocols = append(protocols, "http/1.1")
	}
	cloned.ALPNProtocols = protocols
	return &cloned
}

// NegotiatedProtocol returns trimmed ALPN text from a connection implementing
// the uTLS ConnectionState interface. Nil connections and other state
// signatures return empty text, allowing fingerprint HTTP/2 selection to
// reject an unrecognized negotiation result.
func NegotiatedProtocol(conn net.Conn) string {
	if conn == nil {
		return ""
	}
	// negotiatedProtocolConn identifies the state signature exposed by uTLS connections.
	type negotiatedProtocolConn interface {
		// ConnectionState returns uTLS negotiation metadata for this connected peer.
		ConnectionState() utls.ConnectionState
	}
	if stateConn, ok := conn.(negotiatedProtocolConn); ok {
		return strings.TrimSpace(stateConn.ConnectionState().NegotiatedProtocol)
	}
	return ""
}

// effectiveALPNProtocols returns the supplied nonempty protocol slice or a
// fresh HTTP/1.1 default. The supplied slice remains shared, so consumers
// read it while callers keep profile contents stable.
func effectiveALPNProtocols(profile *Profile) []string {
	if profile != nil && len(profile.ALPNProtocols) > 0 {
		return profile.ALPNProtocols
	}
	return []string{"http/1.1"}
}

// profileHasALPNExtension reports whether the effective extension order
// includes ALPN. Nil profiles or empty order select the built-in order
// containing ALPN; a custom order must contain extension ID sixteen.
func profileHasALPNExtension(profile *Profile) bool {
	if profile == nil || len(profile.Extensions) == 0 {
		return true
	}
	return slices.Contains(profile.Extensions, 16)
}
