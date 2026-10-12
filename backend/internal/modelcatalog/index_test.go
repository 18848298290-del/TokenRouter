package modelcatalog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIndexQueriesCopyOnlyReturnedValues(t *testing.T) {
	catalog, err := Parse([]byte(`{"providers":{"openai":{"models":{"one":{"name":"First","reasoning":true},"two":{"name":"Second","reasoning":false}}}}}`))
	require.NoError(t, err)
	index := NewIndex(catalog)
	page := index.Search(AttributeQuery{Search: "first", Capability: "reasoning", Limit: 1})
	require.Equal(t, 2, page.Total)
	require.Len(t, page.Items, 1)
	*page.Items[0].Attributes.DisplayName = "changed"
	page.Providers[0] = "changed"
	names := index.ModelIDs()
	names[0] = "changed"
	next := index.Search(AttributeQuery{Search: "first", Capability: "reasoning", Limit: 1})
	require.Equal(t, "First", *next.Items[0].Attributes.DisplayName)
	require.Equal(t, []string{"openai"}, next.Providers)
	require.NotContains(t, index.ModelIDs(), "changed")
	require.Empty(t, index.Search(AttributeQuery{Offset: 100, Limit: 1}).Items)
}

func BenchmarkIndexPage(b *testing.B) {
	body, err := Offline()
	require.NoError(b, err)
	catalog, err := Parse(body)
	require.NoError(b, err)
	index := NewIndex(catalog)
	b.ResetTimer()
	for range b.N {
		page := index.Search(AttributeQuery{Limit: 20})
		if len(page.Items) != 20 {
			b.Fatal("missing page")
		}
	}
}
