package egress

import (
	"slices"
)

// CloneTLSFingerprintProfile copies mutable fields to isolate configuration, caches, and requests.
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

// CloneTLSFingerprintRouter copies optional values and rules, preserving nil versus empty slices.
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

func clonePointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	copied := *p
	return &copied
}

// CloneTLSProfiles deep-copies profiles while preserving nil versus empty slices.
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

// CloneTLSRouters deep-copies routers while preserving nil versus empty slices.
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
