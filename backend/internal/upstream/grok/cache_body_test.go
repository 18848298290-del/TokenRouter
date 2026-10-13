package grok

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestAppendMissingGrokFreeCacheNativeTools_PureClientFunctionNoInject
// supplies a client-only function with automatic choice and no client opt-in.
// It asserts neither native search tool is added to that local-tool request.
func TestAppendMissingGrokFreeCacheNativeTools_PureClientFunctionNoInject(t *testing.T) {
	body := []byte(`{
		"model": "grok-4.5",
		"tools": [
			{"type":"function","name":"view_image","description":"View image","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}
		],
		"tool_choice": "auto"
	}`)

	result, err := AppendGrokFreeCacheNativeTools(body, false)
	require.NoError(t, err)

	tools := gjson.GetBytes(result, "tools").Array()
	for _, tool := range tools {
		toolType := tool.Get("type").String()
		assert.NotEqual(t, "web_search", toolType, "should not inject web_search for pure client functions")
		assert.NotEqual(t, "x_search", toolType, "should not inject x_search for pure client functions")
	}
}

// TestAppendMissingGrokFreeCacheNativeTools_FunctionPlusWebSearchInjects
// combines a client function with a search function. It asserts search-
// function conversion produces native web_search and adds the x_search
// companion when client-only injection is disabled.
func TestAppendMissingGrokFreeCacheNativeTools_FunctionPlusWebSearchInjects(t *testing.T) {
	body := []byte(`{
		"model": "grok-4.5",
		"tools": [
			{"type":"function","name":"view_image","description":"View","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}},
			{"type":"function","name":"web_search","description":"Search","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}
		]
	}`)

	result, err := AppendGrokFreeCacheNativeTools(body, false)
	require.NoError(t, err)

	tools := gjson.GetBytes(result, "tools").Array()
	types := make(map[string]bool)
	for _, tool := range tools {
		types[tool.Get("type").String()] = true
	}
	assert.True(t, types["web_search"], "web_search should be present (converted from function)")
	assert.True(t, types["x_search"], "x_search should be injected when web_search is present alongside client functions")
}

// TestAppendMissingGrokFreeCacheNativeTools_NativeSearchAlreadyPresent
// combines a client function with existing native web_search. It asserts that
// web search remains present and the missing X-search companion is added.
func TestAppendMissingGrokFreeCacheNativeTools_NativeSearchAlreadyPresent(t *testing.T) {
	body := []byte(`{
		"model": "grok-4.5",
		"tools": [
			{"type":"function","name":"view_image","description":"View","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}},
			{"type":"web_search"}
		]
	}`)

	result, err := AppendGrokFreeCacheNativeTools(body, false)
	require.NoError(t, err)

	tools := gjson.GetBytes(result, "tools").Array()
	types := make(map[string]bool)
	for _, tool := range tools {
		types[tool.Get("type").String()] = true
	}
	assert.True(t, types["web_search"])
	assert.True(t, types["x_search"], "x_search should be injected when web_search is already present")
}

// TestAppendMissingGrokFreeCacheNativeTools_MultipleFunctionsNoSearch
// supplies two client functions with no search declaration or opt-in. It
// asserts the result retains two tools, preventing automatic addition of
// search companions.
func TestAppendMissingGrokFreeCacheNativeTools_MultipleFunctionsNoSearch(t *testing.T) {
	body := []byte(`{
		"model": "grok-4.5",
		"tools": [
			{"type":"function","name":"view_image","description":"View","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}},
			{"type":"function","name":"read_file","description":"Read","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}
		]
	}`)

	result, err := AppendGrokFreeCacheNativeTools(body, false)
	require.NoError(t, err)

	tools := gjson.GetBytes(result, "tools").Array()
	require.Len(t, tools, 2, "no tools should be injected for pure client functions")
}
