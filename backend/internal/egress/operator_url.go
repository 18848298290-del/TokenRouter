package egress

// Operator-supplied base URLs have different validation when the host allowlist
// is enabled. The enabled path requires HTTPS and a matching host, while the
// disabled path uses format validation with the configured permission for HTTP
// schemes.

// OperatorURLPolicy validates operator-supplied outbound URLs and their host allowlist.
type OperatorURLPolicy struct {
	// Enabled selects the validation path. AllowInsecureHTTP applies to the
	// disabled format check; AllowPrivateHosts and UpstreamHosts apply to the
	// enabled HTTPS allowlist check.
	Enabled           bool     // host allowlist enforcement flag
	AllowInsecureHTTP bool     // disabled-policy HTTP permission flag
	AllowPrivateHosts bool     // enabled-policy literal-address permission flag
	UpstreamHosts     []string // configured upstream host patterns
}

// Validate checks an operator URL with the selected allowlist policy. Enabled
// policies require HTTPS, a configured matching allowlist, and permitted
// literal addresses; disabled policies use format validation and
// AllowInsecureHTTP.
func (p OperatorURLPolicy) Validate(raw string) (string, error) {
	if !p.Enabled {
		return ValidateURLFormat(raw, p.AllowInsecureHTTP)
	}
	return ValidateHTTPSURL(raw, ValidationOptions{AllowedHosts: p.UpstreamHosts, RequireAllowlist: true, AllowPrivate: p.AllowPrivateHosts})
}
