package egress

import (
	"strings"
)

// response_headers.go compiles response header permissions and applies removal
// and framing exclusions.
// Response forwarding uses default membership for content metadata, rate limits,
// redirects, and request identifiers. Enabled configuration can add names or
// request their removal. Forced removal wins over default and additional
// membership, and outgoing framing exclusions still reject content length,
// transfer encoding, and connection even when configured as allowed.
//
// Compilation allocates normalized maps that consumers treat as immutable for
// repeated checks. Disabled configuration ignores both custom lists and retains
// default membership. Allows accepts a nil compiled filter by using the shared
// default, lowercases the supplied header name, and checks removal before
// allowed membership and framing. Filtering adapters rebuild accepted value
// slices before forwarding them to avoid sharing source slice storage.

var (
	// defaultAllowed lists response headers passed through by default.
	// The HTTP stack controls the following headers on the outgoing response:
	//   - content-length depends on the bytes written by ResponseWriter.
	//   - transfer-encoding depends on the outgoing HTTP framing.
	//   - connection depends on the server's connection reuse policy.
	defaultAllowed = map[string]struct{}{
		"content-type":                   {},
		"content-encoding":               {},
		"content-language":               {},
		"cache-control":                  {},
		"etag":                           {},
		"last-modified":                  {},
		"expires":                        {},
		"vary":                           {},
		"date":                           {},
		"x-request-id":                   {},
		"x-ratelimit-limit-requests":     {},
		"x-ratelimit-limit-tokens":       {},
		"x-ratelimit-remaining-requests": {},
		"x-ratelimit-remaining-tokens":   {},
		"x-ratelimit-reset-requests":     {},
		"x-ratelimit-reset-tokens":       {},
		"retry-after":                    {},
		"location":                       {},
		"www-authenticate":               {},
		// Codex uses this response header to avoid estimating reasoning tokens a
		// second time when upstream usage already includes them.
		"x-reasoning-included": {},
	}

	// hopByHopHeaders excludes outgoing framing and connection values even when
	// an administrator adds those names to the response allowlist.
	hopByHopHeaders = map[string]struct{}{
		"content-length":    {},
		"transfer-encoding": {},
		"connection":        {},
	}

	// defaultCompiledHeaderFilter applies the standard pass-through policy when
	// a caller supplies no compiled filter for its response headers.
	defaultCompiledHeaderFilter = CompileHeaderFilter(ResponseHeaderOptions{})
)

// ResponseHeaderOptions configures additional allowed headers and forced removals.
type ResponseHeaderOptions struct {
	// Enabled determines whether AdditionalAllowed and ForceRemove participate in
	// compilation. Disabled options use the default allowlist, while enabled
	// options apply removals ahead of allowed-name checks.
	Enabled           bool     // configured header policy flag
	AdditionalAllowed []string // additional pass-through header names
	ForceRemove       []string // forced response header removals
}

// CompiledHeaderFilter stores normalized response header membership and removal
// rules for repeated checks.
type CompiledHeaderFilter struct {
	// allowed and forceRemove are built together by CompileHeaderFilter. Callers
	// treat both maps as immutable after compilation; forced removal overrides
	// every default or additional allowed name.
	allowed     map[string]struct{} // lowercase permitted header names
	forceRemove map[string]struct{} // lowercase overriding removal names
}

// CompileHeaderFilter allocates normalized membership and removal maps for
// repeated response-header checks. Default names are copied first, and enabled
// configuration adds trimmed lowercase names while ignoring blank entries.
// Disabled configuration ignores both custom lists.
//
// Forced-removal membership is kept separately so Allows can give it precedence
// over default and additional names. Framing exclusions are applied during
// lookup rather than deleted from the compiled membership map, so an
// administrator cannot admit them by adding a name. Callers treat both resulting
// maps as immutable.
func CompileHeaderFilter(cfg ResponseHeaderOptions) *CompiledHeaderFilter {
	allowed := make(map[string]struct{}, len(defaultAllowed)+len(cfg.AdditionalAllowed))
	for key := range defaultAllowed {
		allowed[key] = struct{}{}
	}

	if cfg.Enabled {
		for _, key := range cfg.AdditionalAllowed {
			normalized := strings.ToLower(strings.TrimSpace(key))
			if normalized == "" {
				continue
			}
			allowed[normalized] = struct{}{}
		}
	}

	forceRemove := map[string]struct{}{}
	if cfg.Enabled {
		forceRemove = make(map[string]struct{}, len(cfg.ForceRemove))
		for _, key := range cfg.ForceRemove {
			normalized := strings.ToLower(strings.TrimSpace(key))
			if normalized == "" {
				continue
			}
			forceRemove[normalized] = struct{}{}
		}
	}

	return &CompiledHeaderFilter{
		allowed:     allowed,
		forceRemove: forceRemove,
	}
}

// Allows reports whether name passes forced removal, allowed membership, and
// outgoing framing exclusions. A nil receiver uses the immutable shared default
// filter. Lookup lowercases the supplied name but retains surrounding
// whitespace, so callers pass actual header names.
//
// A forced removal rejects a name even if another list permits it. Allowed
// membership is then required, and the final framing check rejects
// server-controlled fields. Keeping this order lets configuration remove normal
// metadata while additional names cannot override response framing rules.
func (filter *CompiledHeaderFilter) Allows(name string) bool {
	if filter == nil {
		filter = defaultCompiledHeaderFilter
	}
	lower := strings.ToLower(name)
	if _, blocked := filter.forceRemove[lower]; blocked {
		return false
	}
	if _, ok := filter.allowed[lower]; !ok {
		return false
	}
	_, hop := hopByHopHeaders[lower]
	return !hop
}
