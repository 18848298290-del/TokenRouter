package authconfig

import (
	"fmt"
	"net/url"
	"strings"
)

// OAuth endpoints need an absolute HTTP or HTTPS address, while frontend callback
// routes may also be paths on the current origin. These checks validate the saved
// URL shape; endpoint fetching and redirect execution belong to their callers.

// ValidateAbsoluteHTTPURL checks a trimmed endpoint for an HTTP(S) scheme, host,
// and absent fragment. The caller retains its input and decides whether credentials,
// ports, paths, or endpoint reachability are acceptable.
func ValidateAbsoluteHTTPURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("empty url")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if !u.IsAbs() {
		return fmt.Errorf("must be absolute")
	}
	if !isHTTPScheme(u.Scheme) {
		return fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}
	if strings.TrimSpace(u.Host) == "" {
		return fmt.Errorf("missing host")
	}
	if u.Fragment != "" {
		return fmt.Errorf("must not include fragment")
	}
	return nil
}

// ValidateFrontendRedirectURL accepts a trimmed single-slash path or an absolute
// HTTP(S) URL with a host and no fragment. It rejects remaining line breaks and
// protocol-relative prefixes; single-slash paths are accepted before URL parsing.
func ValidateFrontendRedirectURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("empty url")
	}
	if strings.ContainsAny(raw, "\r\n") {
		return fmt.Errorf("contains invalid characters")
	}
	if strings.HasPrefix(raw, "/") {
		if strings.HasPrefix(raw, "//") {
			return fmt.Errorf("must not start with //")
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if !u.IsAbs() {
		return fmt.Errorf("must be absolute http(s) url or relative path")
	}
	if !isHTTPScheme(u.Scheme) {
		return fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}
	if strings.TrimSpace(u.Host) == "" {
		return fmt.Errorf("missing host")
	}
	if u.Fragment != "" {
		return fmt.Errorf("must not include fragment")
	}
	return nil
}

// isHTTPScheme reports whether the scheme is HTTP or HTTPS, ignoring case.
func isHTTPScheme(scheme string) bool {
	return strings.EqualFold(scheme, "http") || strings.EqualFold(scheme, "https")
}
