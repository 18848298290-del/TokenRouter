package settings_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/creative"
	"github.com/TokenFlux/TokenRouter/internal/settings/composite"
	settingskit "github.com/TokenFlux/TokenRouter/internal/settings/testkit"
)

// Creative model configuration serializes allowed operations in canonical order
// before it reaches the settings write batch. The preparation case decodes that
// batch's JSON for one image model, checks the sorted operation list, and
// rejects a model entry lacking required operations.

// TestBuildSystemSettingsUpdatesCreativeModelSettings prepares a creative model
// with operations supplied in reverse order. Its update JSON records canonical
// generate and inpaint order, and missing operations fail preparation.
func TestBuildSystemSettingsUpdatesCreativeModelSettings(t *testing.T) {
	svc := settingskit.NewComposite(&settingUpdateRepoStub{}, nil)
	updates, err := composite.Prepare(context.Background(), &composite.Snapshot{
		CreativeModelSettings: []creative.CreativeModelSetting{{
			GroupID:    3,
			Model:      "gpt-image-2",
			Operations: []string{creative.CreativeOperationInpaint, creative.CreativeOperationGenerate},
		}},
	}, svc.Prepare)
	require.NoError(t, err)
	var persisted []creative.CreativeModelSetting
	require.NoError(t, json.Unmarshal([]byte(updates[creative.SettingKeyCreativeModelSettings]), &persisted))
	require.Equal(t, []creative.CreativeModelSetting{{
		GroupID:    3,
		Model:      "gpt-image-2",
		Operations: []string{creative.CreativeOperationGenerate, creative.CreativeOperationInpaint},
	}}, persisted)

	_, err = composite.Prepare(context.Background(), &composite.Snapshot{
		CreativeModelSettings: []creative.CreativeModelSetting{{GroupID: 3, Model: "gpt-image-2"}},
	}, svc.Prepare)
	require.Error(t, err)
}
