package httpapi

import (
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
)

// Passthrough response headers include Codex quota and opaque turn state even
// when a general filter omits them. Header lookup for those fields accepts
// manually constructed casing, and turn state is replaced or cleared on every
// response so a provider switch cannot retain an earlier value.
//
// Ordinary filtering runs first, with a content-type fallback when no filter
// is installed. Quota values replace existing canonical keys only when
// upstream values are present. Turn state follows a stronger rule: the prior
// canonical key is removed even when this upstream omits it. Mandatory quota
// and turn-state values use Add to build destination slices separate from the
// source slice containers.

// WriteOpenAIPassthroughResponseHeaders relays filtered response metadata plus
// mandatory Codex quota and turn-state fields. Nil source or destination
// returns immediately. A missing filter retains a nonblank content type, while
// quota lookup handles manually constructed case spellings.
//
// Present quota values replace their canonical destination key; absent quota
// values leave any destination value intact. Turn state clears its canonical
// key even when omitted upstream, preventing another provider's prior state
// from surviving that response. Callers invoke this before commitment when the
// client needs to receive these header changes. Supply distinct maps: deleting
// the prior turn-state key precedes reading its upstream values.
func WriteOpenAIPassthroughResponseHeaders(dst http.Header, src http.Header, filter *egress.CompiledHeaderFilter) {
	if dst == nil || src == nil {
		return
	}
	if filter != nil {
		egressadapter.WriteFilteredHeaders(dst, src, filter)
	} else {
		// With no general filter, retain a declared content type so client decoding
		// follows the upstream body format while mandatory headers are handled below.
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
