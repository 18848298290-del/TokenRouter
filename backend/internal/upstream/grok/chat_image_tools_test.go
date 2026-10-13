package grok

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Chat tool removal needs the image in the final user turn and must keep
// explicit or required tool intent representable. These fixtures compare
// non-target requests byte for byte, then inspect removal of all tool controls
// when the inline image makes an automatic tool redundant.

// TestStripRedundantGrokChatViewImageToolLeavesNonTargetRequestsByteExact covers
// a final turn without images, historical images, and named or required
// view_image choices.
// Each request must return identical bytes, protecting the cases where native
// Chat tool intent stays unchanged.
func TestStripRedundantGrokChatViewImageToolLeavesNonTargetRequestsByteExact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string // scenario display label
		body string // supplied Chat JSON
	}{
		{
			name: "current turn has no inline image",
			body: `{"messages":[{"role":"user","content":"Inspect a local image"}],"tools":[{"type":"function","function":{"name":"view_image"}}]}`,
		},
		{
			name: "inline image is only historical",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]},{"role":"assistant","content":"Done"},{"role":"user","content":"Inspect another local image"}],"tools":[{"type":"function","function":{"name":"view_image"}}]}`,
		},
		{
			name: "view image is explicitly selected",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}],"tools":[{"type":"function","function":{"name":"view_image"}}],"tool_choice":{"type":"function","function":{"name":"view_image"}}}`,
		},
		{
			name: "required with view image as the only tool",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}],"tools":[{"type":"function","function":{"name":"view_image"}}],"tool_choice":"required"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(tt.body)
			patched, err := StripRedundantGrokChatViewImageTool(body)
			require.NoError(t, err)
			require.Equal(t, body, patched)
		})
	}
}

// TestStripRedundantGrokChatViewImageToolDropsOnlyToolMetadata supplies an
// inline image with automatic view_image and parallel control. The patched
// request must omit tools, tool_choice and parallel_tool_calls after the last
// redundant declaration disappears.
func TestStripRedundantGrokChatViewImageToolDropsOnlyToolMetadata(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}],
		"tools":[{"type":"function","function":{"name":"view_image"}}],
		"tool_choice":"auto",
		"parallel_tool_calls":true
	}`)

	patched, err := StripRedundantGrokChatViewImageTool(body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(patched, "tools").Exists())
	require.False(t, gjson.GetBytes(patched, "tool_choice").Exists())
	require.False(t, gjson.GetBytes(patched, "parallel_tool_calls").Exists())
}
