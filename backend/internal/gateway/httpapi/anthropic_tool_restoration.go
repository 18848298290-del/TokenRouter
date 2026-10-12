package httpapi

import (
	claude "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// Anthropic request preparation can substitute tool names that response output
// must restore for the client. Output adapters read the request's context mapping
// and apply the Anthropic byte rewriter, including its shared static substitutions.

// toolNameRewriteKey identifies the request mapping that response adapters read
// from Gin context to restore client tool names after upstream preparation.
const toolNameRewriteKey = "claude_tool_name_rewrite"

// staticToolNameRewrites shares Anthropic's static substitutions and determines
// whether restoration is needed when a request has no context-specific mapping.
var staticToolNameRewrites = claude.StaticToolNameRewrites

// toolNameRewriteFromContext reads the tool-name mapping saved during request preparation.
// A nil interface, missing key, or value with another dynamic type yields nil,
// allowing response adapters to use the static substitutions alone.
func toolNameRewriteFromContext(c interface {
	// Get reads a context value and reports whether its key exists.
	Get(string) (any, bool)
},
) *claude.ToolNameRewrite {
	if c == nil {
		return nil
	}
	raw, ok := c.Get(toolNameRewriteKey)
	if !ok || raw == nil {
		return nil
	}
	rw, _ := raw.(*claude.ToolNameRewrite)
	return rw
}

// ReverseToolNamesIfPresent restores tool names in chunk using the request mapping.
// Static name rewrites also apply when the context has no mapping. The delegated
// rewriter replaces matching byte substrings and can return the supplied slice
// when no replacement is needed; callers should use the returned bytes.
func ReverseToolNamesIfPresent(c interface {
	// Get reads a context value and reports whether its key exists.
	Get(string) (any, bool)
}, chunk []byte,
) []byte {
	rw := toolNameRewriteFromContext(c)
	if rw == nil && len(staticToolNameRewrites) == 0 {
		return chunk
	}
	// Static substitutions participate even when this request has no mapping;
	// the Anthropic output adapter therefore uses the same restoration as prepared requests.
	return claude.RestoreToolNamesInBytes(chunk, rw)
}
