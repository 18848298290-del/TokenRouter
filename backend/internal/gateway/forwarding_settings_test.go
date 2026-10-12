package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// promptSettingsFixture supplies requested scalar forwarding values from test data.
type promptSettingsFixture struct {
	// RuntimeSettingsStore provides unimplemented methods through the embedded
	// interface. Tests configure the batch-read path and leave other methods unused.
	RuntimeSettingsStore
	data map[string]string // configured forwarding setting values
}

// identityPatchStore records single-key reads and can inject a read error.
type identityPatchStore struct {
	// RuntimeSettingsStore leaves operations other than GetValue unset in these
	// read-order tests. An unexpected embedded-method call cannot provide a setting result.
	RuntimeSettingsStore
	values map[string]string // configured identity setting values
	err    error             // injected scalar read error
	reads  []string          // ordered setting lookup trace
}

// TestSettingService_GetClaudeOAuthSystemPromptInjectionSettings reads missing
// settings and an explicit disabled configuration. The default enables
// injection with empty prompt values, while configured text and block JSON
// retain their exact contents.
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

// TestIdentityPatchSettingsOriginalReadSemantics supplies exact, empty,
// case-varied, and failed setting reads. It protects per-call lookup order,
// literal true matching, enabled-on-error behavior, and unchanged successful
// prompt text.
func TestIdentityPatchSettingsOriginalReadSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, value string // test scenario label; persisted switch value
		err         error  // configured setting read error
		enabled     bool   // expected identity patch permission
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

// GetMultiple copies the requested entries from the fixture data into a fresh
// map. Missing keys are omitted so the forwarding reader, rather than the
// fixture, supplies its setting defaults.
func (s *promptSettingsFixture) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.data[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

// GetValue records the identity-setting lookup and returns its configured value
// and error. The trace lets tests confirm constructor laziness and the
// switch-before-prompt order of their two explicit reads.
func (s *identityPatchStore) GetValue(_ context.Context, key string) (string, error) {
	s.reads = append(s.reads, key)
	return s.values[key], s.err
}
