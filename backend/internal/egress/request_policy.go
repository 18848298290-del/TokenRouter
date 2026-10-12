package egress

// EgressPolicy contains the proxy, TLS profile, headers, and destination checks for one request.
type EgressPolicy struct {
	ProxyURL           string                 `json:"-"`
	TLSProfile         *TLSFingerprintProfile `json:"-"`
	Headers            map[string]string      `json:"-"`
	TransportMode      string
	ValidateResolvedIP bool
	PublicHostsOnly    bool
	DisableRedirects   bool
}

// RequestPolicyInput contains outbound settings resolved by the caller.
// Apply headers while building the request; choose TLS and redirects before acquiring a client.
type RequestPolicyInput struct {
	ProxyURL                                              string
	TLSProfile                                            *TLSFingerprintProfile
	Headers                                               map[string]string
	ValidateResolvedIP, PublicHostsOnly, DisableRedirects bool
}

// RequestPolicy copies TLS settings and filters header overrides through safety rules.
func RequestPolicy(input RequestPolicyInput) EgressPolicy {
	return EgressPolicy{ProxyURL: input.ProxyURL, TLSProfile: CloneTLSFingerprintProfile(input.TLSProfile), Headers: ResolveHeaderOverrides(input.Headers), ValidateResolvedIP: input.ValidateResolvedIP, PublicHostsOnly: input.PublicHostsOnly, DisableRedirects: input.DisableRedirects}
}

// RequiresHostValidation reports whether global IP validation or per-request public-host checks apply.
func (p EgressPolicy) RequiresHostValidation() bool { return p.ValidateResolvedIP || p.PublicHostsOnly }

// String returns diagnostic text containing the transport mode, excluding credentials.
func (p EgressPolicy) String() string { return "egress policy (" + p.TransportMode + ")" }

// GoString uses the same credential-free diagnostic text for Go-syntax formatting.
func (p EgressPolicy) GoString() string { return p.String() }
