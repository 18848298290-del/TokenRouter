package egress

import (
	"slices"
)

// tls_clone.go copies TLS profiles and routers for cache publication and
// request selection.
// TLS profile slices and optional router values can be retained by caches and
// returned to request selection. Cloning allocates storage for those mutable
// values, including ordered router rules, while retaining the distinction
// between absent and present empty lists.

// CloneTLSFingerprintProfile copies a profile and allocates its optional
// description and every slice. Nil input returns nil, and nil slices remain
// nil; the resulting collections can be changed independently of the input.
func CloneTLSFingerprintProfile(p *TLSFingerprintProfile) *TLSFingerprintProfile {
	if p == nil {
		return nil
	}
	copied := *p
	copied.Description = clonePointer(p.Description)
	copied.CipherSuites = slices.Clone(p.CipherSuites)
	copied.Curves = slices.Clone(p.Curves)
	copied.PointFormats = slices.Clone(p.PointFormats)
	copied.SignatureAlgorithms = slices.Clone(p.SignatureAlgorithms)
	copied.ALPNProtocols = slices.Clone(p.ALPNProtocols)
	copied.SupportedVersions = slices.Clone(p.SupportedVersions)
	copied.KeyShareGroups = slices.Clone(p.KeyShareGroups)
	copied.PSKModes = slices.Clone(p.PSKModes)
	copied.Extensions = slices.Clone(p.Extensions)
	return &copied
}

// CloneTLSFingerprintRouter copies the description, optional token/reset
// profile IDs, and ordered rules into new storage. Nil input returns nil, and
// the Rules slice retains its nil or present empty form.
func CloneTLSFingerprintRouter(p *TLSFingerprintRouter) *TLSFingerprintRouter {
	if p == nil {
		return nil
	}
	copied := *p
	copied.Description = clonePointer(p.Description)
	copied.ChatGPTOAuthTokenTLSFingerprintProfileID = clonePointer(p.ChatGPTOAuthTokenTLSFingerprintProfileID)
	copied.CodexInviteResetTLSFingerprintProfileID = clonePointer(p.CodexInviteResetTLSFingerprintProfileID)
	copied.Rules = slices.Clone(p.Rules)
	return &copied
}

// clonePointer allocates a shallow copy of the pointed-to value. Nil input
// returns nil; reference-bearing members of T remain shared, while current
// callers use string and integer values.
func clonePointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	copied := *p
	return &copied
}

// CloneTLSProfiles allocates a profile slice and clones each element through
// CloneTLSFingerprintProfile. Nil input remains nil, an empty nonnil slice
// remains present, and nil profile entries retain their positions.
func CloneTLSProfiles(values []*TLSFingerprintProfile) []*TLSFingerprintProfile {
	if values == nil {
		return nil
	}
	out := make([]*TLSFingerprintProfile, len(values))
	for i, p := range values {
		out[i] = CloneTLSFingerprintProfile(p)
	}
	return out
}

// CloneTLSRouters allocates a router slice and clones each element through
// CloneTLSFingerprintRouter. Nil input remains nil, an empty nonnil slice
// remains present, and nil router entries retain their positions.
func CloneTLSRouters(values []*TLSFingerprintRouter) []*TLSFingerprintRouter {
	if values == nil {
		return nil
	}
	out := make([]*TLSFingerprintRouter, len(values))
	for i, p := range values {
		out[i] = CloneTLSFingerprintRouter(p)
	}
	return out
}
