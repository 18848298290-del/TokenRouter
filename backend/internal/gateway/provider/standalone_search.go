package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/gateway/searchtools"
	"github.com/TokenFlux/TokenRouter/internal/search/contract"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// standalone_search.go builds Grok Web and X search bodies and converts
// extracted sources into search results.
// Standalone Grok Web and X search build Responses requests through distinct
// upstream encoders. The gateway adapter transfers the search API's filters
// and media-understanding options, then converts extracted sources to search
// results with the original query retained for the caller.

// GrokStandaloneSearchModel resolves the current runtime default through Grok
// text-model rules.
func GrokStandaloneSearchModel() string {
	return xai.ResolveDefaultTextModel(xai.RuntimeDefaultTextModel())
}

// GrokStandaloneSearchMaxResults normalizes the requested result count using
// Grok search limits. Nonpositive counts use the upstream default, oversized
// counts use the upstream maximum, and counts inside that range remain
// unchanged.
func GrokStandaloneSearchMaxResults(n int) int { return xai.NormalizeGrokWebSearchMaxResults(n) }

// GrokStandaloneSearchBody encodes X search with request filters or web search
// with maxResults. X search uses request.MaxResults and can return validation
// errors; the web encoder uses the separate maxResults argument and returns
// its body with nil error.
func GrokStandaloneSearchBody(request searchtools.StandaloneRequest, model string, maxResults int, isX bool) ([]byte, error) {
	if isX {
		return xai.BuildGrokXSearchResponsesBody(projectNativeStandaloneSearch(request), model)
	}
	return xai.BuildGrokWebSearchResponsesBody(request.Query, maxResults, model), nil
}

// GrokStandaloneSearchResponse combines query with extracted upstream sources
// converted to public result fields. maxResults is passed to source
// extraction, which controls accepted sources and their truncation before this
// adapter copies them.
func GrokStandaloneSearchResponse(query string, response []byte, maxResults int) *contract.SearchResponse {
	return &contract.SearchResponse{Query: query, Results: projectStandaloneSearchResults(xai.ExtractGrokWebSearchSources(response, maxResults))}
}

// projectNativeStandaloneSearch transfers query, input alias, handle filters,
// dates, and media-understanding options to the Grok request. Slice and
// pointer values are borrowed, so callers keep the request stable while the
// encoder reads.
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

// projectStandaloneSearchResults converts source records to public URL, title,
// snippet, and age values. Nil input returns nil; other inputs receive a new
// result slice with scalar strings copied.
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
