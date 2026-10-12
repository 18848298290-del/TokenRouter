package egress

import (
	"errors"
	"net/url"
	"strings"
)

// UsageURLPolicy validates usage endpoints, defaulting to HTTPS and public hosts when unconfigured.
type UsageURLPolicy struct {
	Configured        bool
	Enabled           bool
	AllowInsecureHTTP bool
	AllowPrivateHosts bool
	UpstreamHosts     []string
}

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
	if len(raw) > 2048 {
		return errors.New("base URL is too long")
	}
	return nil
}
