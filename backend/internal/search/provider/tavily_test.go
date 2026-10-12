package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tavily fixtures check JSON field names and decoded result values
// independently of a network exchange. They serialize a request value and
// manually construct the common snippet representation, so their assertions
// cover these shapes rather than a live Tavily Search invocation.

// TestTavilyProvider_Name constructs a Tavily adapter using the default client.
// Its name matches the expected supplier identifier; the test makes no HTTP
// request or credential check.
func TestTavilyProvider_Name(t *testing.T) {
	p := NewTavilyProvider("key", nil)
	require.Equal(t, "tavily", p.Name())
}

// TestTavilyProvider_Search_RequestConstruction serializes a tavilyRequest
// fixture with credentials, query, count, and basic depth. Decoded JSON exposes
// the expected field values without invoking the provider's HTTP Search method.
func TestTavilyProvider_Search_RequestConstruction(t *testing.T) {
	req := tavilyRequest{
		APIKey:      "test-key",
		Query:       "golang",
		MaxResults:  3,
		SearchDepth: tavilySearchDepthBasic,
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))
	require.Equal(t, "test-key", parsed["api_key"])
	require.Equal(t, "golang", parsed["query"])
	require.Equal(t, float64(3), parsed["max_results"])
	require.Equal(t, "basic", parsed["search_depth"])
}

// TestTavilyProvider_Search_ResponseParsing decodes a stored result fixture and
// checks its URL, content, and score. Manual common-result construction maps
// content to snippet while leaving page age empty.
func TestTavilyProvider_Search_ResponseParsing(t *testing.T) {
	rawResp := `{"results":[{"url":"https://go.dev","title":"Go","content":"Go programming language","score":0.95}]}`
	var resp tavilyResponse
	require.NoError(t, json.Unmarshal([]byte(rawResp), &resp))
	require.Len(t, resp.Results, 1)
	require.Equal(t, "https://go.dev", resp.Results[0].URL)
	require.Equal(t, "Go programming language", resp.Results[0].Content)
	// Keep the score assertion tolerant of small floating-point decoding differences.
	require.InDelta(t, 0.95, resp.Results[0].Score, 0.001)

	// Construct the shared result manually from decoded Tavily content, protecting
	// its snippet identity and empty page age without executing the HTTP adapter.
	results := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		results = append(results, SearchResult{
			URL: r.URL, Title: r.Title, Snippet: r.Content,
		})
	}
	require.Equal(t, "Go programming language", results[0].Snippet)
	require.Equal(t, "", results[0].PageAge)
}

// TestTavilyProvider_Search_EmptyResults decodes an explicitly empty supplier
// result array from JSON. The result collection stays empty after successful
// decoding, protecting the response shape used by search normalization.
func TestTavilyProvider_Search_EmptyResults(t *testing.T) {
	var resp tavilyResponse
	require.NoError(t, json.Unmarshal([]byte(`{"results":[]}`), &resp))
	require.Empty(t, resp.Results)
}

// TestTavilyProvider_Search_InvalidJSON passes malformed fixture bytes to JSON
// decoding for a supplier response. The decoder returns an error; provider HTTP
// status handling is outside this scenario.
func TestTavilyProvider_Search_InvalidJSON(t *testing.T) {
	var resp tavilyResponse
	require.Error(t, json.Unmarshal([]byte("not json"), &resp))
}
