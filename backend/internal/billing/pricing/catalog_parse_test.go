package pricing

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCatalogProviderCompatibility checks legacy input, provider precedence, and serialized field names.
func TestCatalogProviderCompatibility(t *testing.T) {
	for _, tc := range []struct{ fields, want string }{
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
