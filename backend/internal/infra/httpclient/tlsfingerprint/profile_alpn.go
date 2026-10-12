package tlsfingerprint

import (
	"net"
	"slices"
	"strings"

	utls "github.com/refraction-networking/utls" //nolint:revive // The package is named tls; the alias distinguishes it from crypto/tls.
)

// SupportsHTTP2 reports whether the profile includes an ALPN extension advertising h2.
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

// HTTP1OnlyProfile removes h2 and adds http/1.1 in a shallow profile copy.
// It returns the original pointer when h2 is not supported; other slices remain shared.
func HTTP1OnlyProfile(profile *Profile) *Profile {
	if profile == nil || !SupportsHTTP2(profile) {
		return profile
	}
	cloned := *profile
	protocols := make([]string, 0, len(effectiveALPNProtocols(profile)))
	hasHTTP1 := false
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

// NegotiatedProtocol returns the ALPN protocol from a uTLS connection, or an empty string.
func NegotiatedProtocol(conn net.Conn) string {
	if conn == nil {
		return ""
	}
	type negotiatedProtocolConn interface {
		ConnectionState() utls.ConnectionState
	}
	if stateConn, ok := conn.(negotiatedProtocolConn); ok {
		return strings.TrimSpace(stateConn.ConnectionState().NegotiatedProtocol)
	}
	return ""
}

func effectiveALPNProtocols(profile *Profile) []string {
	if profile != nil && len(profile.ALPNProtocols) > 0 {
		return profile.ALPNProtocols
	}
	return []string{"http/1.1"}
}

func profileHasALPNExtension(profile *Profile) bool {
	if profile == nil || len(profile.Extensions) == 0 {
		return true
	}
	return slices.Contains(profile.Extensions, 16)
}
