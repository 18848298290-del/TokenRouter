package creative

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// patternCreativeSettings supplies wildcard model names from saved settings.
type patternCreativeSettings struct {
	*targetQueryFixture
	model string
}

func (s patternCreativeSettings) GetCreativeModelSettings(context.Context) []CreativeModelSetting {
	return []CreativeModelSetting{{GroupID: 1, Model: s.model, Operations: []string{CreativeOperationGenerate}}}
}

// TestCreativeRejectsPatternModels checks that listing and submission reject wildcard model names.
func TestCreativeRejectsPatternModels(t *testing.T) {
	for _, model := range []string{"gpt-image-2*", "gpt-image-2?"} {
		t.Run(model, func(t *testing.T) {
			fixture := &targetQueryFixture{}
			service := newTargetQueryPublic(fixture)
			service.Settings = patternCreativeSettings{targetQueryFixture: fixture, model: model}
			models, err := service.ListModels(context.Background(), 1)
			require.NoError(t, err)
			require.Empty(t, models.Data)
			_, err = service.ValidateCreateParams(context.Background(), 1, &CreateCreativeRunParamsPublic{GroupID: 1, Model: model, Operation: CreativeOperationGenerate, Prompt: "test"})
			require.ErrorIs(t, err, ErrCreativeInvalidModel)
			require.Empty(t, fixture.checked)
			require.Zero(t, fixture.inventoryReads)
		})
	}
}

// TestCreativeTargetValidationDoesNotEnumerate checks that validating a selected model skips candidate
// enumeration.
func TestCreativeTargetValidationDoesNotEnumerate(t *testing.T) {
	fixture := &targetQueryFixture{}
	service := newTargetQueryPublic(fixture)
	result, err := service.ValidateCreateParams(context.Background(), 1, &CreateCreativeRunParamsPublic{GroupID: 1, Model: "gpt-image-2", Operation: CreativeOperationGenerate, Prompt: "test"})
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", result.FinalModel)
	require.Equal(t, []string{"gpt-image-2"}, fixture.checked)
	require.Zero(t, fixture.inventoryReads)
	require.Equal(t, 1, fixture.providerReads)
}

// TestCreativePublicListChecksConfiguredSettings checks that listing validates the configured model directly.
func TestCreativePublicListChecksConfiguredSettings(t *testing.T) {
	fixture := &targetQueryFixture{}
	result, err := newTargetQueryPublic(fixture).ListModels(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, result.Data, 1)
	require.Equal(t, "gpt-image-2", result.Data[0].Model)
	require.Equal(t, []string{"gpt-image-2"}, fixture.checked)
	require.Zero(t, fixture.inventoryReads)
}
