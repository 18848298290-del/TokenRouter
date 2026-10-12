package creative_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/creative"
	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
)

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

// TestNormalizeCreativeModelSettingsForSaveByPlatform checks resolved capabilities and retains unresolved entries.
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
		{GroupID: 12, Model: "gemini-only-inpaint", Operations: []string{creative.CreativeOperationInpaint}},
		{GroupID: 999, Model: "legacy", Operations: []string{creative.CreativeOperationInpaint}},
	})
	require.NoError(t, err)
	require.Equal(t, []creative.CreativeModelSetting{
		{GroupID: 12, Model: "gemini-3.1-flash-image", Operations: []string{creative.CreativeOperationGenerate}},
		{GroupID: 13, Model: "gpt-image-2", Operations: []string{creative.CreativeOperationGenerate, creative.CreativeOperationInpaint}},
		{GroupID: 12, Model: "gemini-only-inpaint", Operations: []string{creative.CreativeOperationInpaint}},
		{GroupID: 999, Model: "legacy", Operations: []string{creative.CreativeOperationInpaint}},
	}, got)
}
