package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// Brave receives its subscription token in a header and its query and requested
// count in URL parameters. Responses expose result age alongside link and
// description text; successful empty result sets remain valid searches, while
// non-200 responses become bounded diagnostic excerpts.

// These values select Brave's endpoint, identify its normalized results, and
// cap the count placed in outgoing query parameters.
const (
	braveSearchEndpoint = "https://api.search.brave.com/res/v1/web/search" // fixed supplier search endpoint
	braveMaxCount       = 20                                               // maximum outgoing result count
	braveProviderName   = "brave"                                          // normalized Brave supplier name
)

// braveSearchURL holds the parsed fixed endpoint copied for each request's
// query parameters. Parsing errors are suppressed for this valid literal;
// package tests temporarily replace its pointed-to URL serially.
var braveSearchURL, _ = url.Parse(braveSearchEndpoint)

// BraveProvider sends authenticated Brave searches through the caller's chosen
// HTTP client.
type BraveProvider struct {
	apiKey string // Brave subscription authentication token
	// httpClient supplies transport and timeout policy. NewBraveProvider retains
	// the caller's client, so configure it before concurrent searches begin.
	httpClient *http.Client
}

// braveResponse decodes Brave's nested web result collection for shared result
// conversion.
type braveResponse struct {
	Web struct { // Brave web result section
		Results []braveResult `json:"results"` // decoded Brave result entries
	} `json:"web"`
}

// braveResult holds the supplier fields used for one normalized search result.
type braveResult struct {
	URL         string `json:"url"`         // result page address
	Title       string `json:"title"`       // result page title
	Description string `json:"description"` // supplier description text
	Age         string `json:"age"`         // supplier page-age description
}

// NewBraveProvider retains apiKey and the supplied HTTP client for Brave calls.
// A nil client selects http.DefaultClient; callers configure proxy routing and
// timeouts through the chosen client's transport.
func NewBraveProvider(apiKey string, httpClient *http.Client) *BraveProvider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &BraveProvider{apiKey: apiKey, httpClient: httpClient}
}

// Name returns the Brave identifier used to label successful search results.
func (b *BraveProvider) Name() string { return braveProviderName }

// Search sends a Brave GET and converts its web results to shared search
// values. Nonpositive MaxResults selects five, positive counts cap at twenty,
// and the query is submitted as supplied. The configured client and nonnil
// context govern the HTTP exchange; response-body closure errors are suppressed
// after reading.
func (b *BraveProvider) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	count := req.MaxResults
	if count <= 0 {
		count = defaultMaxResults
	}
	// Limit Brave's outgoing count while retaining the caller's requested query
	// text.
	if count > braveMaxCount {
		count = braveMaxCount
	}

	// Copy the shared endpoint before adding query parameters so concurrent
	// searches cannot overwrite each other's encoded query or count.
	u := *braveSearchURL
	q := u.Query()
	q.Set("q", req.Query)
	q.Set("count", strconv.Itoa(count))
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("brave: build request: %w", err)
	}
	httpReq.Header.Set("X-Subscription-Token", b.apiKey)
	httpReq.Header.Set("Accept", "application/json")

	resp, err := b.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("brave: request failed: %w", err)
	}
	// Release the response body on every decode or status path; a deferred close
	// error does not replace the selected search outcome.
	defer func() { _ = resp.Body.Close() }()

	// Read at most one MiB before status reporting or JSON decoding; oversized
	// bodies may reach the decoder as truncated input.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("brave: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("brave: status %d: %s", resp.StatusCode, truncateBody(body))
	}

	var raw braveResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("brave: decode response: %w", err)
	}

	// Map Brave descriptions and age into the common result fields so gateway tool
	// output can consume both supplier response formats.
	results := make([]SearchResult, 0, len(raw.Web.Results))
	for _, r := range raw.Web.Results {
		results = append(results, SearchResult{
			URL:     r.URL,
			Title:   r.Title,
			Snippet: r.Description,
			PageAge: r.Age,
		})
	}

	return &SearchResponse{Results: results, Query: req.Query}, nil
}
