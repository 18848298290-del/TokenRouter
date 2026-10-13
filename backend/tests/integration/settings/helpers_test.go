package settings_test

import (
	"context"
	"maps"

	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

// Settings scenarios capture prepared write batches and supply selected runtime
// reads through small map fixtures. Backend-mode reads can be routed through a
// callback, update fixtures accumulate string values, and whole-map read
// fixtures return copies that callers can inspect independently.
//
// These fixtures have different failure effects: settingUpdateRepoStub changes
// its captured and accumulated maps before returning its configured write
// error. That observation records an attempted write and does not establish
// database rollback behavior. Their maps have no synchronization, so callers
// serialize access or use the separately locked threshold and key-limit
// fixtures when calls can overlap. Unexpected repository methods panic to
// expose a flow using unsupported reads or writes.

// bmUpdateRepoStub captures backend-mode batch writes and delegates selected
// single-value reads.
type bmUpdateRepoStub struct {
	// updates is replaced by each SetMultiple call with copied input entries, and
	// GetAll returns a clone; callers serialize direct access to this unprotected
	// map.
	updates map[string]string
	// getValueFn handles the supported single-value read. A nil callback panics,
	// and configured callbacks receive the caller's original context and setting
	// key.
	getValueFn func(ctx context.Context, key string) (string, error)
}

// settingUpdateRepoStub captures attempted setting batches and accumulates
// values before reporting configured failure.
type settingUpdateRepoStub struct {
	// updates captures the latest batch while values accumulates all written keys.
	// SetMultiple changes both before reading setMultipleErr, so failed calls
	// still record writes.
	updates map[string]string // latest captured write batch
	values  map[string]string // accumulated setting read values
	// setMultipleErr is returned after fixture maps have changed. Nil reports
	// success; a configured error leaves those attempted values available for
	// later reads.
	setMultipleErr error
}

// settingGetAllRepoStub supplies complete configured settings through its
// supported copied-map read.
type settingGetAllRepoStub struct {
	// values supplies GetAll input and may be nil for an empty response. Callers
	// serialize direct mutations; each GetAll result receives separate map
	// storage.
	values map[string]string
}

// Get panics when a backend-mode settings case unexpectedly requests this
// unsupported repository operation.
func (s *bmUpdateRepoStub) Get(ctx context.Context, key string) (*settingscore.Setting, error) {
	panic("unexpected Get call")
}

// GetValue delegates the supplied context and key to getValueFn and returns its
// result unchanged. A nil callback panics, so cases configure it before
// permitting single-value settings reads.
func (s *bmUpdateRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	if s.getValueFn == nil {
		panic("unexpected GetValue call")
	}
	return s.getValueFn(ctx, key)
}

// Set panics when a backend-mode settings case unexpectedly requests this
// unsupported repository operation.
func (s *bmUpdateRepoStub) Set(ctx context.Context, key, value string) error {
	panic("unexpected Set call")
}

// GetMultiple panics when a backend-mode settings case unexpectedly requests
// this unsupported repository operation.
func (s *bmUpdateRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	panic("unexpected GetMultiple call")
}

// SetMultiple replaces updates with a newly allocated copy of the supplied
// string map and reports success. The caller can later mutate its input
// independently; this fixture ignores ctx and records no database transaction.
func (s *bmUpdateRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	s.updates = make(map[string]string, len(settings))
	maps.Copy(s.updates, settings)
	return nil
}

// GetAll clones the latest captured updates map and reports success. It returns
// nil before any batch is captured, and callers can mutate a nonnil returned
// map independently of updates.
func (s *bmUpdateRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	return maps.Clone(s.updates), nil
}

// Delete panics when a backend-mode settings case unexpectedly requests this
// unsupported repository operation.
func (s *bmUpdateRepoStub) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}

// Get panics when an accumulated settings-update fixture unexpectedly requests
// this unsupported repository operation.
func (s *settingUpdateRepoStub) Get(ctx context.Context, key string) (*settingscore.Setting, error) {
	panic("unexpected Get call")
}

// GetValue returns a configured stored string when its key exists, including an
// empty string value. Missing keys return ErrSettingNotFound, and this map
// fixture ignores the supplied context.
func (s *settingUpdateRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	if s.values != nil {
		if value, ok := s.values[key]; ok {
			return value, nil
		}
	}
	return "", settingscore.ErrSettingNotFound
}

// Set panics when an accumulated settings-update fixture unexpectedly requests
// this unsupported repository operation.
func (s *settingUpdateRepoStub) Set(ctx context.Context, key, value string) error {
	panic("unexpected Set call")
}

// GetMultiple panics when an accumulated settings-update fixture unexpectedly
// requests this unsupported repository operation.
func (s *settingUpdateRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	panic("unexpected GetMultiple call")
}

// SetMultiple captures the new batch and merges its values before returning
// setMultipleErr. Nil accumulated storage is allocated during the first written
// key, and even a configured error leaves these fixture maps changed.
func (s *settingUpdateRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	// Record and merge the attempted batch before returning the injected error;
	// tests use this captured attempt while runtime publication still follows
	// Save's error result.
	s.updates = make(map[string]string, len(settings))
	for k, v := range settings {
		s.updates[k] = v
		if s.values == nil {
			s.values = map[string]string{}
		}
		s.values[k] = v
	}
	return s.setMultipleErr
}

// GetAll returns a newly allocated copy of the accumulated values and reports
// success. Nil storage produces an allocated empty map, so callers can modify
// results independently of this unsynchronized fixture.
func (s *settingUpdateRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	out := make(map[string]string, len(s.values))
	maps.Copy(out, s.values)
	return out, nil
}

// Delete panics when an accumulated settings-update fixture unexpectedly
// requests this unsupported repository operation.
func (s *settingUpdateRepoStub) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}

// Get panics when a whole-map settings reader unexpectedly requests this
// unsupported repository operation.
func (s *settingGetAllRepoStub) Get(ctx context.Context, key string) (*settingscore.Setting, error) {
	panic("unexpected Get call")
}

// GetValue panics when a whole-map settings reader unexpectedly requests this
// unsupported repository operation.
func (s *settingGetAllRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	panic("unexpected GetValue call")
}

// Set panics when a whole-map settings reader unexpectedly requests this
// unsupported repository operation.
func (s *settingGetAllRepoStub) Set(ctx context.Context, key, value string) error {
	panic("unexpected Set call")
}

// GetMultiple panics when a whole-map settings reader unexpectedly requests
// this unsupported repository operation.
func (s *settingGetAllRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	panic("unexpected GetMultiple call")
}

// SetMultiple panics when a whole-map settings reader unexpectedly requests
// this unsupported repository operation.
func (s *settingGetAllRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	panic("unexpected SetMultiple call")
}

// GetAll copies configured string values into a newly allocated map and reports
// success. Nil values produce an allocated empty map, and the caller can mutate
// that result independently of fixture storage.
func (s *settingGetAllRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	out := make(map[string]string, len(s.values))
	maps.Copy(out, s.values)
	return out, nil
}

// Delete panics when a whole-map settings reader unexpectedly requests this
// unsupported repository operation.
func (s *settingGetAllRepoStub) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}
