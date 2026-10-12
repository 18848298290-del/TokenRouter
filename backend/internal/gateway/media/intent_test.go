package media

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// imagePolicyFixture binds narrow image type/name matching and scripted map
// qualification. HasTool always returns true and ToolChoice false, so map
// tests isolate delegation from real tool parsing; FirstString returns the
// first nonblank supplied string.
func imagePolicyFixture() ImageIntentPolicy {
	return NewImageIntentPolicy(ImageToolRules{
		IsImageType:     func(s string) bool { return strings.TrimSpace(s) == "image_generation" },
		IsNamespaceName: func(s string) bool { return strings.TrimSpace(s) == "image_gen" },
		HasTool:         func(map[string]any) bool { return true },
		ToolChoice:      func(any) bool { return false },
		FirstString: func(values ...any) string {
			for _, v := range values {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					return s
				}
			}
			return ""
		},
	})
}

// TestImagePolicyExplicitModeAndDuplicateFields compares passive namespace
// declarations with explicit mode, then supplies duplicate model/tools
// members. It checks first-member precedence and explicit imagegen
// function-choice recognition on valid Responses JSON.
func TestImagePolicyExplicitModeAndDuplicateFields(t *testing.T) {
	p := imagePolicyFixture()
	passive := []byte(`{"model":"text","tools":[{"type":"namespace","name":"image_gen"}]}`)
	require.True(t, p.IsImageGenerationIntentForPlatform("/v1/responses", "text", passive, false))
	require.False(t, p.IsImageGenerationIntentForPlatform("/v1/responses", "text", passive, true))
	// An empty tools member or null model appears before a qualifying
	// duplicate, protecting first-member handling rather than accepting a
	// later conflicting declaration.
	for _, body := range []string{`{"tools":[],"tools":[{"type":"image_generation"}]}`, `{"model":null,"model":"gpt-image-2"}`} {
		require.False(t, p.IsImageGenerationIntent("/v1/responses", "text", []byte(body)))
	}
	require.True(t, p.IsExplicitImageGenerationIntent("/v1/responses", "text", []byte(`{"tool_choice":{"type":"function","namespace":"image_gen","name":"imagegen"}}`)))
}

// TestImagePolicyUsesExistingModelAndSizeRules supplies GPT and Grok image
// models, a normalized edit URL and image billing hints. It checks
// native-tool default model, selected size evidence and retention of
// unknown input-size spelling.
func TestImagePolicyUsesExistingModelAndSizeRules(t *testing.T) {
	p := imagePolicyFixture()
	for _, model := range []string{"gpt-image-2", "grok-imagine"} {
		require.True(t, p.IsImageGenerationIntent("/v1/responses", model, nil))
	}
	require.True(t, p.IsImageGenerationEndpoint("https://api.openai.com/v1/images/edits/?x=1"))
	cfg, err := p.ResolveOpenAIResponsesImageBillingConfigDetailedFromBody([]byte(`{"model":"text","tools":[{"type":"image_generation","size":"1536x1024"}]}`), "fallback")
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", cfg.Model)
	require.Equal(t, NormalizeImageSizeTier("1536x1024"), cfg.SizeTier)
	require.Equal(t, "1536x1024", cfg.InputSize)
	cfg, err = p.ResolveOpenAIResponsesImageBillingConfigDetailed(map[string]any{"model": "custom-image", "size": "future-size"}, "fallback")
	require.NoError(t, err)
	require.Equal(t, "custom-image", cfg.Model)
	require.Equal(t, "future-size", cfg.InputSize)
}

// TestImagePolicyDelegatesMapToolQualification uses a fixture whose
// ordinary map tool predicate always succeeds. It checks ordinary versus
// explicit empty-map results and the no-group/present-group permission
// decisions.
func TestImagePolicyDelegatesMapToolQualification(t *testing.T) {
	p := imagePolicyFixture()
	require.True(t, p.IsImageGenerationIntentMap("/v1/responses", "text", map[string]any{}))
	require.False(t, p.IsExplicitImageGenerationIntentMap("/v1/responses", "text", map[string]any{}))
	require.True(t, GroupImagePermission(false, false))
	require.False(t, GroupImagePermission(true, false))
	require.True(t, GroupImagePermission(true, true))
}
