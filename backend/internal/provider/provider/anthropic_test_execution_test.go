package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog/testkit"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

func TestAnthropicProbeUsesServiceSamplingCapabilities(t *testing.T) {
	for _, tc := range []struct {
		kind, model string
		sampling    bool
	}{
		{"apikey", "claude-opus-4-7", false},
		{"oauth", "claude-opus-4-7", false},
		{"service_account", "claude-opus-4-7@default", false},
		{"bedrock", "us.anthropic.claude-opus-4-7", false},
		{"apikey", "claude-sonnet-4-5", true},
		{"apikey", "custom-unknown", true},
	} {
		t.Run(tc.kind+"/"+tc.model, func(t *testing.T) {
			reads := 0
			executor := AnthropicProviderTest{Attributes: func() modelcatalog.View { reads++; return testkit.OfflineView() }}
			payload := map[string]any{"temperature": 1, "max_tokens": 256}
			executor.applyModelAttributes(&providercore.Record{Platform: "anthropic", Type: tc.kind}, tc.model, payload)
			_, present := payload["temperature"]
			require.Equal(t, tc.sampling, present)
			require.Equal(t, 256, payload["max_tokens"])
			require.Equal(t, 1, reads)
		})
	}
}
