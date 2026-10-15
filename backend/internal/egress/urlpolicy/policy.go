package urlpolicy

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ipmatch"
)

// policy.go validates outbound URL syntax and host permissions before callers
// append endpoint paths and send requests. Scheme permission, optional port
// range, normalized host patterns, and literal private-host permission
// determine whether a parsed URL can be used. Hostname resolution belongs to
// the transport layer, so accepting ordinary DNS names here does not classify
// their resolved addresses.
//
// AllowedHosts constrains the URL when normalization retains entries;
// RequireAllowlist also rejects an empty normalized list. Wildcard patterns
// admit their suffix host as well as subdomains. Successful full validation
// rebuilds the parsed URL after trimming decoded trailing path separators and
// clearing RawPath, which can change escaped path spelling. Format-only
// validation instead trims the supplied text and leaves host policy to its
// caller.

// ValidationOptions selects allowlist requirements and literal private-host
// handling for outbound URL checks.
type ValidationOptions struct {
	// AllowedHosts constrains hosts whenever normalization leaves at least one
	// entry. RequireAllowlist additionally rejects an empty configured list, while
	// AllowPrivate independently controls localhost names and literal IPs.
	AllowedHosts     []string // configured host allowlist patterns
	RequireAllowlist bool     // reject empty normalized allowlists
	AllowPrivate     bool     // permit nonpublic literal hosts
}

// ValidateHTTPURL validates and normalizes an outbound HTTP or HTTPS URL using
// optional host patterns and literal-address policy. HTTP requires
// allowInsecureHTTP, a supplied port needs the range 1 through 65535, and
// RequireAllowlist rejects an empty normalized host list.
//
// Allowlist wildcards match both their suffix host and subdomains. The result
// is rebuilt from the parsed URL after trimming decoded path slashes and
// clearing RawPath, so encoded path spelling may change. DNS results require
// separate validation when a request is sent.
func ValidateHTTPURL(raw string, allowInsecureHTTP bool, opts ValidationOptions) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("url is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid url: %s", trimmed)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && (!allowInsecureHTTP || scheme != "http") {
		return "", fmt.Errorf("invalid url scheme: %s", parsed.Scheme)
	}

	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return "", errors.New("invalid host")
	}
	if !opts.AllowPrivate && blockedHost(host) {
		return "", fmt.Errorf("host is not allowed: %s", host)
	}

	if port := parsed.Port(); port != "" {
		num, err := strconv.Atoi(port)
		if err != nil || num <= 0 || num > 65535 {
			return "", fmt.Errorf("invalid port: %s", port)
		}
	}

	allowlist := NormalizeAllowlist(opts.AllowedHosts)
	if opts.RequireAllowlist && len(allowlist) == 0 {
		return "", errors.New("allowlist is not configured")
	}
	if len(allowlist) > 0 && !IsAllowedHost(host, allowlist) {
		return "", fmt.Errorf("host is not allowed: %s", host)
	}

	// Rebuild from the decoded Path after trimming its trailing separators.
	// Clearing RawPath lets url.URL choose escaping for the normalized path value.
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

// ValidateURLFormat checks a trimmed URL for HTTP/HTTPS scheme, hostname, and
// any supplied port range. HTTP needs allowInsecureHTTP; successful output
// trims trailing slash characters from the input text, leaving allowlist and
// private-address decisions to policy callers.
func ValidateURLFormat(raw string, allowInsecureHTTP bool) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("url is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid url: %s", trimmed)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && (!allowInsecureHTTP || scheme != "http") {
		return "", fmt.Errorf("invalid url scheme: %s", parsed.Scheme)
	}

	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return "", errors.New("invalid host")
	}

	if port := parsed.Port(); port != "" {
		num, err := strconv.Atoi(port)
		if err != nil || num <= 0 || num > 65535 {
			return "", fmt.Errorf("invalid port: %s", port)
		}
	}

	return strings.TrimRight(trimmed, "/"), nil
}

// ValidateHTTPSURL applies outbound URL validation with HTTP disabled and the
// supplied host policy. It returns normalized HTTPS text on success, and
// callers perform any required checks of resolved DNS addresses separately.
func ValidateHTTPSURL(raw string, opts ValidationOptions) (string, error) {
	return ValidateHTTPURL(raw, false, opts)
}

// NormalizeAllowlist trims and lowercases host entries, drops blank values, and
// removes ports accepted by SplitHostPort. Nil or empty input returns nil;
// wildcard text remains available for IsAllowedHost, and entries are not
// DNS-resolved or deduplicated.
func NormalizeAllowlist(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(values))
	for _, v := range values {
		entry := strings.ToLower(strings.TrimSpace(v))
		if entry == "" {
			continue
		}
		// URL validation compares parsed hostnames, so remove recognized authority
		// ports from configured patterns while leaving other supplied text intact.
		if host, _, err := net.SplitHostPort(entry); err == nil {
			entry = host
		}
		normalized = append(normalized, entry)
	}
	return normalized
}

// IsAllowedHost reports whether host exactly matches an allowlist entry or its
// wildcard suffix. Wildcards also admit the suffix itself; callers normalize
// host and entries before lookup, since this comparison preserves case and
// whitespace.
func IsAllowedHost(host string, allowlist []string) bool {
	for _, entry := range allowlist {
		if entry == "" {
			continue
		}
		// The configured wildcard covers the suffix host as well as descendants.
		// Include the apex comparison so a shared domain pattern admits either spelling.
		if after, ok := strings.CutPrefix(entry, "*."); ok {
			suffix := after
			if host == suffix || strings.HasSuffix(host, "."+suffix) {
				return true
			}
			continue
		}
		if host == entry {
			return true
		}
	}
	return false
}

// IsBlockedHost reports whether a trimmed, lowercased hostname is localhost, a
// localhost subdomain, or a nonpublic IP literal. Other names return false
// here; callers separately validate resolved DNS addresses when their policy
// requires it.
func IsBlockedHost(host string) bool {
	return blockedHost(strings.ToLower(strings.TrimSpace(host)))
}

// blockedHost checks a normalized hostname for local names and nonpublic IP
// literals. Ordinary DNS names return false without resolution, allowing
// transport callers to apply their separate resolved-address policy later.
func blockedHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		if ipmatch.IsNonPublic(ip) {
			return true
		}
	}
	return false
}
