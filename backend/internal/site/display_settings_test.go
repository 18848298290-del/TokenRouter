package site

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// displayValues records queried keys to check on-demand reads and configuration defaults.
type displayValues struct {
	values map[string]string
	err    error
	keys   []string
}

func (s *displayValues) GetValue(_ context.Context, key string) (string, error) {
	s.keys = append(s.keys, key)
	return s.values[key], s.err
}

func TestDisplaySettingsPreserveNameAndMenuValues(t *testing.T) {
	for _, tc := range []struct {
		name     string
		value    string
		err      error
		wantName string
		wantMenu string
	}{
		{name: "empty", wantName: "TokenRouter"},
		{name: "whitespace", value: "  ", wantName: "  ", wantMenu: "  "},
		{name: "stored", value: "display", wantName: "display", wantMenu: "display"},
		{name: "read failure", value: "ignored", err: errors.New("read failed"), wantName: "TokenRouter", wantMenu: "[]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &displayValues{values: map[string]string{SettingKeySiteName: tc.value, SettingKeyCustomMenuItems: tc.value}, err: tc.err}
			reader := NewDisplaySettings(repo, nil)
			require.Empty(t, repo.keys)
			require.Equal(t, tc.wantName, reader.GetSiteName(context.Background()))
			require.Equal(t, tc.wantMenu, reader.GetCustomMenuItemsRaw(context.Background()))
			require.Equal(t, []string{SettingKeySiteTexts, SettingKeySiteName, SettingKeyCustomMenuItems}, repo.keys)
		})
	}
}

func TestDisplaySettingsFrontendFallbackIsLazy(t *testing.T) {
	repo := &displayValues{values: map[string]string{SettingKeyFrontendURL: " https://example.com "}}
	calls := 0
	fallback := " https://fallback.example "
	reader := NewDisplaySettings(repo, func() string { calls++; return fallback })
	require.Equal(t, "https://example.com", reader.GetFrontendURL(context.Background()))
	require.Zero(t, calls)
	repo.values[SettingKeyFrontendURL] = " "
	require.Equal(t, fallback, reader.GetFrontendURL(context.Background()))
	fallback = "https://changed.example"
	repo.err = errors.New("read failed")
	require.Equal(t, fallback, reader.GetFrontendURL(context.Background()))
	require.Equal(t, 2, calls)
}
