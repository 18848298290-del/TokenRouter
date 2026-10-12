package authconfig

import (
	"fmt"
	"net/url"
	"strings"
)

// ValidateAbsoluteHTTPURL requires an absolute HTTP(S) URL with a host and no fragment.
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

// ValidateFrontendRedirectURL accepts a single-slash relative path or an absolute HTTP(S) URL and rejects line breaks.
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

func isHTTPScheme(scheme string) bool {
	return strings.EqualFold(scheme, "http") || strings.EqualFold(scheme, "https")
}
