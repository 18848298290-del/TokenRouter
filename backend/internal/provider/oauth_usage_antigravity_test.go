package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"
)

// TestAntigravitySharedUsageRejectsDifferentIdentity blocks an old-credential
// query while a changed record joins its flight. It checks one fetch,
// successful original completion, and identity-change rejection for the second
// caller.
func TestAntigravitySharedUsageRejectsDifferentIdentity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		entered := make(chan struct{})
		release := make(chan struct{})
		cache := NewOAuthUsageCache()
		core := NewOAuthUsageService(nil, cache, nil, OAuthUsageOptions{Antigravity: AntigravityUsageOptions{CanFetch: func(*Record) bool { return true }, Enrich: func(*UsageInfo, *Record) {}, Fetch: func(context.Context, *Record) (*UsageInfo, error) {
			calls.Add(1)
			close(entered)
			<-release
			return &UsageInfo{Source: "old"}, nil
		}}})
		old := &Record{ID: 9, Platform: PlatformAntigravity, Type: ProviderTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "old"}}
		first := make(chan error, 1)
		go func() { _, err := core.GetAntigravityUsage(context.Background(), old); first <- err }()
		<-entered
		fresh := CloneRecord(old)
		fresh.Credentials = map[string]any{"access_token": "new"}
		second := make(chan error, 1)
		go func() { _, err := core.GetAntigravityUsage(context.Background(), fresh); second <- err }()
		// Wait for both callers to block before releasing the first fetch. This
		// fixes the cross-credential join so the completed identity check rejects
		// the changed record.
		synctest.Wait()
		require.Equal(t, int32(1), calls.Load())
		close(release)
		require.NoError(t, <-first)
		require.ErrorIs(t, <-second, ErrUsageObservationChanged)
		require.Equal(t, int32(1), calls.Load())
	})
}
