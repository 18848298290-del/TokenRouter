package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Tavily receives credentials and query parameters together in a JSON POST
// using basic search depth. Its content field supplies each normalized snippet;
// the supplier score is decoded for response compatibility while the shared
// result shape carries links, titles, and snippets.

// These values select Tavily's endpoint, result identity, and the search depth
// encoded in every outgoing supplier request.
const (
	tavilySearchEndpoint   = "https://api.tavily.com/search" // fixed supplier search endpoint
	tavilyProviderName     = "tavily"                        // normalized Tavily supplier name
	tavilySearchDepthBasic = "basic"                         // outgoing basic search depth
)

// TavilyProvider sends authenticated Tavily searches through the caller's
// chosen HTTP client.
type TavilyProvider struct {
	apiKey string // Tavily request authentication key
	// httpClient supplies transport and timeout policy. NewTavilyProvider retains
	// the supplied client, which callers configure before starting concurrent
	// searches.
	httpClient *http.Client
}

// tavilyRequest encodes credentials, query, result count, and basic depth for
// Tavily.
type tavilyRequest struct {
	APIKey      string `json:"api_key"`      // supplier request authentication key
	Query       string `json:"query"`        // query sent to Tavily
	MaxResults  int    `json:"max_results"`  // requested supplier result count
	SearchDepth string `json:"search_depth"` // requested supplier search depth
}

// tavilyResponse decodes the supplier's result array before shared result
// conversion.
type tavilyResponse struct {
	Results []tavilyResult `json:"results"` // decoded supplier result entries
}

// tavilyResult contains one supplier result and its decoded relevance score.
type tavilyResult struct {
	URL     string  `json:"url"`     // result page address
	Title   string  `json:"title"`   // result page title
	Content string  `json:"content"` // supplier result snippet text
	Score   float64 `json:"score"`   // decoded supplier relevance score
}

// NewTavilyProvider retains apiKey and the chosen HTTP client for Tavily calls.
// A nil client selects http.DefaultClient; callers configure timeout and proxy
// policy on that client before searching.
func NewTavilyProvider(apiKey string, httpClient *http.Client) *TavilyProvider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &TavilyProvider{apiKey: apiKey, httpClient: httpClient}
}

// Name returns the Tavily identifier used to label successful search results.
func (t *TavilyProvider) Name() string { return tavilyProviderName }

// Search sends a Tavily JSON POST using basic depth and normalizes its results.
// Nonpositive MaxResults selects five; positive values pass through to the
// supplier. The chosen client and nonnil context govern transport and
// cancellation, and deferred response-body close errors are suppressed.
func (t *TavilyProvider) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	maxResults := req.MaxResults
	if maxResults <= 0 {
		maxResults = defaultMaxResults
	}

	payload := tavilyRequest{
		APIKey:      t.apiKey,
		Query:       req.Query,
		MaxResults:  maxResults,
		SearchDepth: tavilySearchDepthBasic,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("tavily: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tavilySearchEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("tavily: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("tavily: request failed: %w", err)
	}
	// Close the received body after each result or error path; a close failure
	// cannot replace the already selected search result.
	defer func() { _ = resp.Body.Close() }()

	// Bound response reads to one MiB before decoding or reporting status errors;
	// data beyond that prefix does not enter result parsing.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("tavily: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tavily: status %d: %s", resp.StatusCode, truncateBody(body))
	}

	var raw tavilyResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("tavily: decode response: %w", err)
	}

	// Use Tavily content as the shared snippet and leave page age empty because
	// this decoded supplier shape supplies no age field.
	results := make([]SearchResult, 0, len(raw.Results))
	for _, r := range raw.Results {
		results = append(results, SearchResult{
			URL:     r.URL,
			Title:   r.Title,
			Snippet: r.Content,
		})
	}

	return &SearchResponse{Results: results, Query: req.Query}, nil
}
