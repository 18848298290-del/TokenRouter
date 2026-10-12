package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
)

// TLSProfiles converts egress TLS policy results into transport fingerprints.
type TLSProfiles struct {
	*egress.TLSFingerprintProfileService
}

func NewTLSProfiles(core *egress.TLSFingerprintProfileService) *TLSProfiles {
	return &TLSProfiles{TLSFingerprintProfileService: core}
}

func (p *TLSProfiles) core() *egress.TLSFingerprintProfileService {
	if p == nil {
		return nil
	}
	return p.TLSFingerprintProfileService
}

// ResolveRequestTLS resolves provider eligibility and router selection into a transport fingerprint.
func (p *TLSProfiles) ResolveRequestTLS(selection egress.TLSSelection) *tlsfingerprint.Profile {
	return ToTLSProfile(p.core().ResolveRequestPolicy(selection).TLSProfile)
}

func (p *TLSProfiles) ResolveTokenTLSProfileByID(id int64) (*tlsfingerprint.Profile, bool) {
	profile, ok := p.core().ResolveTokenTLSProfileByID(id)
	return ToTLSProfile(profile), ok
}

// ToTLSProfile copies an egress TLS profile into transport fingerprint settings.
// The dialer uses built-in defaults for empty slices.
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

// FromTLSProfile copies transport fingerprint fields into an egress TLS profile.
func FromTLSProfile(p *tlsfingerprint.Profile) *egress.TLSFingerprintProfile {
	if p == nil {
		return nil
	}
	return egress.CloneTLSFingerprintProfile(&egress.TLSFingerprintProfile{Name: p.Name, EnableGREASE: p.EnableGREASE, CipherSuites: p.CipherSuites, Curves: p.Curves, PointFormats: p.PointFormats, SignatureAlgorithms: p.SignatureAlgorithms, ALPNProtocols: p.ALPNProtocols, SupportedVersions: p.SupportedVersions, KeyShareGroups: p.KeyShareGroups, PSKModes: p.PSKModes, Extensions: p.Extensions})
}
