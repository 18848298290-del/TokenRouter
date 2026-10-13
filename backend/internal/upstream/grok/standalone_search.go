package grok

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// A search response can contain tool source URLs, citation annotations, and
// assistant-written JSON results. Structured results are admitted only when
// their normalized URL appears in that source evidence, preventing an
// invented assistant URL from becoming a standalone search result. The
// returned URL retains the evidence source's spelling.
//
// Structured results determine the initial result order and can enrich title
// or snippet text. Remaining observed sources are appended in discovery order
// up to the normalized result limit. URL matching removes fragments and
// normalizes hostname case and empty paths while retaining queries. Blank,
// URL-valued, or integer-parsable titles fall back to source text or hostname.
// Snippets can remain
// empty when neither representation supplies them.

// Search prompts and extraction share a default and hard cap so returned
// source counts follow the same request-size rule across web and X searches.
const (
	defaultGrokWebSearchResults = 5  // default search result count
	maxGrokWebSearchResults     = 20 // maximum search result count
)

// StandaloneSearchRequest carries query text and optional native X-search
// filters from the HTTP request.
type StandaloneSearchRequest struct {
	// Query takes precedence over Input when its trimmed value is nonblank in
	// BuildGrokXSearchResponsesBody. The HTTP caller also validates whether
	// usable query text is present before forwarding.
	Query string `json:"query"` // primary search query text
	Input string `json:"input"` // alternate search query text
	// MaxResults is normalized for the prompt and result extraction; nil uses
	// the default. It is not sent as an x_search tool count parameter.
	MaxResults       *int     `json:"max_results"`        // optional search result limit
	AllowedXHandles  []string `json:"allowed_x_handles"`  // included X account handles
	ExcludedXHandles []string `json:"excluded_x_handles"` // excluded X account handles
	// FromDate and ToDate are trimmed and included when nonblank. The builder
	// leaves date syntax validation to the native search caller and upstream request handling.
	FromDate string `json:"from_date"` // search start date text
	ToDate   string `json:"to_date"`   // search end date text
	// EnableImageUnderstanding and EnableVideoUnderstanding use pointers so
	// supplied false values remain distinct from absence.
	// BuildGrokXSearchResponsesBody includes each option whenever its pointer is
	// present.
	EnableImageUnderstanding *bool `json:"enable_image_understanding"` // optional image understanding flag
	EnableVideoUnderstanding *bool `json:"enable_video_understanding"` // optional video understanding flag
}

// StandaloneSearchResult carries an observed search URL with its optional
// display details.
type StandaloneSearchResult struct {
	URL     string `json:"url"`                // observed search source URL
	Title   string `json:"title"`              // result display title text
	Snippet string `json:"snippet"`            // result display summary text
	PageAge string `json:"page_age,omitempty"` // reported result age text
}

// NormalizeGrokWebSearchMaxResults selects five results for nonpositive input
// and caps positive input at twenty. Values within that range are retained
// for search prompts and source extraction to use the same limit.
func NormalizeGrokWebSearchMaxResults(maxResults int) int {
	if maxResults <= 0 {
		return defaultGrokWebSearchResults
	}
	// Keep extraction and the requested prompt within the same result cap.
	if maxResults > maxGrokWebSearchResults {
		return maxGrokWebSearchResults
	}
	return maxResults
}

// BuildGrokWebSearchPrompt formats a web-search instruction with the
// normalized result limit and supplied query. The query is interpolated as
// given; callers validate input and treat the requested JSON shape as an
// instruction rather than guaranteed output.
func BuildGrokWebSearchPrompt(query string, maxResults int) string {
	return fmt.Sprintf(`Search the web for the user query below. Return ONLY valid JSON with this exact shape: {"results":[{"url":"https://...","title":"page title","snippet":"concise factual summary"}]}. Return at most %d unique results. Every URL must be an actual web_search source. Populate a non-empty title and snippet for every result. Do not wrap the JSON in markdown.

User query:
%s`, NormalizeGrokWebSearchMaxResults(maxResults), query)
}

