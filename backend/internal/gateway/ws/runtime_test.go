package ws

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// settingReadFunc controls reads to test races between refresh and publication.
type settingReadFunc func(context.Context, []string) (map[string]string, error)

func (f settingReadFunc) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	return f(ctx, keys)
}

func TestParametersPartialUpdateAndReset(t *testing.T) {
	defaults := DefaultParameters()
	defaults.ReadTimeoutSeconds = 77
	stored, err := PatchParameters(defaults, `{"read_timeout_seconds":30,"max_ingress_connections_per_api_key":4}`, json.RawMessage(`{"read_timeout_seconds":null,"max_ingress_connections_per_api_key":0,"dynamic_max_conns_by_provider_concurrency_enabled":false}`))
	require.NoError(t, err)
	value, err := ResolveParameters(defaults, stored)
	require.NoError(t, err)
	require.Equal(t, 77, value.ReadTimeoutSeconds)
	require.Zero(t, value.MaxIngressConnectionsPerAPIKey)
	require.False(t, value.DynamicMaxConnsByProviderConcurrencyEnabled)
	cleared, err := PatchParameters(defaults, stored, json.RawMessage(`null`))
	require.NoError(t, err)
	require.JSONEq(t, `{}`, cleared)
	repaired, err := PatchParameters(defaults, `{broken`, json.RawMessage(`null`))
	require.NoError(t, err)
	require.JSONEq(t, `{}`, repaired)
	require.Equal(t, defaults, NewRuntime(defaults, nil, nil).Snapshot())
	for _, invalid := range []string{`{"max_conns_per_provider":1}`, `{"read_timeout_seconds":0}`, `{"unknown":null}`, `{"enabled":false}`, `{"client_read_limit_bytes":1.5}`, `[]`} {
		_, err = PatchParameters(defaults, stored, json.RawMessage(invalid))
		require.Error(t, err, invalid)
	}
}

// TestRuntimeAcceptsZeroIdleConnections covers zero idle connections in defaults, overrides, and reset.
func TestRuntimeAcceptsZeroIdleConnections(t *testing.T) {
	defaults := DefaultParameters()
	defaults.MinIdlePerProvider = 0
	defaults.MaxIdlePerProvider = 0
	runtime := NewRuntime(defaults, nil, nil)
	require.NoError(t, runtime.Publish(`{}`))
	require.Zero(t, runtime.Snapshot().MaxIdlePerProvider)
	stored, err := PatchParameters(DefaultParameters(), "", json.RawMessage(`{"min_idle_per_provider":0,"max_idle_per_provider":0}`))
	require.NoError(t, err)
	cleared, err := PatchParameters(defaults, stored, json.RawMessage(`null`))
	require.NoError(t, err)
	require.NoError(t, runtime.Publish(cleared))
	require.Zero(t, runtime.Snapshot().MaxIdlePerProvider)
	_, err = PatchParameters(defaults, "", json.RawMessage(`{"max_idle_per_provider":-1}`))
	require.Error(t, err)
	_, err = PatchParameters(defaults, "", json.RawMessage(`{"min_idle_per_provider":1}`))
	require.Error(t, err)
}

func TestRuntimeLateRefreshCannotOverwriteSavedSettings(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	source := settingReadFunc(func(context.Context, []string) (map[string]string, error) {
		close(started)
		<-release
		return map[string]string{SettingKey: `{"read_timeout_seconds":50}`}, nil
	})
	runtime := NewRuntime(DefaultParameters(), source, nil)
	done := make(chan error, 1)
	go func() { done <- runtime.Refresh(context.Background()) }()
	<-started
	require.NoError(t, runtime.Publish(`{"read_timeout_seconds":60}`))
	close(release)
	require.NoError(t, <-done)
	require.Equal(t, 60, runtime.Snapshot().ReadTimeoutSeconds)
	require.NoError(t, runtime.Stop(context.Background()))
	require.Error(t, runtime.Publish(`{}`))
}

func TestRuntimeReadOrApplyFailureKeepsLastSnapshot(t *testing.T) {
	runtime := NewRuntime(DefaultParameters(), settingReadFunc(func(context.Context, []string) (map[string]string, error) { return nil, errors.New("offline") }), nil)
	require.NoError(t, runtime.Publish(`{"read_timeout_seconds":60}`))
	require.Error(t, runtime.Refresh(context.Background()))
	require.Equal(t, 60, runtime.Snapshot().ReadTimeoutSeconds)
	runtime.SetApply(func(Parameters) error { return errors.New("pool stopped") })
	require.Error(t, runtime.Publish(`{"read_timeout_seconds":70}`))
	require.Equal(t, 60, runtime.Snapshot().ReadTimeoutSeconds)
}

func TestSettingsParticipantValidatesBeforePublishing(t *testing.T) {
	p := SettingsParticipant(DefaultParameters())
	change, err := p.Prepare(context.Background(), nil, nil)
	require.NoError(t, err)
	require.Empty(t, change.Values)
	change, err = p.Prepare(context.Background(), map[string]json.RawMessage{SettingKey: json.RawMessage(`{"max_conns_per_provider":1}`)}, nil)
	require.Error(t, err)
	require.Empty(t, change.Values)
}
