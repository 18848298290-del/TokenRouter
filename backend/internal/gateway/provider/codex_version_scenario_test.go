package provider

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// TestCodexVersionConstants_Consistency compares the configured CLI version
// with the upstream minimum. It also checks that both generated and default
// gateway User-Agent strings contain the configured version.
func TestCodexVersionConstants_Consistency(t *testing.T) {
	require.GreaterOrEqual(t, clientmeta.CompareVersions(openai.CodexCLIVersion, openai.CodexUpstreamMinVersion), 0,
		"codexCLIVersion must not be below the upstream minimum")

	require.True(t, strings.Contains(openai.CodexCLIUserAgent, openai.CodexDefaultOriginator+"/"+openai.CodexCLIVersion),
		"codexCLIUserAgent must embed codexCLIVersion")

	require.True(t, strings.Contains(gateway.DefaultOpenAICodexUserAgent, openai.CodexCLIVersion),
		"DefaultOpenAICodexUserAgent must embed codexCLIVersion")
}