// ExtractGrokWebSearchSources joins assistant result details to observed tool
// and citation URLs. Empty or invalid JSON returns nil, and maxResults
// follows the shared five-default, twenty-cap rule. Source keys normalize URL
// matching, but returned URLs retain the first observed spelling.
//
// Structured assistant results are accepted in their order only when a source
// key matches; duplicate URLs are skipped and missing display text can use
// source details. Remaining source entries append in discovery order until
// the limit. Source titles can fall back to hostnames, while snippets may
// remain empty. This extraction verifies evidence membership, not destination
// safety for subsequently fetching a URL.
func ExtractGrokWebSearchSources(body []byte, maxResults int) []StandaloneSearchResult {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil
	}
	maxResults = NormalizeGrokWebSearchMaxResults(maxResults)

	sources := make(map[string]StandaloneSearchResult)
	var sourceOrder []string
	addSource := func(rawURL, title, snippet string) {
		key, ok := NormalizeGrokWebSearchURL(rawURL)
		if !ok {
			return
		}
		result, exists := sources[key]
		if !exists {
			result.URL = strings.TrimSpace(rawURL)
			sourceOrder = append(sourceOrder, key)
		}
		if result.Title == "" {
			result.Title = UsableGrokWebSearchTitle(title, result.URL)
		}
		if result.Snippet == "" {
			result.Snippet = strings.TrimSpace(snippet)
		}
		sources[key] = result
	}

	output := gjson.GetBytes(body, "output")
	output.ForEach(func(_, item gjson.Result) bool {
		callType := item.Get("type").String()
		if callType == "web_search_call" || callType == "x_search_call" {
			sources := item.Get("action.sources")
			if sources.IsArray() {
				sources.ForEach(func(_, src gjson.Result) bool {
					addSource(src.Get("url").String(), src.Get("title").String(), src.Get("snippet").String())
					return true
				})
			}
		}
		if item.Get("type").String() == "message" {
			item.Get("content").ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() != "output_text" {
					return true
				}
				part.Get("annotations").ForEach(func(_, ann gjson.Result) bool {
					if ann.Get("type").String() == "url_citation" || ann.Get("type").String() == "web" {
						addSource(ann.Get("url").String(), ann.Get("title").String(), "")
					}
					return true
				})
				return true
			})
		}
		return true
	})

	var out []StandaloneSearchResult
	seen := make(map[string]bool)
	output.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() != "message" {
			return true
		}
		item.Get("content").ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() != "output_text" || len(out) >= maxResults {
				return true
			}
			for _, result := range ParseGrokWebSearchStructuredResults(part.Get("text").String()) {
				key, ok := NormalizeGrokWebSearchURL(result.URL)
				if !ok || seen[key] {
					continue
				}
				// Assistant JSON can enrich observed URLs only. Membership in collected
				// tool or citation evidence prevents unrelated generated links from entering the results.
				source, allowed := sources[key]
				if !allowed {
					continue
				}
				seen[key] = true
				result.URL = source.URL
				result.Title = UsableGrokWebSearchTitle(result.Title, result.URL)
				if result.Title == "" {
					result.Title = source.Title
				}
				result.Snippet = strings.TrimSpace(result.Snippet)
				if result.Snippet == "" {
					result.Snippet = source.Snippet
				}
				out = append(out, result)
				if len(out) >= maxResults {
					break
				}
			}
			return true
		})
		return len(out) < maxResults
	})

	// Observed sources absent from structured output remain usable. Append them
	// in discovery order after enriched results so partial assistant JSON does not lose evidence.
	for _, key := range sourceOrder {
		if len(out) >= maxResults {
			break
		}
		if seen[key] {
			continue
		}
		result := sources[key]
		// A source can lack a useful title after numeric-title filtering. Derive its
		// hostname so this retained evidence still has a display label for search callers.
		if result.Title == "" {
			result.Title = GrokWebSearchTitleFromURL(result.URL)
		}
		seen[key] = true
		out = append(out, result)
	}
	return out
}

// ParseGrokWebSearchStructuredResults decodes the text between its first
// opening and last closing brace as a results object. Missing braces or
// invalid JSON returns nil; URLs and display fields are validated by the
// later source extraction step.
func ParseGrokWebSearchStructuredResults(text string) []StandaloneSearchResult {
	text = strings.TrimSpace(text)
	start := strings.IndexByte(text, '{')
	end := strings.LastIndexByte(text, '}')
	if start < 0 || end < start {
		return nil
	}
	var payload struct {
		Results []StandaloneSearchResult `json:"results"` // decoded structured search results
	}
	// Unusable assistant JSON supplies no enriched results. Source extraction
	// can still append observed URLs in discovery order after this empty result.
	if err := json.Unmarshal([]byte(text[start:end+1]), &payload); err != nil {
		return nil
	}
	return payload.Results
}

// NormalizeGrokWebSearchURL returns a matching key for an HTTP or HTTPS URL
// with a nonempty host. It lowercases scheme and host, drops fragments, and
// adds a slash to empty paths while retaining query and userinfo.
//
// The key groups source evidence and assistant result spelling; it does not
// authorize outbound fetching or normalize every equivalent URL
// representation. Paths, ports, and query values remain part of identity, so
// callers use the same function on both evidence and candidate results before
// comparison.
func NormalizeGrokWebSearchURL(rawURL string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	// Fragments distinguish positions within a page rather than the source page
	// itself, so evidence matching omits them while retaining the original returned URL.
	u.Fragment = ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), true
}

