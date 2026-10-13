package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIsOpenAIOAuthServableModel compares recognized, unknown, case-varied,
// and path-qualified model names. Admission must reject the listed foreign
// prefixes and bare Kimi IDs while allowing unlisted aliases.
func TestIsOpenAIOAuthServableModel(t *testing.T) {
	require.True(t, IsOpenAIOAuthServableModel("gpt-5.4-high"))
	require.True(t, IsOpenAIOAuthServableModel("  gpt-5.3-codex  "))
	require.True(t, IsOpenAIOAuthServableModel("claude-3-5-haiku-20241022"))
	require.True(t, IsOpenAIOAuthServableModel("DeepThink-x"))  // An unlisted prefix remains eligible.
	require.False(t, IsOpenAIOAuthServableModel("DeepSeek-V4")) // Prefix matching is case-insensitive.
	require.False(t, IsOpenAIOAuthServableModel("qwen3-235b-thinking"))
	require.False(t, IsOpenAIOAuthServableModel("k3"))
	require.False(t, IsOpenAIOAuthServableModel("k3-256k"))
	require.False(t, IsOpenAIOAuthServableModel("provider/k3"))
	require.True(t, IsOpenAIOAuthServableModel("my-k3-alias"))   // Custom aliases remain eligible.
	require.True(t, IsOpenAIOAuthServableModel("deepseekcoder")) // The required hyphen distinguishes this name from the blocked prefix.
}
