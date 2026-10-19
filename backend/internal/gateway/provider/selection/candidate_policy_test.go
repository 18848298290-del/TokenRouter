package selection

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog/testkit"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestMixedGroupImageCandidateUsesFinalMappedModel maps an image alias through
// a wildcard provider scope. A text target is rejected and an image-generation
// target passes, protecting final-model checks during image selection.
func TestMixedGroupImageCandidateUsesFinalMappedModel(t *testing.T) {
	selector := NewCompatible(CompatibleDependencies{}, DefaultOptions())
	value := mixedGroupProvider(1, capability.PlatformOpenAI, "*", 91)
	value.Record.Credentials["model_mapping"] = map[string]any{"image-alias": "gpt-test"}
	ctx := context.WithValue(routing.WithModelAttributes(context.Background(), &routing.ModelAttributeService{Catalog: routing.ModelAttributeCatalog{View: testkit.OfflineView}}), imageModelRequiredKey{}, &imageSelectionState{})
	require.Equal(t, "MODEL_CAPABILITY_UNKNOWN", selector.candidateEligibilityReason(ctx, &value, "", "image-alias", false, ""))
	value.Record.Credentials["model_mapping"] = map[string]any{"image-alias": "gpt-image-1"}
	require.Empty(t, selector.candidateEligibilityReason(ctx, &value, "", "image-alias", false, ""))
}
