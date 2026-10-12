package creative

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// creativeRuntimeSettingsFixture returns stored values or ErrSettingNotFound for missing keys.
type creativeRuntimeSettingsFixture struct{ values map[string]string }

// TestSettingService_IsCreativeEnabled checks that false disables the feature and a missing key enables it.
func TestSettingService_IsCreativeEnabled(t *testing.T) {
	repo := &creativeRuntimeSettingsFixture{values: map[string]string{SettingKeyCreativeEnabled: "false"}}
	svc := NewRuntimeSettings(repo, settings.ErrSettingNotFound)
	require.False(t, svc.IsCreativeEnabled(context.Background()))

	// A missing key defaults to enabled.
	repo = &creativeRuntimeSettingsFixture{values: map[string]string{}}
	svc = NewRuntimeSettings(repo, settings.ErrSettingNotFound)
	require.True(t, svc.IsCreativeEnabled(context.Background()))
}

func TestSettingServiceGetCreativeModelSettings(t *testing.T) {
	repo := &creativeRuntimeSettingsFixture{values: map[string]string{
		SettingKeyCreativeModelSettings: `[{"group_id":12,"model":"gpt-image-2","operations":["generate"]}]`,
	}}
	svc := NewRuntimeSettings(repo, settings.ErrSettingNotFound)
	require.Equal(t, []CreativeModelSetting{{
		GroupID:    12,
		Model:      "gpt-image-2",
		Operations: []string{CreativeOperationGenerate},
	}}, svc.GetCreativeModelSettings(context.Background()))

	repo.values[SettingKeyCreativeModelSettings] = "not-json"
	require.Empty(t, svc.GetCreativeModelSettings(context.Background()))
}

func (r *creativeRuntimeSettingsFixture) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", settings.ErrSettingNotFound
}

func TestParseCreativeModelSettingsFailsClosed(t *testing.T) {
	require.Empty(t, ParseCreativeModelSettings("{broken"))
	require.Empty(t, ParseCreativeModelSettings(`[{"group_id":1,"model":"image","operations":[]}]`))

	parsed := ParseCreativeModelSettings(`[{
		"group_id": 7,
		"model": "gpt-image-2",
		"operations": ["edit", "generate"]
	}]`)
	require.Equal(t, []CreativeModelSetting{{
		GroupID:    7,
		Model:      "gpt-image-2",
		Operations: []string{"generate", "edit"},
	}}, parsed)
}
