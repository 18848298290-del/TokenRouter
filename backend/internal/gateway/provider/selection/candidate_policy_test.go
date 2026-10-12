package selection

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestMixedGroupImageCandidateUsesFinalMappedModel checks that image aliases resolve to image models even with a wildcard allowlist.
func TestMixedGroupImageCandidateUsesFinalMappedModel(t *testing.T) {
	selector := NewCompatible(CompatibleDependencies{}, DefaultOptions())
	value := mixedGroupProvider(1, capability.PlatformOpenAI, "*", 91)
	value.Record.Credentials["model_mapping"] = map[string]any{"image-alias": "gpt-test"}
	ctx := context.WithValue(context.Background(), imageModelRequiredKey{}, true)
	require.Equal(t, "image_model_required", selector.candidateEligibilityReason(ctx, &value, "", "image-alias", false, ""))
	value.Record.Credentials["model_mapping"] = map[string]any{"image-alias": "gpt-image-1"}
	require.Empty(t, selector.candidateEligibilityReason(ctx, &value, "", "image-alias", false, ""))
}
