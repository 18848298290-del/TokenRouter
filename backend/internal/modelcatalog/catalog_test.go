package modelcatalog

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A bare identity can have ambiguous relay prices and still have reliable
// public attributes. Fixtures vary canonical authorship and original endpoints
// to verify metadata lookup, first party preference, explicit zero prices, and
// missing price values independently.

// ambiguousImageCatalog supplies different relay modalities and prices with
// one shared public image model identity.
const ambiguousImageCatalog = `{
	"models":{"openai/gpt-image-2.5-flare":{
		"name":"GPT Image 2.5 Flare",
		"modalities":{"input":["text","image"],"output":["image"]}
	}},
	"providers":{
		"azure":{"models":{"gpt-image-2.5-flare":{
			"canonical_model_id":"openai/gpt-image-2.5-flare",
			"modalities":{"input":["text"],"output":["image"]},
			"cost":{"input":1,"output":2}
		}}},
		"relay":{"models":{"gpt-image-2.5-flare":{
			"canonical_model_id":"openai/gpt-image-2.5-flare",
			"cost":{"input":3,"output":4}
		}}}
	}
}`

// TestLookupAttributesCanonicalFallback uses relay entries that agree on a
// public author identity. Bare metadata must use copied public attributes while
// prices remain qualified and exact relay metadata retains its own omissions.
func TestLookupAttributesCanonicalFallback(t *testing.T) {
	for _, variant := range []string{"flare", "sunburst"} {
		t.Run(variant, func(t *testing.T) {
			model := "gpt-image-2.5-" + variant
			catalog, err := Parse([]byte(strings.ReplaceAll(ambiguousImageCatalog, "flare", variant)))
			require.NoError(t, err)
			attributes, found := catalog.LookupAttributes(model, nil)
			require.True(t, found)
			require.Equal(t, []string{"text", "image"}, *attributes.InputModalities)
			require.Equal(t, []string{"image"}, *attributes.OutputModalities)

			// Canonical agreement makes public attributes available for this bare
			// identity, while independent price lookup still requires a
			// provider-qualified record.
			require.True(t, catalog.RequiresExact(model))
			_, found = catalog.Lookup([]string{model})
			require.False(t, found)
			(*attributes.InputModalities)[0] = "audio"
			again, found := catalog.LookupAttributes(model, nil)
			require.True(t, found)
			require.Equal(t, []string{"text", "image"}, *again.InputModalities)

			qualified, found := catalog.LookupAttributes("azure/"+model, []string{model})
			require.True(t, found)
			require.Equal(t, []string{"text"}, *qualified.InputModalities)
			price, found := catalog.Lookup([]string{"azure/" + model})
			require.True(t, found)
			require.Equal(t, 1.0, *price.Cost.Input)
			// The exact relay entry has no input modalities declared. Its known
			// identity takes precedence over public metadata, leaving that
			// capability unknown.
			qualified, found = catalog.LookupAttributes("relay/"+model, []string{model})
			require.True(t, found)
			require.Nil(t, qualified.InputModalities)
			_, found = catalog.LookupAttributes("azure/missing", nil)
			require.False(t, found)
		})
	}
}

// TestLookupAttributesRejectsUncertainCanonical changes canonical agreement
// and public metadata availability. An unknown author, conflicting identity,
// or missing public record must leave bare attributes unresolved.
func TestLookupAttributesRejectsUncertainCanonical(t *testing.T) {
	for _, canonical := range []string{"", "other/model", "openai/missing"} {
		t.Run(canonical, func(t *testing.T) {
			// Change one provider's authorship while retaining the other record
			// and public metadata, so failure depends on canonical agreement
			// across both candidates.
			body := strings.Replace(ambiguousImageCatalog,
				`"canonical_model_id":"openai/gpt-image-2.5-flare"`,
				`"canonical_model_id":"`+canonical+`"`, 1)
			catalog, err := Parse([]byte(body))
			require.NoError(t, err)
			_, found := catalog.LookupAttributes("gpt-image-2.5-flare", nil)
			require.False(t, found)
		})
	}
	// Both providers now agree on an identity absent from the public collection.
	// Agreement alone cannot supply attributes when their canonical model has
	// no metadata.
	body := strings.ReplaceAll(ambiguousImageCatalog,
		`"canonical_model_id":"openai/gpt-image-2.5-flare"`,
		`"canonical_model_id":"openai/missing"`)
	catalog, err := Parse([]byte(body))
	require.NoError(t, err)
	_, found := catalog.LookupAttributes("gpt-image-2.5-flare", nil)
	require.False(t, found)
}

// TestLookupAttributesKeepsOriginalEndpoint replaces one relay with the
// original author endpoint. Its exact bare entry must keep endpoint modalities
// even when the public collection declares additional input support.
func TestLookupAttributesKeepsOriginalEndpoint(t *testing.T) {
	body := strings.Replace(ambiguousImageCatalog, `"azure":`, `"openai":`, 1)
	catalog, err := Parse([]byte(body))
	require.NoError(t, err)
	attributes, found := catalog.LookupAttributes("gpt-image-2.5-flare", nil)
	require.True(t, found)
	require.Equal(t, []string{"text"}, *attributes.InputModalities)
}

