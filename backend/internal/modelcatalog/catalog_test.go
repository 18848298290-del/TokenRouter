package modelcatalog

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ambiguousImageCatalog gives relays different modalities but a shared public model identity.
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

			// Public attributes resolve the metadata ambiguity while prices remain provider-specific; returned
			// attributes are independent.
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
			// Missing attributes in an exact entry remain unknown.
			qualified, found = catalog.LookupAttributes("relay/"+model, []string{model})
			require.True(t, found)
			require.Nil(t, qualified.InputModalities)
			_, found = catalog.LookupAttributes("azure/missing", nil)
			require.False(t, found)
		})
	}
}

func TestLookupAttributesRejectsUncertainCanonical(t *testing.T) {
	for _, canonical := range []string{"", "other/model", "openai/missing"} {
		t.Run(canonical, func(t *testing.T) {
			// Change one provider's authorship while keeping the other record and public metadata intact.
			body := strings.Replace(ambiguousImageCatalog,
				`"canonical_model_id":"openai/gpt-image-2.5-flare"`,
				`"canonical_model_id":"`+canonical+`"`, 1)
			catalog, err := Parse([]byte(body))
			require.NoError(t, err)
			_, found := catalog.LookupAttributes("gpt-image-2.5-flare", nil)
			require.False(t, found)
		})
	}
	// Agreed authorship is insufficient when the public metadata is missing.
	body := strings.ReplaceAll(ambiguousImageCatalog,
		`"canonical_model_id":"openai/gpt-image-2.5-flare"`,
		`"canonical_model_id":"openai/missing"`)
	catalog, err := Parse([]byte(body))
	require.NoError(t, err)
	_, found := catalog.LookupAttributes("gpt-image-2.5-flare", nil)
	require.False(t, found)
}

func TestLookupAttributesKeepsOriginalEndpoint(t *testing.T) {
	body := strings.Replace(ambiguousImageCatalog, `"azure":`, `"openai":`, 1)
	catalog, err := Parse([]byte(body))
	require.NoError(t, err)
	attributes, found := catalog.LookupAttributes("gpt-image-2.5-flare", nil)
	require.True(t, found)
	require.Equal(t, []string{"text"}, *attributes.InputModalities)
}

// TestOfflineEmbeddingOrigin checks first-party models whose embedded records omit canonical identities.
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
