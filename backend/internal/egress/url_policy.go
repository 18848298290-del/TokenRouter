package egress

import "github.com/TokenFlux/TokenRouter/internal/egress/urlpolicy"

// URL validation is shared with the urlpolicy package so callers in egress use
// the same scheme, host, and port rules. These forwarding functions return its
// normalized text or error; callers arrange DNS-address checks when
// establishing requests.

// ValidationOptions names the shared allowlist and literal-address validation
// settings from urlpolicy.
type ValidationOptions = urlpolicy.ValidationOptions

// ValidateHTTPURL validates a URL using shared scheme, allowlist, and
// literal-address rules. HTTP requires allowInsecureHTTP; the returned text
// normalizes trailing path slashes, and DNS-address validation remains with the
// request executor.
func ValidateHTTPURL(raw string, allowInsecureHTTP bool, opts ValidationOptions) (string, error) {
	return urlpolicy.ValidateHTTPURL(raw, allowInsecureHTTP, opts)
}

// ValidateURLFormat validates URL syntax and the permitted HTTP or HTTPS
// scheme. It trims surrounding whitespace and trailing slash characters; this
// format check leaves host allowlisting and resolved-address policy to callers.
func ValidateURLFormat(raw string, allowInsecureHTTP bool) (string, error) {
	return urlpolicy.ValidateURLFormat(raw, allowInsecureHTTP)
}

// ValidateHTTPSURL applies the shared HTTPS-only URL validation using opts. A
// successful result has normalized trailing path slashes; callers validate DNS
// results separately when their request policy requires public addresses.
func ValidateHTTPSURL(raw string, opts ValidationOptions) (string, error) {
	return urlpolicy.ValidateHTTPSURL(raw, opts)
}
