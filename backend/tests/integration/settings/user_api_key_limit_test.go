package settings_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/settings/composite"
	settingskit "github.com/TokenFlux/TokenRouter/internal/settings/testkit"
)

// The default user key allowance treats zero as unlimited and replaces missing
// or invalid stored values with the identity default. Map-backed cases compare
// runtime getter and composite parsing outcomes, captured update strings, and
// BadRequest errors for values below zero or above the supported maximum.

// settingRepoStub records locked setting reads and configurable failures for
// default allowance cases.
type settingRepoStub struct {
	// mu protects values, err, getValueCalls, and getMultipleCalls during
	// supported reads. Test setup changes these fields before overlapping
	// repository calls can start.
	mu     sync.Mutex        // fixture setting observation lock
	values map[string]string // configured setting string values
	// err overrides both supported read methods after their attempt counter rises.
	// Nil permits configured values to be read, while a failure returns no setting
	// data.
	err              error
	getValueCalls    int // observed single-value read attempts
	getMultipleCalls int // observed batch-value read attempts
}

// TestSettingService_GetDefaultUserAPIKeyLimit supplies missing, malformed,
// negative, oversized, zero, and positive stored values. The identity reader
// uses defaults for invalid cases while retaining zero and the valid configured
// allowance.
func TestSettingService_GetDefaultUserAPIKeyLimit(t *testing.T) {
	tests := []struct {
		name string // allowance input case label
		// value supplies stored text; nil omits the setting key so the getter
		// exercises the missing-value path rather than a configured empty string.
		value  *string
		expect int // expected resolved key allowance
	}{
		{name: "设置缺失", expect: identity.DefaultUserAPIKeyLimit},
		{name: "非法字符串", value: stringPointer("invalid"), expect: identity.DefaultUserAPIKeyLimit},
		{name: "负数", value: stringPointer("-1"), expect: identity.DefaultUserAPIKeyLimit},
		{name: "超过数据库上限", value: stringPointer(fmt.Sprint(identity.MaxUserAPIKeyLimit + 1)), expect: identity.DefaultUserAPIKeyLimit},
		{name: "显式不限量", value: stringPointer("0"), expect: 0},
		{name: "显式上限", value: stringPointer("37"), expect: 37},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := map[string]string{}
			if tt.value != nil {
				values[identity.SettingKeyDefaultUserAPIKeyLimit] = *tt.value
			}
			svc := settingskit.NewComposite(&settingRepoStub{values: values}, &config.Config{})
			require.Equal(t, tt.expect, svc.Identity.GetDefaultUserAPIKeyLimit(context.Background()))
		})
	}
}

// TestSettingService_ParseDefaultUserAPIKeyLimit applies the same missing and
// invalid-value matrix to composite parsing. Its snapshot selects the default
// allowance or retains the supplied zero and valid positive values.
func TestSettingService_ParseDefaultUserAPIKeyLimit(t *testing.T) {
	svc := settingskit.NewComposite(&settingGetAllRepoStub{}, &config.Config{})

	tests := []struct {
		name     string            // composite allowance case label
		settings map[string]string // stored input setting values
		expect   int               // expected parsed key allowance
	}{
		{name: "设置缺失", settings: map[string]string{}, expect: identity.DefaultUserAPIKeyLimit},
		{name: "非法设置", settings: map[string]string{identity.SettingKeyDefaultUserAPIKeyLimit: "bad"}, expect: identity.DefaultUserAPIKeyLimit},
		{name: "负数设置", settings: map[string]string{identity.SettingKeyDefaultUserAPIKeyLimit: "-2"}, expect: identity.DefaultUserAPIKeyLimit},
		{name: "超过数据库上限", settings: map[string]string{identity.SettingKeyDefaultUserAPIKeyLimit: fmt.Sprint(identity.MaxUserAPIKeyLimit + 1)}, expect: identity.DefaultUserAPIKeyLimit},
		{name: "显式不限量", settings: map[string]string{identity.SettingKeyDefaultUserAPIKeyLimit: "0"}, expect: 0},
		{name: "显式上限", settings: map[string]string{identity.SettingKeyDefaultUserAPIKeyLimit: "58"}, expect: 58},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := composite.Parse(tt.settings, svc.Read)
			require.Equal(t, tt.expect, settings.DefaultUserAPIKeyLimit)
		})
	}
}

// TestSettingService_UpdateDefaultUserAPIKeyLimit saves unlimited and valid
// positive allowances through the capturing fixture. Invalid negative and
// oversized inputs return the allowance error with HTTP BadRequest status.
func TestSettingService_UpdateDefaultUserAPIKeyLimit(t *testing.T) {
	repo := &settingUpdateRepoStub{}
	svc := settingskit.NewComposite(repo, &config.Config{})

	require.NoError(t, svc.Save(context.Background(), &composite.Snapshot{DefaultUserAPIKeyLimit: 0}))
	require.Equal(t, "0", repo.updates[identity.SettingKeyDefaultUserAPIKeyLimit])

	require.NoError(t, svc.Save(context.Background(), &composite.Snapshot{DefaultUserAPIKeyLimit: 64}))
	require.Equal(t, "64", repo.updates[identity.SettingKeyDefaultUserAPIKeyLimit])

	err := svc.Save(context.Background(), &composite.Snapshot{DefaultUserAPIKeyLimit: -1})
	require.ErrorIs(t, err, identity.ErrUserAPIKeyLimitInvalid)
	require.Equal(t, 400, httpx.ErrorCode(err))

	err = svc.Save(context.Background(), &composite.Snapshot{DefaultUserAPIKeyLimit: identity.MaxUserAPIKeyLimit + 1})
	require.ErrorIs(t, err, identity.ErrUserAPIKeyLimitInvalid)
	require.Equal(t, 400, httpx.ErrorCode(err))
}

// stringPointer returns a pointer to a distinct copy of the supplied string
// fixture input.
func stringPointer(value string) *string {
	return &value
}

// Get panics when a default-key-limit reader unexpectedly requests this
// unsupported repository operation.
func (s *settingRepoStub) Get(ctx context.Context, key string) (*settingscore.Setting, error) {
	panic("unexpected Get call")
}

// GetValue counts its attempt under the mutex and returns configured failure
// before reading stored values. A present key returns its string, a missing key
// returns ErrSettingNotFound, and ctx is ignored.
func (s *settingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getValueCalls++
	if s.err != nil {
		return "", s.err
	}
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", settingscore.ErrSettingNotFound
}

// Set panics when a default-key-limit reader unexpectedly requests this
// unsupported repository operation.
func (s *settingRepoStub) Set(ctx context.Context, key, value string) error {
	panic("unexpected Set call")
}

// GetMultiple counts its attempt under the mutex before returning configured
// failure or a new map of present requested values. Missing keys are omitted,
// and the supplied context is ignored.
func (s *settingRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getMultipleCalls++
	if s.err != nil {
		return nil, s.err
	}
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if v, ok := s.values[key]; ok {
			result[key] = v
		}
	}
	return result, nil
}

// SetMultiple panics when a default-key-limit reader unexpectedly requests this
// unsupported repository operation.
func (s *settingRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	panic("unexpected SetMultiple call")
}

// GetAll panics when a default-key-limit reader unexpectedly requests this
// unsupported repository operation.
func (s *settingRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

// Delete panics when a default-key-limit reader unexpectedly requests this
// unsupported repository operation.
func (s *settingRepoStub) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}