// UsableGrokWebSearchTitle returns a trimmed title unless blank, equal to
// rawURL, or parseable as an integer. Rejected values return empty so source
// extraction can try observed title text or a hostname fallback.
func UsableGrokWebSearchTitle(title, rawURL string) string {
	title = strings.TrimSpace(title)
	if title == "" || title == rawURL {
		return ""
	}
	if _, err := strconv.Atoi(title); err == nil {
		return ""
	}
	return title
}

// GrokWebSearchTitleFromURL derives a lowercased host display title with a
// leading www. removed. Invalid or hostless URLs return rawURL as supplied;
// callers validate source URLs before using this display fallback.
func GrokWebSearchTitleFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	return strings.TrimPrefix(strings.ToLower(u.Host), "www.")
}

// BuildGrokXSearchResponsesBody builds a nonstreaming Responses request
// requiring x_search and source inclusion. A trimmed Query wins before Input,
// and optional filters are sent when supplied, including false pointer
// values. Model and result-limit defaults use the shared text/search rules.
//
// Handle slices and date text are serialized as provided apart from trimming
// dates; this builder does not validate their vendor syntax. MaxResults
// shapes the prompt rather than a native tool count parameter. JSON marshal
// errors propagate, and store is set false in the encoded request.
func BuildGrokXSearchResponsesBody(req StandaloneSearchRequest, model string) ([]byte, error) {
	input := strings.TrimSpace(req.Query)
	if input == "" {
		input = strings.TrimSpace(req.Input)
	}
	tool := map[string]any{"type": "x_search"}
	if len(req.AllowedXHandles) > 0 {
		tool["allowed_x_handles"] = req.AllowedXHandles
	}
	if len(req.ExcludedXHandles) > 0 {
		tool["excluded_x_handles"] = req.ExcludedXHandles
	}
	if strings.TrimSpace(req.FromDate) != "" {
		tool["from_date"] = strings.TrimSpace(req.FromDate)
	}
	if strings.TrimSpace(req.ToDate) != "" {
		tool["to_date"] = strings.TrimSpace(req.ToDate)
	}
	if req.EnableImageUnderstanding != nil {
		tool["enable_image_understanding"] = *req.EnableImageUnderstanding
	}
	if req.EnableVideoUnderstanding != nil {
		tool["enable_video_understanding"] = *req.EnableVideoUnderstanding
	}
	maxResults := 0
	if req.MaxResults != nil {
		maxResults = *req.MaxResults
	}
	return json.Marshal(map[string]any{
		"model":       ResolveDefaultTextModel(model),
		"input":       BuildGrokXSearchPrompt(input, maxResults),
		"tools":       []map[string]any{tool},
		"tool_choice": "required",
		"include":     []string{"x_search_call.action.sources"},
		"store":       false,
		"stream":      false,
	})
}

// BuildGrokXSearchPrompt formats an X-search instruction with the normalized
// result limit and supplied query. The query is retained as given, and
// callers use observed source evidence to validate the assistant's requested
// JSON results.
func BuildGrokXSearchPrompt(query string, maxResults int) string {
	return fmt.Sprintf(`Search X for the user query below. Return ONLY valid JSON with this exact shape: {"results":[{"url":"https://...","title":"post or page title","snippet":"concise factual summary"}]}. Return at most %d unique results. Every URL must be an actual x_search source. Populate a non-empty title and snippet for every result. Do not wrap the JSON in markdown.

User query:
%s`, NormalizeGrokWebSearchMaxResults(maxResults), query)
}

// BuildGrokWebSearchResponsesBody encodes a nonstreaming web_search request
// with source inclusion and store=false. Model and result-limit defaults use
// shared resolvers; it leaves tool_choice absent and returns the encoded body
// for validated forwarding.
func BuildGrokWebSearchResponsesBody(query string, maxResults int, model string) []byte {
	// The constructed map contains JSON-encodable strings, booleans, and tool
	// objects; this convenience builder returns the resulting bytes without exposing an error result.
	body, _ := json.Marshal(map[string]any{"model": ResolveDefaultTextModel(model), "input": BuildGrokWebSearchPrompt(query, maxResults), "tools": []map[string]any{{"type": "web_search"}}, "include": []string{"web_search_call.action.sources"}, "store": false, "stream": false})
	return body
}
