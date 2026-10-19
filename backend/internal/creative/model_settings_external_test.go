package creative_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/creative"
	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
)

// Configured operations can be narrower than platform support, and saved
// entries can outlive their current routes. These fixtures compare the
// resulting operation lists for known and unresolved models, including a group
// that the test reader cannot find.

// TestCreativeOperationsForModelIntersectsPlatformSupport configures
// generation and editing but supplies generation support alone. The matching
// group returns generation with configured true, while another group returns
// no configured entry.
func TestCreativeOperationsForModelIntersectsPlatformSupport(t *testing.T) {
	index := creative.CreativeModelSettingsIndex([]creative.CreativeModelSetting{{
		GroupID:    9,
		Model:      "grok-imagine",
		Operations: []string{"generate", "edit"},
	}})
	operations, configured := creative.CreativeOperationsForModel(index, 9, "grok-imagine", []string{creative.CreativeOperationGenerate})
	require.True(t, configured)
	require.Equal(t, []string{creative.CreativeOperationGenerate}, operations)

	operations, configured = creative.CreativeOperationsForModel(index, 10, "grok-imagine", []string{creative.CreativeOperationGenerate})
	require.False(t, configured)
	require.Empty(t, operations)
}

// TestNormalizeCreativeModelSettingsForSaveByPlatform saves Gemini, OpenAI,
// and unresolved entries through the fixture service. Known Gemini capability
// removes inpaint, OpenAI retains it, and unresolved model or group entries
// remain.
func TestNormalizeCreativeModelSettingsForSaveByPlatform(t *testing.T) {
	svc := newCreativeTestService()
	groupRepo := testassert.MustType[*creativeFakeGroupRepo](testassert.MustType[creativeGroupReader](svc.GroupRepo).source)
	openai := newCreativeTestGroup()
	openai.ID = 13
	openai.Name = "OpenAI Image"
	groupRepo.byID[13] = openai

	got, err := svc.NormalizeCreativeModelSettingsForSave(context.Background(), []creative.CreativeModelSetting{
		{GroupID: 12, Model: "gemini-3.1-flash-image", Operations: []string{creative.CreativeOperationGenerate, creative.CreativeOperationInpaint}},
		{GroupID: 13, Model: "gpt-image-2", Operations: []string{creative.CreativeOperationGenerate, creative.CreativeOperationInpaint}},
		{GroupID: 999, Model: "legacy", Operations: []string{creative.CreativeOperationInpaint}},
	})
	require.NoError(t, err)
	require.Equal(t, []creative.CreativeModelSetting{
		{GroupID: 12, Model: "gemini-3.1-flash-image", Operations: []string{creative.CreativeOperationGenerate}},
		{GroupID: 13, Model: "gpt-image-2", Operations: []string{creative.CreativeOperationGenerate, creative.CreativeOperationInpaint}},
		{GroupID: 999, Model: "legacy", Operations: []string{creative.CreativeOperationInpaint}},
	}, got)
	_, err = svc.NormalizeCreativeModelSettingsForSave(context.Background(), []creative.CreativeModelSetting{{GroupID: 12, Model: "gemini-only-inpaint", Operations: []string{creative.CreativeOperationGenerate}}})
	require.ErrorContains(t, err, "MODEL_CAPABILITY_UNKNOWN")
}
