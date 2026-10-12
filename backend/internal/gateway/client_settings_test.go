package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

// allowClaudeCodeSettingRepoStub retains scalar settings and records writes for client tests.
type allowClaudeCodeSettingRepoStub struct {
	// values supplies subsequent reads, and sets records every replacement made
	// through Set. Tests inspect sets to distinguish an unchanged default from
	// a migration that actually persisted a new value.
	values map[string]string // current scalar setting values
	sets   map[string]string // recorded scalar write values
}

// Get rejects record-object lookups in this scalar settings fixture. Permission
// and model-migration tests use GetValue, so a call here panics to expose an
// unexpected dependency on setting objects.
func (s *allowClaudeCodeSettingRepoStub) Get(ctx context.Context, key string) (*settingscore.Setting, error) {
	panic("unused")
}

// GetValue returns a configured scalar value for the requested key. An absent
// entry returns settings.ErrSettingNotFound, allowing the runtime reader to
// exercise its missing-setting default.
func (s *allowClaudeCodeSettingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", settingscore.ErrSettingNotFound
}

// Set records a scalar write in both the current values and write trace. Nil
// maps are allocated first, so migration tests can inspect the resulting stored
// value and whether a write occurred.
func (s *allowClaudeCodeSettingRepoStub) Set(ctx context.Context, key, value string) error {
	if s.values == nil {
		s.values = map[string]string{}
	}
	if s.sets == nil {
		s.sets = map[string]string{}
	}
	s.values[key] = value
	s.sets[key] = value
	return nil
}

// GetMultiple rejects batch reads in the client-setting fixture. The tests
// exercise single-key permission and migration operations, and a call here
// panics to expose an unexpected batch-read dependency.
func (s *allowClaudeCodeSettingRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	panic("unused")
}

// SetMultiple rejects bulk updates in this migration fixture. The migration
// writes one named scalar setting through Set, so invoking bulk persistence
// panics before a test can silently accept that different write path.
func (s *allowClaudeCodeSettingRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	panic("unused")
}

// GetAll rejects whole-settings reads in the client fixture. Permission and
// migration cases load their named key through GetValue, and this panic exposes
// any unexpected inventory lookup.
func (s *allowClaudeCodeSettingRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	panic("unused")
}

// Delete rejects setting deletion in this client fixture. The covered migration
// replaces a scalar model value and permission checks read it, so deletion
// panics to expose an unexpected destructive operation.
func (s *allowClaudeCodeSettingRepoStub) Delete(ctx context.Context, key string) error {
	panic("unused")
}

// TestSettingService_IsOpenAIAllowClaudeCodeCodexPluginEnabled supplies absent,
// true, and false permission values. The true fixture enables the setting,
// while absent and false fixtures leave plugin permission disabled.
func TestSettingService_IsOpenAIAllowClaudeCodeCodexPluginEnabled(t *testing.T) {
	t.Run("默认关闭（设置缺失）", func(t *testing.T) {
		svc := NewRuntimeSettings(&allowClaudeCodeSettingRepoStub{values: map[string]string{}}, settingscore.ErrSettingNotFound, nil)
		require.False(t, svc.IsOpenAIAllowClaudeCodeCodexPluginEnabled(context.Background()))
	})
	t.Run("值为 true 时开启", func(t *testing.T) {
		svc := NewRuntimeSettings(&allowClaudeCodeSettingRepoStub{values: map[string]string{
			SettingKeyOpenAIAllowClaudeCodeCodexPlugin: "true",
		}}, settingscore.ErrSettingNotFound, nil)
		require.True(t, svc.IsOpenAIAllowClaudeCodeCodexPluginEnabled(context.Background()))
	})
	t.Run("值非 true 时关闭", func(t *testing.T) {
		svc := NewRuntimeSettings(&allowClaudeCodeSettingRepoStub{values: map[string]string{
			SettingKeyOpenAIAllowClaudeCodeCodexPlugin: "false",
		}}, settingscore.ErrSettingNotFound, nil)
		require.False(t, svc.IsOpenAIAllowClaudeCodeCodexPluginEnabled(context.Background()))
	})
}

// TestSettingService_MigrateGrokDefaultTextModel supplies the historical
// default, a custom model, and an absent key. The historical default is
// replaced with grok-4.6, while the custom and missing cases produce no write.
func TestSettingService_MigrateGrokDefaultTextModel(t *testing.T) {
	t.Run("升级历史默认值", func(t *testing.T) {
		repo := &allowClaudeCodeSettingRepoStub{values: map[string]string{SettingKeyGrokDefaultTextModel: "grok-4.5"}}
		svc := NewRuntimeSettings(repo, settingscore.ErrSettingNotFound, nil)
		require.NoError(t, svc.MigrateGrokDefaultTextModel(context.Background()))
		require.Equal(t, "grok-4.6", repo.sets[SettingKeyGrokDefaultTextModel])
	})
	t.Run("保留显式模型", func(t *testing.T) {
		repo := &allowClaudeCodeSettingRepoStub{values: map[string]string{SettingKeyGrokDefaultTextModel: "grok-4.3"}}
		svc := NewRuntimeSettings(repo, settingscore.ErrSettingNotFound, nil)
		require.NoError(t, svc.MigrateGrokDefaultTextModel(context.Background()))
		require.Empty(t, repo.sets)
	})
	t.Run("缺失设置不写入", func(t *testing.T) {
		repo := &allowClaudeCodeSettingRepoStub{values: map[string]string{}}
		svc := NewRuntimeSettings(repo, settingscore.ErrSettingNotFound, nil)
		require.NoError(t, svc.MigrateGrokDefaultTextModel(context.Background()))
		require.Empty(t, repo.sets)
	})
}
