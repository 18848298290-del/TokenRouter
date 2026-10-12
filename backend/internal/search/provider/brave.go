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

const (
	braveSearchEndpoint = "https://api.search.brave.com/res/v1/web/search"
	braveMaxCount       = 20
	braveProviderName   = "brave"
)

// braveSearchURL parses the fixed, valid endpoint once at initialization.
var braveSearchURL, _ = url.Parse(braveSearchEndpoint)

// BraveProvider implements web search via the Brave Search API.
type BraveProvider struct {
	apiKey     string
	httpClient *http.Client
}

// braveResponse is the minimal structure of the Brave Search API response.
type braveResponse struct {
	Web struct {
		Results []braveResult `json:"results"`
	} `json:"web"`
}

type braveResult struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Age         string `json:"age"`
}

// NewBraveProvider creates a Brave Search provider.
// The caller configures proxy and timeouts; a nil client uses http.DefaultClient.
func NewBraveProvider(apiKey string, httpClient *http.Client) *BraveProvider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &BraveProvider{apiKey: apiKey, httpClient: httpClient}
}

func (b *BraveProvider) Name() string { return braveProviderName }

func (b *BraveProvider) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	count := req.MaxResults
	if count <= 0 {
		count = defaultMaxResults
	}
	if count > braveMaxCount {
		count = braveMaxCount
	}

	u := *braveSearchURL // Keep query parameters local to this request.
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
	defer func() { _ = resp.Body.Close() }()

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