// TestOfflineEmbeddingOrigin reads embedded models whose records omit
// canonical identities. Known OpenAI endpoint recognition must still make
// their bare names unambiguous and retain the qualified entry's price values.
func TestOfflineEmbeddingOrigin(t *testing.T) {
	body, err := Offline()
	require.NoError(t, err)
	catalog, err := Parse(body)
	require.NoError(t, err)
	for _, model := range []string{"text-embedding-3-small", "text-embedding-3-large", "text-embedding-ada-002"} {
		t.Run(model, func(t *testing.T) {
			entry, found := catalog.Lookup([]string{model})
			require.True(t, found)
			require.True(t, entry.FirstParty)
			require.Equal(t, "openai", entry.Provider)
			require.False(t, catalog.Ambiguous[model])
			qualified, found := catalog.Lookup([]string{"openai/" + model})
			require.True(t, found)
			require.Equal(t, qualified.Cost, entry.Cost)
		})
	}
}

// TestKnownOriginWithoutCanonicalAndExplicitForeignOrigin combines known
// endpoints, foreign authorship, and competing unknown relays. Endpoint
// recognition must select its own embedding record, explicit authorship wins
// for foreign models, and unknown competition remains ambiguous.
func TestKnownOriginWithoutCanonicalAndExplicitForeignOrigin(t *testing.T) {
	catalog, err := Parse([]byte(`{
		"providers": {
			"openai": {"models": {
				"embedding": {"cost":{"input":1,"output":0}},
				"foreign": {"canonical_model_id":"author/foreign","cost":{"input":9,"output":9}}
			}},
			"azure": {"models":{"embedding":{"cost":{"input":2,"output":0}}}},
			"author": {"models":{"foreign":{"canonical_model_id":"author/foreign","cost":{"input":3,"output":4}}}},
			"relay-a": {"models":{"unknown":{"cost":{"input":5,"output":6}}}},
			"relay-b": {"models":{"unknown":{"cost":{"input":7,"output":8}}}}
		}
	}`))
	require.NoError(t, err)
	entry, found := catalog.Lookup([]string{"embedding"})
	require.True(t, found)
	require.Equal(t, "openai", entry.Provider)
	entry, found = catalog.Lookup([]string{"azure/embedding"})
	require.True(t, found)
	require.False(t, entry.FirstParty)
	require.Equal(t, 2.0, *entry.Cost.Input)
	entry, found = catalog.Lookup([]string{"foreign"})
	require.True(t, found)
	require.Equal(t, "author", entry.Provider)
	require.True(t, catalog.Ambiguous["unknown"])
}

// TestMistralOriginRespectsForeignCanonical gives Mistral a local model and
// another author's model. Bare lookup must prefer Mistral for its own record
// and the declared author for the foreign identity.
func TestMistralOriginRespectsForeignCanonical(t *testing.T) {
	catalog, err := Parse([]byte(`{"providers":{
		"mistral":{"models":{
			"devstral-latest":{"cost":{"input":0.4,"output":2}},
			"glm-test":{"canonical_model_id":"zhipuai/glm-test","cost":{"input":9,"output":9}}
		}},
		"requesty":{"models":{"devstral-latest":{"cost":{"input":0.44,"output":2.2}}}},
		"zai":{"models":{"glm-test":{"canonical_model_id":"zhipuai/glm-test","cost":{"input":1,"output":2}}}}
	}}`))
	require.NoError(t, err)
	entry, found := catalog.Lookup([]string{"devstral-latest"})
	require.True(t, found)
	require.Equal(t, "mistral", entry.Provider)
	entry, found = catalog.Lookup([]string{"glm-test"})
	require.True(t, found)
	require.Equal(t, "zai", entry.Provider)
	entry, found = catalog.Lookup([]string{"mistral/glm-test"})
	require.True(t, found)
	require.False(t, entry.FirstParty)
}

// TestOfflineCatalog decompresses and parses the embedded models.dev
// snapshot. A registered Claude identity must expose its Anthropic provider,
// known input price, and declared context limit for offline startup.
func TestOfflineCatalog(t *testing.T) {
	body, err := Offline()
	require.NoError(t, err)
	catalog, err := Parse(body)
	require.NoError(t, err)
	entry, ok := catalog.Lookup([]string{"claude-sonnet-4-5"})
	require.True(t, ok)
	require.Equal(t, "anthropic", entry.Provider)
	require.Equal(t, 3.0, *entry.Cost.Input)
	require.NotNil(t, entry.Attributes.Context)
}

// TestCatalogIdentityAndUnpricedAttributes combines an origin's zero input
// rate, relay prices, and attribute-only records. Exact qualification must
// retain each provider's price, while ambiguous bare names and missing prices
// remain distinct from declared zero prices.
func TestCatalogIdentityAndUnpricedAttributes(t *testing.T) {
	catalog, err := Parse([]byte(`{"models":{"origin/m":{"name":"Original"}},"providers":{"relay":{"models":{"m":{"name":"Relay","cost":{"input":9,"output":9}},"unpriced":{"name":"No price","reasoning":false}}},"origin":{"models":{"m":{"name":"Original","cost":{"input":0,"output":2}}}},"other":{"models":{"unpriced":{"name":"Other"}}}}}`))
	require.NoError(t, err)
	original, ok := catalog.Lookup([]string{"m"})
	require.True(t, ok)
	require.Equal(t, 0.0, *original.Cost.Input)
	relay, ok := catalog.Lookup([]string{"relay/m"})
	require.True(t, ok)
	require.Equal(t, 9.0, *relay.Cost.Input)
	_, ambiguous := catalog.Lookup([]string{"unpriced"})
	require.False(t, ambiguous)
	noPrice, ok := catalog.Lookup([]string{"relay/unpriced"})
	require.True(t, ok)
	require.Nil(t, noPrice.Cost.Input)
	require.NotNil(t, noPrice.Attributes.Reasoning)
	require.False(t, *noPrice.Attributes.Reasoning)
}
