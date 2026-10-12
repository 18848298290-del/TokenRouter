package admission

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// bmRepoStub counts setting reads while a test supplies their results.
type bmRepoStub struct {
	// getValueFn supplies each test's read result. GetValue panics when this
	// callback is nil so an unexpected setting read fails the test immediately.
	getValueFn func(ctx context.Context, key string) (string, error)
	calls      int // recorded read count
}

// GetValue records a setting read and calls the test's configured callback.
// An unset callback panics to expose unexpected reads; tests access the counter
// from their synchronous invocation path.
func (s *bmRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	s.calls++
	if s.getValueFn == nil {
		panic("unexpected GetValue call")
	}
	return s.getValueFn(ctx, key)
}

// TestIsBackendModeEnabled_ReturnsTrue supplies the enabled persisted value.
// The identity-facing accessor returns true and requests the backend-mode key
// exactly once from the repository.
func TestIsBackendModeEnabled_ReturnsTrue(t *testing.T) {
	repo := &bmRepoStub{
		getValueFn: func(ctx context.Context, key string) (string, error) {
			require.Equal(t, BackendModeKey, key)
			return "true", nil
		},
	}
	svc := NewBackendMode(repo, nil)

	require.True(t, svc.IsBackendModeEnabled(context.Background()))
	require.Equal(t, 1, repo.calls)
}

// TestIsBackendModeEnabled_ReturnsFalse supplies the disabled persisted value.
// The identity-facing accessor returns false and performs one repository read
// for the expected backend-mode setting key.
func TestIsBackendModeEnabled_ReturnsFalse(t *testing.T) {
	repo := &bmRepoStub{
		getValueFn: func(ctx context.Context, key string) (string, error) {
			require.Equal(t, BackendModeKey, key)
			return "false", nil
		},
	}
	svc := NewBackendMode(repo, nil)

	require.False(t, svc.IsBackendModeEnabled(context.Background()))
	require.Equal(t, 1, repo.calls)
}

// TestIsBackendModeEnabled_ReturnsFalseOnNotFound models an unconfigured toggle.
// A missing-setting error disables backend mode and the accessor makes one read
// using the expected persisted setting key.
func TestIsBackendModeEnabled_ReturnsFalseOnNotFound(t *testing.T) {
	repo := &bmRepoStub{
		getValueFn: func(ctx context.Context, key string) (string, error) {
			require.Equal(t, BackendModeKey, key)
			return "", settings.ErrSettingNotFound
		},
	}
	svc := NewBackendMode(repo, nil)

	require.False(t, svc.IsBackendModeEnabled(context.Background()))
	require.Equal(t, 1, repo.calls)
}

// TestIsBackendModeEnabled_ReturnsFalseOnDBError models an unavailable setting
// repository. The accessor treats the failed read as disabled and performs one
// attempt with the backend-mode key.
func TestIsBackendModeEnabled_ReturnsFalseOnDBError(t *testing.T) {
	repo := &bmRepoStub{
		getValueFn: func(ctx context.Context, key string) (string, error) {
			require.Equal(t, BackendModeKey, key)
			return "", errors.New("db down")
		},
	}
	svc := NewBackendMode(repo, nil)

	require.False(t, svc.IsBackendModeEnabled(context.Background()))
	require.Equal(t, 1, repo.calls)
}

// TestIsBackendModeEnabled_CachesResult reads an enabled setting twice before
// expiration. Both calls return true while the repository counter remains one,
// protecting reuse of the cached snapshot.
func TestIsBackendModeEnabled_CachesResult(t *testing.T) {
	repo := &bmRepoStub{
		getValueFn: func(ctx context.Context, key string) (string, error) {
			require.Equal(t, BackendModeKey, key)
			return "true", nil
		},
	}
	svc := NewBackendMode(repo, nil)

	require.True(t, svc.IsBackendModeEnabled(context.Background()))
	require.True(t, svc.IsBackendModeEnabled(context.Background()))
	require.Equal(t, 1, repo.calls)
}
