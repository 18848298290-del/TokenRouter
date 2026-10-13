package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/searchtools"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// TestBuildGrokXSearchResponsesBody supplies X handle/date filters and
// image/video understanding options. Encoding retains those values, requests
// required x_search sources, uses the selected model, and disables
// stream/store.
func TestBuildGrokXSearchResponsesBody(t *testing.T) {
	t.Parallel()
	understandImages := true
	understandVideos := false
	body, err := buildStandaloneXSearchForTest(searchtools.StandaloneRequest{
		Query:                    "latest posts from xAI",
		AllowedXHandles:          []string{"xai"},
		ExcludedXHandles:         []string{"spam"},
		FromDate:                 "2026-08-01",
		ToDate:                   "2026-08-10",
		EnableImageUnderstanding: &understandImages,
		EnableVideoUnderstanding: &understandVideos,
	}, xai.DefaultTextModel)
	require.NoError(t, err)
	require.Equal(t, xai.DefaultTextModel, gjson.GetBytes(body, "model").String())
	require.Contains(t, gjson.GetBytes(body, "input").String(), "latest posts from xAI")
	require.Contains(t, gjson.GetBytes(body, "input").String(), "Return ONLY valid JSON")
	require.Equal(t, "x_search_call.action.sources", gjson.GetBytes(body, "include.0").String())
	require.Equal(t, "required", gjson.GetBytes(body, "tool_choice").String())
	require.Equal(t, "x_search", gjson.GetBytes(body, "tools.0.type").String())
	require.Equal(t, "xai", gjson.GetBytes(body, "tools.0.allowed_x_handles.0").String())
	require.Equal(t, "spam", gjson.GetBytes(body, "tools.0.excluded_x_handles.0").String())
	require.Equal(t, "2026-08-01", gjson.GetBytes(body, "tools.0.from_date").String())
	require.Equal(t, "2026-08-10", gjson.GetBytes(body, "tools.0.to_date").String())
	require.True(t, gjson.GetBytes(body, "tools.0.enable_image_understanding").Bool())
	require.False(t, gjson.GetBytes(body, "tools.0.enable_video_understanding").Bool())
	require.False(t, gjson.GetBytes(body, "store").Bool())
	require.False(t, gjson.GetBytes(body, "stream").Bool())
}

// TestBuildGrokXSearchResponsesBodyAcceptsInputAlias uses the Input alias with
// no Query for X search. The resulting Responses input contains the supplied
// search text.
func TestBuildGrokXSearchResponsesBodyAcceptsInputAlias(t *testing.T) {
	t.Parallel()
	body, err := buildStandaloneXSearchForTest(searchtools.StandaloneRequest{Input: "latest posts from xAI"}, xai.DefaultTextModel)
	require.NoError(t, err)
	require.Contains(t, gjson.GetBytes(body, "input").String(), "latest posts from xAI")
}

// TestResolveGrokStandaloneSearchModelUsesRuntimeDefault temporarily changes
// the upstream runtime default and builds an X-search body from its resolved
// model. The new model appears in both resolution and encoded request before
// cleanup restores prior state.
func TestResolveGrokStandaloneSearchModelUsesRuntimeDefault(t *testing.T) {
	original := xai.RuntimeDefaultTextModel()
	// Restore process model configuration after this scenario so later search tests
	// resolve their own default rather than retaining this temporary runtime override.
	t.Cleanup(func() { xai.SetRuntimeDefaultTextModel(original) })
	xai.SetRuntimeDefaultTextModel("grok-4.6")

	model := GrokStandaloneSearchModel()
	body, err := buildStandaloneXSearchForTest(searchtools.StandaloneRequest{Query: "latest posts from xAI"}, model)
	require.NoError(t, err)
	require.Equal(t, "grok-4.6", model)
	require.Equal(t, model, gjson.GetBytes(body, "model").String())
}

// buildStandaloneXSearchForTest delegates a standalone request to the X-search
// body encoder.
func buildStandaloneXSearchForTest(r searchtools.StandaloneRequest, m string) ([]byte, error) {
	return GrokStandaloneSearchBody(r, m, 0, true)
}
