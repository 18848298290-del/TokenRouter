package creative

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Persisted model settings fail closed after decoding or validation failure,
// while an absent feature switch defaults to enabled. Map-backed readers
// isolate these parsing results from external settings storage and allow an
// existing model value to change between reads.

// creativeRuntimeSettingsFixture returns stored values or ErrSettingNotFound
// for missing keys.
type creativeRuntimeSettingsFixture struct {
	values map[string]string // mutable stored setting strings
}

// TestSettingService_IsCreativeEnabled reads a stored false feature value and
// then an absent value. The first disables creative and the second returns the
// enabled default through the map reader.
func TestSettingService_IsCreativeEnabled(t *testing.T) {
	repo := &creativeRuntimeSettingsFixture{values: map[string]string{SettingKeyCreativeEnabled: "false"}}
	svc := NewRuntimeSettings(repo, settings.ErrSettingNotFound)
	require.False(t, svc.IsCreativeEnabled(context.Background()))

	// A missing key defaults to enabled.
	repo = &creativeRuntimeSettingsFixture{values: map[string]string{}}
	svc = NewRuntimeSettings(repo, settings.ErrSettingNotFound)
	require.True(t, svc.IsCreativeEnabled(context.Background()))
}

// TestSettingServiceGetCreativeModelSettings reads one valid model JSON entry,
// then replaces it with malformed JSON. It checks the normalized entry first
// and the empty allowlist on the second read.
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

// GetValue returns the fixture stored string for key or ErrSettingNotFound.
// The map remains shared with the test so replacing a value affects the next
// read.
func (r *creativeRuntimeSettingsFixture) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", settings.ErrSettingNotFound
}

// TestParseCreativeModelSettingsFailsClosed supplies broken JSON, an
// empty-operation entry, and valid unordered operations. Invalid cases yield
// empty settings, while the valid case returns generate before edit.
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
