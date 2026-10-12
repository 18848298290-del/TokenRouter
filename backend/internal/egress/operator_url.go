package egress

// OperatorURLPolicy validates operator-supplied outbound URLs and their host allowlist.
type OperatorURLPolicy struct {
	Enabled           bool
	AllowInsecureHTTP bool
	AllowPrivateHosts bool
	UpstreamHosts     []string
}

func (p OperatorURLPolicy) Validate(raw string) (string, error) {
	if !p.Enabled {
		return ValidateURLFormat(raw, p.AllowInsecureHTTP)
	}
	return ValidateHTTPSURL(raw, ValidationOptions{AllowedHosts: p.UpstreamHosts, RequireAllowlist: true, AllowPrivate: p.AllowPrivateHosts})
}
