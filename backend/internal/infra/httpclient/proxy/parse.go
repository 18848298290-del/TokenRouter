package proxy

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// allowedSchemes lists supported proxy protocols.
var allowedSchemes = map[string]bool{
	"http":    true,
	"https":   true,
	"socks5":  true,
	"socks5h": true,
}

// Parse accepts HTTP, HTTPS, SOCKS5, and SOCKS5H proxy URLs.
// Blank input returns a nil URL for direct access; malformed URLs, missing hosts, and unsupported schemes fail.
// SOCKS5 is normalized to SOCKS5H; the returned string is trimmed and includes that normalization.
func Parse(raw string) (trimmed string, parsed *url.URL, err error) {
	trimmed = strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil, nil
	}

	parsed, err = url.Parse(trimmed)
	if err != nil {
		// Include the URL parser's error in the returned diagnostic.
		return "", nil, fmt.Errorf("invalid proxy URL: %v", err)
	}

	if parsed.Host == "" || parsed.Hostname() == "" {
		return "", nil, fmt.Errorf("proxy URL missing host: %s", parsed.Redacted())
	}

	scheme := strings.ToLower(parsed.Scheme)
	if !allowedSchemes[scheme] {
		return "", nil, fmt.Errorf("unsupported proxy scheme %q (allowed: http, https, socks5, socks5h)", scheme)
	}

	// Normalize the SOCKS5 alias to SOCKS5H.
	if scheme == "socks5" {
		parsed.Scheme = "socks5h"
		trimmed = parsed.String()
	}

	return trimmed, parsed, nil
}

// NormalizePoolKey returns a normalized proxy pool key and parsed URL, or "direct" for blank input.
func NormalizePoolKey(raw string) (string, *url.URL, error) {
	_, parsed, err := Parse(raw)
	if err != nil {
		return "", nil, err
	}
	if parsed == nil {
		return "direct", nil, nil
	}
	// Pool identity uses lowercase scheme and host, excluding paths, queries, and fragments.
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.ForceQuery = false
	if hostname := parsed.Hostname(); hostname != "" {
		port := parsed.Port()
		if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
			port = ""
		}
		hostname = strings.ToLower(hostname)
		if port != "" {
			parsed.Host = net.JoinHostPort(hostname, port)
		} else {
			parsed.Host = hostname
		}
	}
	return parsed.String(), parsed, nil
}
