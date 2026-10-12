package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
)

// TLS selection returns egress configuration values, while network dialers
// consume tlsfingerprint profiles. Conversion copies every ordered list at this
// handoff, giving the returned transport value separate mutable storage before
// a dialer retains it for connection construction.

// TLSProfiles converts egress TLS policy results into transport fingerprints.
type TLSProfiles struct {
	// TLSFingerprintProfileService is retained by the wrapper and shared with its
	// other application consumers. The application starts and stops the underlying
	// cache subscription.
	*egress.TLSFingerprintProfileService // shared profile selection service
}

// NewTLSProfiles wraps the supplied core service for transport fingerprint
// selection. The wrapper retains that service and delegates its lifecycle and
// cache ownership to the application that constructed it.
func NewTLSProfiles(core *egress.TLSFingerprintProfileService) *TLSProfiles {
	return &TLSProfiles{TLSFingerprintProfileService: core}
}

// core returns the wrapped TLS profile selection service pointer. A nil wrapper
// returns nil, allowing delegated resolver methods to apply their own handling
// for an absent core.
func (p *TLSProfiles) core() *egress.TLSFingerprintProfileService {
	if p == nil {
		return nil
	}
	return p.TLSFingerprintProfileService
}

// ResolveRequestTLS resolves the supplied provider eligibility and router
// selection, then copies the selected fingerprint into transport settings.
// Disabled eligibility returns nil, and conversion gives each returned profile
// separate list storage.
func (p *TLSProfiles) ResolveRequestTLS(selection egress.TLSSelection) *tlsfingerprint.Profile {
	return ToTLSProfile(p.core().ResolveRequestPolicy(selection).TLSProfile)
}

// ResolveTokenTLSProfileByID resolves token-request TLS settings and converts
// the result into copied transport lists. The boolean reports selection
// availability, including a built-in default for zero and failure for an
// unavailable positive or random selection.
func (p *TLSProfiles) ResolveTokenTLSProfileByID(id int64) (*tlsfingerprint.Profile, bool) {
	profile, ok := p.core().ResolveTokenTLSProfileByID(id)
	return ToTLSProfile(profile), ok
}

// ToTLSProfile allocates transport settings with separately cloned ordered lists
// from the egress profile. Nil input returns nil, and empty lists remain empty
// for the dialer's effective defaults. This separates returned transport storage
// from source policy values.
//
// Cached transports can retain the returned profile across several connections.
// Conversion does not create separate storage for each later handshake;
// buildClientHelloSpecFromProfile can borrow SupportedVersions, and pinned uTLS
// rewrites its GREASE entries in place. Dialer consumers therefore handle that
// later sharing separately from this policy-to-transport conversion.
func ToTLSProfile(p *egress.TLSFingerprintProfile) *tlsfingerprint.Profile {
	if p == nil {
		return nil
	}
	p = egress.CloneTLSFingerprintProfile(p)
	return &tlsfingerprint.Profile{
		Name:                p.Name,
		EnableGREASE:        p.EnableGREASE,
		CipherSuites:        p.CipherSuites,
		Curves:              p.Curves,
		PointFormats:        p.PointFormats,
		SignatureAlgorithms: p.SignatureAlgorithms,
		ALPNProtocols:       p.ALPNProtocols,
		SupportedVersions:   p.SupportedVersions,
		KeyShareGroups:      p.KeyShareGroups,
		PSKModes:            p.PSKModes,
		Extensions:          p.Extensions,
	}
}

// FromTLSProfile allocates an egress profile with separately cloned ordered
// lists from transport settings. Nil input returns nil, and the converter
// retains each list's nil or present empty form for policy and cache identity.
func FromTLSProfile(p *tlsfingerprint.Profile) *egress.TLSFingerprintProfile {
	if p == nil {
		return nil
	}
	return egress.CloneTLSFingerprintProfile(&egress.TLSFingerprintProfile{Name: p.Name, EnableGREASE: p.EnableGREASE, CipherSuites: p.CipherSuites, Curves: p.Curves, PointFormats: p.PointFormats, SignatureAlgorithms: p.SignatureAlgorithms, ALPNProtocols: p.ALPNProtocols, SupportedVersions: p.SupportedVersions, KeyShareGroups: p.KeyShareGroups, PSKModes: p.PSKModes, Extensions: p.Extensions})
}
