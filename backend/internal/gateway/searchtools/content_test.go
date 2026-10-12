package searchtools

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/search/contract"
)

// TestIsOnlyWebSearchToolInBody_WebSearchType supplies one canonical
// web_search type declaration. The shape predicate must accept it as a search
// candidate before global/provider policy is evaluated elsewhere.
func TestIsOnlyWebSearchToolInBody_WebSearchType(t *testing.T) {
	require.True(t, IsOnlyWebSearchToolInBody([]byte(`{"tools":[{"type":"web_search"}]}`)))
}

// TestIsOnlyWebSearchToolInBody_WebSearch2025Type supplies a dated web_search
// type spelling. The prefix matcher must accept that one-tool request as
// search declaration evidence for its later policy consumer.
func TestIsOnlyWebSearchToolInBody_WebSearch2025Type(t *testing.T) {
	require.True(t, IsOnlyWebSearchToolInBody([]byte(`{"tools":[{"type":"web_search_20250305"}]}`)))
}

// TestIsOnlyWebSearchToolInBody_GoogleSearchType supplies one exact
// google_search declaration. The shape predicate must recognize that supported
// type rather than require the web_search prefix spelling.
func TestIsOnlyWebSearchToolInBody_GoogleSearchType(t *testing.T) {
	require.True(t, IsOnlyWebSearchToolInBody([]byte(`{"tools":[{"type":"google_search"}]}`)))
}

// TestIsOnlyWebSearchToolInBody_NameWebSearch supplies one name-only
// web_search declaration. The predicate must accept the supported exact name
// even when no tool type field was supplied.
func TestIsOnlyWebSearchToolInBody_NameWebSearch(t *testing.T) {
	require.True(t, IsOnlyWebSearchToolInBody([]byte(`{"tools":[{"name":"web_search"}]}`)))
}

// TestIsOnlyWebSearchToolInBody_NameWebSearch2025 supplies the supported dated
// name without a type. The predicate must recognize that exact compatibility
// name in a single-tool request.
func TestIsOnlyWebSearchToolInBody_NameWebSearch2025(t *testing.T) {
	require.True(t, IsOnlyWebSearchToolInBody([]byte(`{"tools":[{"name":"web_search_20250305"}]}`)))
}

// TestIsOnlyWebSearchToolInBody_NameGoogleSearch supplies one name-only
// google_search declaration. Shape recognition must accept the supported exact
// name before the owning emulator applies feature and manager prerequisites.
func TestIsOnlyWebSearchToolInBody_NameGoogleSearch(t *testing.T) {
	require.True(t, IsOnlyWebSearchToolInBody([]byte(`{"tools":[{"name":"google_search"}]}`)))
}

// TestIsOnlyWebSearchToolInBody_MultipleTools supplies search and text-editor
// declarations together. The shape gate must decline emulation because its
// one-tool requirement is not met by this mixed request.
func TestIsOnlyWebSearchToolInBody_MultipleTools(t *testing.T) {
	require.False(t, IsOnlyWebSearchToolInBody(
		[]byte(`{"tools":[{"type":"web_search"},{"type":"text_editor"}]}`)))
}

// TestIsOnlyWebSearchToolInBody_NoTools supplies a model-only request with no
// tools member. The shape gate must return false for unavailable search
// declaration evidence before any external query.
func TestIsOnlyWebSearchToolInBody_NoTools(t *testing.T) {
	require.False(t, IsOnlyWebSearchToolInBody([]byte(`{"model":"claude-3"}`)))
}

// TestIsOnlyWebSearchToolInBody_EmptyToolsArray supplies a present empty
// declaration list. The predicate must return false because presence alone
// cannot establish the required single recognized search tool.
func TestIsOnlyWebSearchToolInBody_EmptyToolsArray(t *testing.T) {
	require.False(t, IsOnlyWebSearchToolInBody([]byte(`{"tools":[]}`)))
}

// TestIsOnlyWebSearchToolInBody_NonWebSearchTool supplies one text-editor
// declaration. The predicate must decline that one-tool shape because its
// type/name supplies no recognized search evidence.
func TestIsOnlyWebSearchToolInBody_NonWebSearchTool(t *testing.T) {
	require.False(t, IsOnlyWebSearchToolInBody([]byte(`{"tools":[{"type":"text_editor"}]}`)))
}

// TestIsOnlyWebSearchToolInBody_ToolsNotArray supplies a search string instead
// of a declaration array. Shape recognition must return false before count or
// type recognition can qualify a tool.
func TestIsOnlyWebSearchToolInBody_ToolsNotArray(t *testing.T) {
	require.False(t, IsOnlyWebSearchToolInBody([]byte(`{"tools":"web_search"}`)))
}

// TestExtractSearchQueryFromBody_StringContent supplies a final user message
// with string content. Query extraction must return that text spelling for the
// external manager without selecting another message.
func TestExtractSearchQueryFromBody_StringContent(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"what is golang"}]}`
	require.Equal(t, "what is golang", ExtractSearchQueryFromBody([]byte(body)))
}

// TestExtractSearchQueryFromBody_ArrayContent supplies a user content array
// containing one text block. Query extraction must return its text for search
// rather than the array's raw JSON representation.
func TestExtractSearchQueryFromBody_ArrayContent(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"search this"}]}]}`
	require.Equal(t, "search this", ExtractSearchQueryFromBody([]byte(body)))
}

