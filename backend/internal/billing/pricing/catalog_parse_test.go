package pricing

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCatalogProviderCompatibility supplies legacy provider names beside
// absent, populated, empty, and null current fields. Decoding and serialization
// must use provider, retain its precedence, and remove litellm_provider.
func TestCatalogProviderCompatibility(t *testing.T) {
	for _, tc := range []struct {
		fields, // submitted provider JSON fields
		want string // expected decoded provider label
	}{
		{`"litellm_provider":"openai"`, "openai"},
		{`"litellm_provider":"old","provider":"new"`, "new"},
		{`"litellm_provider":"old","provider":""`, ""},
		{`"litellm_provider":"old","provider":null`, ""},
	} {
		raw, err := DecodeCatalogEntries([]byte(`{"model":{"input_cost_per_token":0,` + tc.fields + `}}`))
		require.NoError(t, err)
		require.NotContains(t, string(raw["model"]), "litellm_provider")
		values, diagnostics, err := ParsePricingEntries(raw)
		require.NoError(t, err)
		require.NoError(t, diagnostics.ValidationError())
		require.Equal(t, tc.want, values["model"].Provider)
		output, err := json.Marshal(values["model"])
		require.NoError(t, err)
		require.NotContains(t, string(output), "litellm_provider")
		require.Contains(t, string(output), `"provider":`)
	}
}
