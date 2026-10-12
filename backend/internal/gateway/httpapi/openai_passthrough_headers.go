package httpapi

import (
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
)

// WriteOpenAIPassthroughResponseHeaders applies filtering, copies quota headers, and replaces prior turn state.
func WriteOpenAIPassthroughResponseHeaders(dst http.Header, src http.Header, filter *egress.CompiledHeaderFilter) {
	if dst == nil || src == nil {
		return
	}
	if filter != nil {
		egressadapter.WriteFilteredHeaders(dst, src, filter)
	} else {
		if v := strings.TrimSpace(src.Get("Content-Type")); v != "" {
			dst.Set("Content-Type", v)
		}
	}
	// Match x-codex-* keys case-insensitively because manually built response headers
	// can retain lowercase keys rather than net/http's canonical spelling.
	getCaseInsensitiveValues := func(h http.Header, want string) []string {
		if h == nil {
			return nil
		}
		for k, vals := range h {
			if strings.EqualFold(k, want) {
				return vals
			}
		}
		return nil
	}

	for _, rawKey := range []string{
		"x-codex-primary-used-percent",
		"x-codex-primary-reset-after-seconds",
		"x-codex-primary-window-minutes",
		"x-codex-secondary-used-percent",
		"x-codex-secondary-reset-after-seconds",
		"x-codex-secondary-window-minutes",
		"x-codex-primary-over-secondary-limit-percent",
	} {
		vals := getCaseInsensitiveValues(src, rawKey)
		if len(vals) == 0 {
			continue
		}
		key := http.CanonicalHeaderKey(rawKey)
		dst.Del(key)
		for _, v := range vals {
			dst.Add(key, v)
		}
	}

	// Turn state bypasses the general allowlist. Clear the prior value even when the upstream omits it
	// so failover cannot leave another provider's turn state in the downstream headers.
	turnStateKey := http.CanonicalHeaderKey(CodexTurnStateHeader)
	dst.Del(turnStateKey)
	for _, value := range getCaseInsensitiveValues(src, CodexTurnStateHeader) {
		dst.Add(turnStateKey, value)
	}
}