// TestExtractSearchQueryFromBody_MultipleMessages supplies earlier
// conversation and a final user turn. Extraction must choose the final turn's
// text instead of reusing the earlier user query.
func TestExtractSearchQueryFromBody_MultipleMessages(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"ok"},{"role":"user","content":"second"}]}`
	require.Equal(t, "second", ExtractSearchQueryFromBody([]byte(body)))
}

// TestExtractSearchQueryFromBody_LastMessageNotUser places an assistant answer
// after a user query. Extraction must return empty because earlier user text
// cannot substitute for an unsuitable final message.
func TestExtractSearchQueryFromBody_LastMessageNotUser(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"q"},{"role":"assistant","content":"a"}]}`
	require.Equal(t, "", ExtractSearchQueryFromBody([]byte(body)))
}

// TestExtractSearchQueryFromBody_EmptyMessages supplies an empty conversation
// array. Extraction must return empty rather than inventing a query when there
// is no final message to inspect.
func TestExtractSearchQueryFromBody_EmptyMessages(t *testing.T) {
	require.Equal(t, "", ExtractSearchQueryFromBody([]byte(`{"messages":[]}`)))
}

// TestExtractSearchQueryFromBody_NoMessages supplies a model-only body with no
// conversation array. Query extraction must return empty, leaving the owning
// emulator to produce its missing-query error.
func TestExtractSearchQueryFromBody_NoMessages(t *testing.T) {
	require.Equal(t, "", ExtractSearchQueryFromBody([]byte(`{"model":"claude-3"}`)))
}

// TestExtractSearchQueryFromBody_ArrayContentSkipsEmptyText supplies image and
// empty text blocks before useful text. Extraction must choose the first
// nonempty text block and retain its query spelling.
func TestExtractSearchQueryFromBody_ArrayContentSkipsEmptyText(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"image"},{"type":"text","text":""},{"type":"text","text":"real query"}]}]}`
	require.Equal(t, "real query", ExtractSearchQueryFromBody([]byte(body)))
}

// TestExtractSearchQueryFromBody_ArrayContentNoTextBlock supplies only an
// image content block. Extraction must return empty because this search path
// obtains its query from supported text representation.
func TestExtractSearchQueryFromBody_ArrayContentNoTextBlock(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"image","source":{}}]}]}`
	require.Equal(t, "", ExtractSearchQueryFromBody([]byte(body)))
}

// TestBuildSearchResultBlocks_WithResults supplies ordered results with
// optional snippet and age values. Conversion must retain URLs/content and
// include age only for the result that provided it.
func TestBuildSearchResultBlocks_WithResults(t *testing.T) {
	results := []contract.SearchResult{
		{URL: "https://a.com", Title: "A", Snippet: "snippet a", PageAge: "2 days"},
		{URL: "https://b.com", Title: "B", Snippet: "snippet b"},
	}
	blocks := BuildSearchResultBlocks(results)
	require.Len(t, blocks, 2)
	require.Equal(t, "web_search_result", blocks[0]["type"])
	require.Equal(t, "https://a.com", blocks[0]["url"])
	require.Equal(t, "snippet a", blocks[0]["page_content"])
	require.Equal(t, "2 days", blocks[0]["page_age"])

	require.Equal(t, "https://b.com", blocks[1]["url"])
	_, hasPageAge := blocks[1]["page_age"]
	require.False(t, hasPageAge)
}

// TestBuildSearchResultBlocks_Empty supplies no external results to block
// conversion. The produced list must be empty so synthetic output can
// represent a search with no result entries.
func TestBuildSearchResultBlocks_Empty(t *testing.T) {
	blocks := BuildSearchResultBlocks(nil)
	require.Empty(t, blocks)
}

// TestBuildSearchResultBlocks_SnippetEmpty supplies a result whose snippet is
// empty. Its block must omit page_content when that optional text is
// unavailable to synthetic output; other result fields are outside this assertion.
func TestBuildSearchResultBlocks_SnippetEmpty(t *testing.T) {
	blocks := BuildSearchResultBlocks([]contract.SearchResult{{URL: "https://x.com", Title: "X", Snippet: ""}})
	_, hasContent := blocks[0]["page_content"]
	require.False(t, hasContent)
}

// TestBuildTextSummary_WithResults formats one titled result for a query. The
// summary must include query, numbered title and URL, protecting the readable
// content used by synthetic output.
func TestBuildTextSummary_WithResults(t *testing.T) {
	results := []contract.SearchResult{
		{URL: "https://a.com", Title: "A", Snippet: "desc a"},
	}
	summary := BuildTextSummary("test query", results)
	require.Contains(t, summary, "test query")
	require.Contains(t, summary, "1. **A**")
	require.Contains(t, summary, "https://a.com")
}

// TestBuildTextSummary_NoResults formats a query with no result entries. The
// summary must retain that query in its no-results text for the client's
// synthetic response.
func TestBuildTextSummary_NoResults(t *testing.T) {
	summary := BuildTextSummary("test", nil)
	require.Contains(t, summary, "No search results found for: test")
}
