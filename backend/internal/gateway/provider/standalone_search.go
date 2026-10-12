package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/gateway/searchtools"
	"github.com/TokenFlux/TokenRouter/internal/search/contract"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// GrokStandaloneSearchModel returns the configured default model for standalone search.
func GrokStandaloneSearchModel() string {
	return xai.ResolveDefaultTextModel(xai.RuntimeDefaultTextModel())
}
func GrokStandaloneSearchMaxResults(n int) int { return xai.NormalizeGrokWebSearchMaxResults(n) }

// GrokStandaloneSearchBody builds the protocol-specific body for X search or web search.
func GrokStandaloneSearchBody(request searchtools.StandaloneRequest, model string, maxResults int, isX bool) ([]byte, error) {
	if isX {
		return xai.BuildGrokXSearchResponsesBody(projectNativeStandaloneSearch(request), model)
	}
	return xai.BuildGrokWebSearchResponsesBody(request.Query, maxResults, model), nil
}

func GrokStandaloneSearchResponse(query string, response []byte, maxResults int) *contract.SearchResponse {
	return &contract.SearchResponse{Query: query, Results: projectStandaloneSearchResults(xai.ExtractGrokWebSearchSources(response, maxResults))}
}

func projectNativeStandaloneSearch(v searchtools.StandaloneRequest) xai.StandaloneSearchRequest {
	return xai.StandaloneSearchRequest{
		Query:                    v.Query,
		Input:                    v.Input,
		MaxResults:               v.MaxResults,
		AllowedXHandles:          v.AllowedXHandles,
		ExcludedXHandles:         v.ExcludedXHandles,
		FromDate:                 v.FromDate,
		ToDate:                   v.ToDate,
		EnableImageUnderstanding: v.EnableImageUnderstanding,
		EnableVideoUnderstanding: v.EnableVideoUnderstanding,
	}
}

func projectStandaloneSearchResults(in []xai.StandaloneSearchResult) []contract.SearchResult {
	if in == nil {
		return nil
	}
	out := make([]contract.SearchResult, len(in))
	for i, v := range in {
		out[i] = contract.SearchResult{URL: v.URL, Title: v.Title, Snippet: v.Snippet, PageAge: v.PageAge}
	}
	return out
}
