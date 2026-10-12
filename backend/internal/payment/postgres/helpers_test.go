package postgres_test

import (
	"context"
)

// paymentConfigSettingRepoStub stores test settings and captures writes.
type paymentConfigSettingRepoStub struct {
	values  map[string]string
	updates map[string]string
}

func (s *paymentConfigSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *paymentConfigSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		out[key] = s.values[key]
	}
	return out, nil
}

func (s *paymentConfigSettingRepoStub) SetMultiple(_ context.Context, values map[string]string) error {
	s.updates = make(map[string]string, len(values))
	if s.values == nil {
		s.values = make(map[string]string)
	}
	for key, value := range values {
		s.updates[key], s.values[key] = value, value
	}
	return nil
}
