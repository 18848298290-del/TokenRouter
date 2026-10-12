package httpapi

import (
	claude "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// toolNameRewriteKey stores the request tool-name mapping in gin.Context.
// The response path uses this mapping to restore original names in response bytes.
const toolNameRewriteKey = "claude_tool_name_rewrite"

var staticToolNameRewrites = claude.StaticToolNameRewrites

// toolNameRewriteFromContext reads the tool-name mapping saved during request preparation.
// It returns nil for a nil context, missing key, or unexpected value type.
func toolNameRewriteFromContext(c interface {
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
// Static name rewrites also apply when the context has no mapping.
func ReverseToolNamesIfPresent(c interface {
	Get(string) (any, bool)
}, chunk []byte,
) []byte {
	rw := toolNameRewriteFromContext(c)
	if rw == nil && len(staticToolNameRewrites) == 0 {
		return chunk
	}
	return claude.RestoreToolNamesInBytes(chunk, rw)
}
