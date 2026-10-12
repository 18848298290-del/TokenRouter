package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// promptSettingsFixture supplies forwarding settings to tests.
type promptSettingsFixture struct {
	RuntimeSettingsStore
	data map[string]string
}

// identityPatchStore records single-key reads and can inject a read error.
type identityPatchStore struct {
	RuntimeSettingsStore
	values map[string]string
	err    error
	reads  []string
}

func TestSettingService_GetClaudeOAuthSystemPromptInjectionSettings(t *testing.T) {
	t.Run("defaults to enabled with empty prompt", func(t *testing.T) {
		svc := NewRuntimeSettings(&promptSettingsFixture{data: map[string]string{}}, nil, nil)

		enabled, prompt, blocks := svc.GetClaudeOAuthSystemPromptInjectionSettings(context.Background())

		require.True(t, enabled)
		require.Empty(t, prompt)
		require.Empty(t, blocks)
	})

	t.Run("uses configured switch prompt and blocks", func(t *testing.T) {
		const customPrompt = "custom prompt\n\nkeep spacing"
		const customBlocks = `[{"type":"text","text":"custom block","cache_control":true}]`
		svc := NewRuntimeSettings(&promptSettingsFixture{data: map[string]string{
			SettingKeyEnableClaudeOAuthSystemPromptInjection: "false",
			SettingKeyClaudeOAuthSystemPrompt:                customPrompt,
			SettingKeyClaudeOAuthSystemPromptBlocks:          customBlocks,
		}}, nil, nil)

		enabled, prompt, blocks := svc.GetClaudeOAuthSystemPromptInjectionSettings(context.Background())

		require.False(t, enabled)
		require.Equal(t, customPrompt, prompt)
		require.Equal(t, customBlocks, blocks)
	})
}

func TestIdentityPatchSettingsOriginalReadSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		err         error
		enabled     bool
	}{
		{name: "enabled", value: "true", enabled: true},
		{name: "disabled", value: "false"},
		{name: "empty"},
		{name: "exact_case", value: "TRUE"},
		{name: "read_failure", err: errors.New("read failed"), enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &identityPatchStore{values: map[string]string{SettingKeyEnableIdentityPatch: tc.value, SettingKeyIdentityPatchPrompt: "原提示词"}, err: tc.err}
			runtime := NewRuntimeSettings(store, nil, nil)
			require.Empty(t, store.reads)
			require.Equal(t, tc.enabled, runtime.IsIdentityPatchEnabled(context.Background()))
			expected := "原提示词"
			if tc.err != nil {
				expected = ""
			}
			require.Equal(t, expected, runtime.GetIdentityPatchPrompt(context.Background()))
			require.Equal(t, []string{SettingKeyEnableIdentityPatch, SettingKeyIdentityPatchPrompt}, store.reads)
		})
	}
}

func (s *promptSettingsFixture) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.data[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func (s *identityPatchStore) GetValue(_ context.Context, key string) (string, error) {
	s.reads = append(s.reads, key)
	return s.values[key], s.err
}
