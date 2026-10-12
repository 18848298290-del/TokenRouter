package egress

import (
	"strings"
)

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

	// hopByHopHeaders blocks framing and connection headers managed by the HTTP stack.
	hopByHopHeaders = map[string]struct{}{
		"content-length":    {},
		"transfer-encoding": {},
		"connection":        {},
	}

	defaultCompiledHeaderFilter = CompileHeaderFilter(ResponseHeaderOptions{})
)

// ResponseHeaderOptions configures additional allowed headers and forced removals.
type ResponseHeaderOptions struct {
	Enabled           bool
	AdditionalAllowed []string
	ForceRemove       []string
}

type CompiledHeaderFilter struct {
	allowed     map[string]struct{}
	forceRemove map[string]struct{}
}

func CompileHeaderFilter(cfg ResponseHeaderOptions) *CompiledHeaderFilter {
	allowed := make(map[string]struct{}, len(defaultAllowed)+len(cfg.AdditionalAllowed))
	for key := range defaultAllowed {
		allowed[key] = struct{}{}
	}
	// Disabled configuration uses defaults and ignores additional and force-remove entries.
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

// Allows applies forced removal, the allowlist, and transport-header exclusions in that order.
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
