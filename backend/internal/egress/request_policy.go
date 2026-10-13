package egress

// An outbound attempt carries its selected proxy and fingerprint together with
// destination and redirect checks. RequestPolicy copies a present profile and
// any accepted header overrides before transport construction, so callers can
// prepare request-specific values while retaining the input collections.

// EgressPolicy holds resolved transport identity and destination checks for one
// outbound attempt.
type EgressPolicy struct {
	ProxyURL string `json:"-"` // selected credential-bearing proxy URL
	// TLSProfile is cloned by RequestPolicy before publication to the caller.
	// Transport adapters separately convert this profile for dialers that can
	// retain their own copies.
	TLSProfile *TLSFingerprintProfile `json:"-"` // optional selected fingerprint profile
	// Headers is allocated and filtered by RequestPolicy before the request is
	// built. ApplyRequestHeaders consumes these names as case-insensitive
	// replacements for existing wire headers.
	Headers            map[string]string `json:"-"` // filtered lowercase header overrides
	TransportMode      string            // selected transport cache variant
	ValidateResolvedIP bool              // global resolved-address validation flag
	PublicHostsOnly    bool              // request public-address restriction flag
	DisableRedirects   bool              // request redirect rejection flag
}

// RequestPolicyInput supplies transport identity, header overrides, and
// validation flags for copying.
//
// TLSProfile and Headers are read during RequestPolicy and copied into the
// returned policy. The caller keeps their source collections stable while this
// synchronous conversion runs.
type RequestPolicyInput struct {
	ProxyURL string // caller-selected credential-bearing proxy URL
	// caller-supplied fingerprint profile pointer
	TLSProfile *TLSFingerprintProfile
	Headers    map[string]string // caller-supplied header override values
	// ValidateResolvedIP requests DNS-address validation; PublicHostsOnly
	// restricts resolved addresses; DisableRedirects rejects redirect following.
	ValidateResolvedIP, PublicHostsOnly, DisableRedirects bool
}

// RequestPolicy clones the supplied fingerprint and normalizes permitted header
// overrides into a request policy. Invalid, blocked, and empty header entries
// are omitted, while proxy and destination flags retain their supplied values.
func RequestPolicy(input RequestPolicyInput) EgressPolicy {
	return EgressPolicy{ProxyURL: input.ProxyURL, TLSProfile: CloneTLSFingerprintProfile(input.TLSProfile), Headers: ResolveHeaderOverrides(input.Headers), ValidateResolvedIP: input.ValidateResolvedIP, PublicHostsOnly: input.PublicHostsOnly, DisableRedirects: input.DisableRedirects}
}

// RequiresHostValidation reports whether global IP validation or per-request public-host checks apply.
func (p EgressPolicy) RequiresHostValidation() bool { return p.ValidateResolvedIP || p.PublicHostsOnly }

// String returns diagnostic text containing the transport mode, excluding credentials.
func (p EgressPolicy) String() string { return "egress policy (" + p.TransportMode + ")" }

// GoString uses the same credential-free diagnostic text for Go-syntax formatting.
func (p EgressPolicy) GoString() string { return p.String() }
