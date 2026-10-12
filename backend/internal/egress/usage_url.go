package egress

import (
	"errors"
	"net/url"
	"strings"
)

// Usage endpoints first reject embedded credentials and nonempty query or
// fragment text before selecting host policy. That preliminary format check does
// not reject an empty query or fragment delimiter, and it leaves supplied port
// ranges to the selected validator. The resulting policy depends on whether
// configuration exists as well as whether its allowlist is enabled.
//
// An unconfigured service requires HTTPS and public literal hosts. Configured
// disabled policies instead use format validation with their HTTP permission,
// while enabled policies require HTTPS and a matching upstream allowlist. These
// checks classify URL syntax, host patterns, and literal addresses; request
// execution separately checks resolved DNS addresses when needed.

// UsageURLPolicy validates usage endpoints, defaulting to HTTPS and public hosts when unconfigured.
type UsageURLPolicy struct {
	// Configured distinguishes strict defaults from supplied policy. Enabled
	// chooses between format-only validation and HTTPS allowlist enforcement,
	// which consumes AllowPrivateHosts and UpstreamHosts; AllowInsecureHTTP
	// affects the disabled path.
	Configured        bool     // configuration availability selection flag
	Enabled           bool     // configured allowlist enforcement flag
	AllowInsecureHTTP bool     // disabled-policy HTTP permission flag
	AllowPrivateHosts bool     // enabled-policy literal-address permission flag
	UpstreamHosts     []string // configured usage host patterns
}

// Validate rejects credentials and nonempty query or fragment text before
// selecting usage host policy. Unconfigured policies require HTTPS and public
// literal addresses. Configured disabled policies use format-only validation
// with AllowInsecureHTTP, while enabled policies require HTTPS, a matching
// allowlist, and their configured private-host permission.
//
// Configured distinguishes absent settings from a disabled allowlist, so those
// cases can accept different schemes and literal hosts. The successful validator
// returns normalized URL text; callers apply any required DNS-result checks when
// sending requests.
func (p UsageURLPolicy) Validate(raw string) (string, error) {
	if err := ValidateUsageBaseURLFormat(raw); err != nil {
		return "", err
	}
	if !p.Configured {
		return ValidateHTTPSURL(raw, ValidationOptions{AllowPrivate: false})
	}
	if !p.Enabled {
		return ValidateURLFormat(raw, p.AllowInsecureHTTP)
	}
	return ValidateHTTPSURL(raw, ValidationOptions{AllowedHosts: p.UpstreamHosts, RequireAllowlist: true, AllowPrivate: p.AllowPrivateHosts})
}

// ValidateUsageBaseURLFormat checks HTTP/HTTPS base URL structure and rejects
// credentials and nonempty query or fragment text. Empty query or fragment
// delimiters can pass this check. Raw input has a 2048-byte limit; the selected
// URL validator checks host allowlisting, private-address rules, and supplied
// port ranges.
func ValidateUsageBaseURLFormat(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("invalid base URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("base URL must not contain credentials, query, or fragment")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return errors.New("base URL scheme is not supported")
	}
	// Bound the stored base-URL text before appending usage endpoint paths.
	if len(raw) > 2048 {
		return errors.New("base URL is too long")
	}
	return nil
}
