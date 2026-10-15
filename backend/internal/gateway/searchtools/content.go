package searchtools

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/search/contract"
)

// content.go recognizes search tool declarations, extracts user queries, and
// formats search results. The final user message supplies its query through a
// string or the first nonempty text block, and results become protocol blocks
// plus a readable text summary for the client.

const (
	// Search tool types and names identify emulation candidates before a provider
	// request is executed; exact names supplement the supported type prefixes.
	toolTypeWebSearchPrefix = "web_search"          // accepted search type prefix
	toolTypeGoogleSearch    = "google_search"       // exact Google search type
	toolNameWebSearch       = "web_search"          // accepted web search tool name
	toolNameGoogleSearch    = "google_search"       // accepted Google search tool name
	toolNameWebSearch2025   = "web_search_20250305" // accepted dated search tool name

	// Synthetic search uses a bounded result request, a model fallback and
	// reserved IDs. Output builders estimate summary tokens for display while
	// returning zero TokenUsage in the structured execution result.
	webSearchDefaultMaxResults = 5                   // default external search result limit
	defaultWebSearchModel      = "claude-sonnet-4-6" // empty-model synthetic response label
	webSearchMsgIDPrefix       = "msg_ws_"           // synthetic message identifier prefix
	ToolUseIDPrefix            = "srvtoolu_ws_"      // synthetic tool identifier prefix
	tokenEstimateDivisor       = 4                   // summary byte-to-token estimate divisor

	// FeatureKey identifies provider mode and pricing feature configuration used
	// by emulation policy; its callers interpret those two configuration sources.
	FeatureKey = "web_search_emulation"
)

// IsOnlyWebSearchToolInBody reports whether tools is an array with exactly one
// recognized search declaration. Missing, empty, nonarray and multi-tool inputs
// return false; full JSON validity is checked by the owning request parser.
func IsOnlyWebSearchToolInBody(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	arr := tools.Array()
	if len(arr) != 1 {
		return false
	}
	return IsWebSearchToolJSON(arr[0])
}

// IsWebSearchToolJSON recognizes case-sensitive web_search type prefixes, exact
// google_search type, or supported exact names. A recognized name can qualify
// independently of type; the caller checks how many tools the request declares.
func IsWebSearchToolJSON(tool gjson.Result) bool {
	toolType := tool.Get("type").String()
	if strings.HasPrefix(toolType, toolTypeWebSearchPrefix) || toolType == toolTypeGoogleSearch {
		return true
	}
	switch tool.Get("name").String() {
	case toolNameWebSearch, toolNameGoogleSearch, toolNameWebSearch2025:
		return true
	}
	return false
}

// ExtractSearchQueryFromBody returns query text from the final exact user-role
// message. Missing/nonarray or empty messages and a different final role return
// empty; earlier user messages do not supply a query when the final entry is
// unsuitable.
func ExtractSearchQueryFromBody(body []byte) string {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return ""
	}
	arr := messages.Array()
	if len(arr) == 0 {
		return ""
	}
	lastMsg := arr[len(arr)-1]
	if lastMsg.Get("role").String() != "user" {
		return ""
	}
	return ExtractWebSearchTextFromContent(lastMsg.Get("content"))
}

// ExtractWebSearchTextFromContent returns string content or the first nonempty
// text block in an array. It keeps the supplied text spelling, including
// whitespace, and returns empty when no supported text representation is found.
func ExtractWebSearchTextFromContent(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		for _, block := range content.Array() {
			if block.Get("type").String() == "text" {
				if text := block.Get("text").String(); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

// BuildSearchResultBlocks converts ordered search results to web_search_result
// string maps. Snippet and PageAge fields are omitted when empty, while an
// empty result slice yields an allocated empty block list.
func BuildSearchResultBlocks(results []contract.SearchResult) []map[string]string {
	blocks := make([]map[string]string, 0, len(results))
	for _, r := range results {
		block := map[string]string{
			"type":  "web_search_result",
			"url":   r.URL,
			"title": r.Title,
		}
		if r.Snippet != "" {
			block["page_content"] = r.Snippet
		}
		if r.PageAge != "" {
			block["page_age"] = r.PageAge
		}
		blocks = append(blocks, block)
	}
	return blocks
}

// BuildTextSummary formats the query and ordered result titles, URLs and
// snippets as readable search text. Empty results produce the no-results
// message; values are inserted as supplied and are not Markdown-escaped by this
// helper.
func BuildTextSummary(query string, results []contract.SearchResult) string {
	if len(results) == 0 {
		return "No search results found for: " + query
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Here are the search results for \"%s\":\n\n", query)
	for i, r := range results {
		fmt.Fprintf(&sb, "%d. **%s**\n   %s\n   %s\n\n", i+1, r.Title, r.URL, r.Snippet)
	}
	return sb.String()
}
