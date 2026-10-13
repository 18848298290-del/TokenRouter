package postgres_test

import (
	"context"
)

// Payment configuration scenarios use a small in-memory settings repository to
// observe partial updates. Reads return stored or empty string values with nil
// error, and SetMultiple copies the latest write batch while retaining
// unspecified settings for the next scenario read.

// paymentConfigSettingRepoStub retains scenario settings and independently
// captures the most recent update batch.
type paymentConfigSettingRepoStub struct {
	// values retains settings across write batches and supplies empty text for
	// missing keys. This scenario fixture uses it synchronously without a lock.
	values map[string]string
	// updates is newly allocated for each SetMultiple call and copies that batch.
	// Later input-map mutations therefore do not change the captured scalar
	// key/value entries.
	updates map[string]string
}

// GetValue returns a stored scalar setting or empty text with nil error.
// Missing keys deliberately differ from ErrSettingNotFound so parser scenarios
// exercise the supplied empty/default behavior.
func (s *paymentConfigSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

// GetMultiple returns a new map containing every requested key and its stored
// or empty value. Reads use nil error, and changing returned scalar entries
// leaves the fixture's persisted map unchanged.
func (s *paymentConfigSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		out[key] = s.values[key]
	}
	return out, nil
}

// SetMultiple applies scalar settings in place while capturing an independent
// copy of the latest write batch. Nil stored map is allocated and unspecified
// keys remain; callers use this unsynchronized fixture from one scenario
// goroutine.
func (s *paymentConfigSettingRepoStub) SetMultiple(_ context.Context, values map[string]string) error {
	// Replace the captured batch before applying its settings to retained
	// storage. The independent map lets assertions compare this update while
	// earlier unspecified persisted keys remain available.
	s.updates = make(map[string]string, len(values))
	if s.values == nil {
		s.values = make(map[string]string)
	}
	for key, value := range values {
		s.updates[key], s.values[key] = value, value
	}
	return nil
}
