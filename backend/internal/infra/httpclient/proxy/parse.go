package proxy

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Proxy input accepts four supported schemes and treats blank text as direct
// access. Parsing retains credentials needed for routing and normalizes the
// SOCKS5 alias to SOCKS5H; pool-key normalization then removes path, query,
// and fragment components that do not select the proxy connection.
//
// The resulting key keeps scheme, host, port, and userinfo identity so
// transports can distinguish credential or endpoint changes. HTTP and HTTPS
// default ports are omitted, while the supplied port remains for SOCKS
// schemes. The returned URL is the normalized routing value itself, and
// callers treat its userinfo as secret even though password-redacted
// diagnostics are used for a missing host.

// allowedSchemes admits the proxy URL schemes supported by shared transport
// construction before a parsed URL reaches its scheme-specific routing setup.
var allowedSchemes = map[string]bool{
	"http":    true,
	"https":   true,
	"socks5":  true,
	"socks5h": true,
}

// Parse trims raw and accepts HTTP, HTTPS, SOCKS5, or SOCKS5H URLs with a
// host. Blank text returns a nil URL for direct access; syntax, missing-host,
// and unsupported-scheme failures return errors. SOCKS5 becomes SOCKS5H in
// the returned text and URL, while credentials remain available to dialers.
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

	// Both aliases use proxy-side hostname handling in the chosen dialer. Normalize
	// the scheme before consumers construct routing and their credential-bearing pool identity.
	if scheme == "socks5" {
		parsed.Scheme = "socks5h"
		trimmed = parsed.String()
	}

	return trimmed, parsed, nil
}

// NormalizePoolKey returns a normalized proxy identity and its corresponding
// routing URL, or direct with a nil URL for blank input. It parses first,
// lowercases scheme and host, removes path/query/fragment, and drops
// HTTP/HTTPS default ports.
//
// Userinfo stays part of both key and URL so changed credentials separate
// cache identity. The host rebuild uses net.JoinHostPort when a port is
// retained; IPv6 proxies need a port that survives HTTP/HTTPS default-port
// removal to retain their bracketed authority. Without a retained port, IPv6
// becomes a bare hostname in this
// implementation. Callers retain the mutable URL for construction and keep
// credential-bearing keys out of logs.
func NormalizePoolKey(raw string) (string, *url.URL, error) {
	_, parsed, err := Parse(raw)
	if err != nil {
		return "", nil, err
	}
	if parsed == nil {
		return "direct", nil, nil
	}
	// Proxy routing uses authority and userinfo. Remove unrelated URL components
	// so path or query spelling cannot split otherwise equivalent proxy pool entries.
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.ForceQuery = false
	if hostname := parsed.Hostname(); hostname != "" {
		port := parsed.Port()
		// Default HTTP and HTTPS ports identify the same endpoint as omission. Drop
		// them before rebuilding authority so those spellings share one pool identity.
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
