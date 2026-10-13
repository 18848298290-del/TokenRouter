package bedrock

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBedrockModelRegionRules_DefaultCatalogAndSourceConsistency checks every
// default alias against the static model table. It checks nonempty source-link
// metadata, base-ID agreement, source-set membership, and unique geographic
// routing within each recorded model.
func TestBedrockModelRegionRules_DefaultCatalogAndSourceConsistency(t *testing.T) {
	t.Parallel()
	for _, modelID := range DefaultBedrockModelMapping {
		require.Contains(t, BedrockModelRegionRules, BedrockBaseModelID(modelID))
	}
	for baseID, rule := range BedrockModelRegionRules {
		require.NotEmpty(t, rule.SourceURL, baseID)
		for _, source := range rule.InRegionSources {
			require.Contains(t, rule.DocumentedRegions, source)
		}
		seen := map[string]bool{}
		for _, profile := range rule.GeoProfiles {
			require.Equal(t, baseID, BedrockBaseModelID(profile.Id))
			for _, source := range profile.SourceRegions {
				require.False(t, seen[source], "同一型号来源区域不可同时指向两个地域 ID：%s / %s", baseID, source)
				seen[source] = true
				require.Contains(t, rule.DocumentedRegions, source)
				require.NotContains(t, rule.UnverifiedGeoRegions, source)
			}
		}
		if rule.GlobalProfile.Id != "" {
			require.Equal(t, "global."+baseID, rule.GlobalProfile.Id)
			for _, source := range rule.GlobalProfile.SourceRegions {
				require.Contains(t, rule.DocumentedRegions, source)
			}
		}
	}
}

// TestDefaultBedrockModelMapping_ContainsNewClaudeModels compares selected
// public aliases with their expected vendor IDs. It checks exact version/date
// suffixes and suffix-free entries so alias changes cannot silently invent a
// different resource identity.
func TestDefaultBedrockModelMapping_ContainsNewClaudeModels(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"claude-fable-5-1": "anthropic.claude-fable-5-1",
		"claude-fable-5":   "anthropic.claude-fable-5",
		"claude-opus-5":    "us.anthropic.claude-opus-5",
		"claude-opus-4-8":  "us.anthropic.claude-opus-4-8",
		"claude-opus-4-7":  "us.anthropic.claude-opus-4-7",
		"claude-sonnet-5":  "us.anthropic.claude-sonnet-5",
		// These model IDs retain their version or date suffixes.
		"claude-opus-4-6":          "us.anthropic.claude-opus-4-6-v1",
		"claude-opus-4-5-20251101": "us.anthropic.claude-opus-4-5-20251101-v1:0",
	}
	for from, want := range cases {
		got, ok := DefaultBedrockModelMapping[from]
		if !ok {
			t.Fatalf("expected Bedrock mapping for %q to exist", from)
		}
		if got != want {
			t.Fatalf("unexpected Bedrock mapping for %q: got %q want %q", from, got, want)
		}
	}
}
