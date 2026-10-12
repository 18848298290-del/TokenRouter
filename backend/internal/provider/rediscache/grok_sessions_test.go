package rediscache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	sessionstore "github.com/TokenFlux/TokenRouter/internal/infra/redis/session"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// A Grok session cached after a successful remote write remains subject to
// Redis reads. This grok_sessions.go test deletes remote data before
// simulating an outage, then confirms only a newly failed write can use the
// provider core's local read and single-consumption path.

// TestSessionStoreRedisFallbackIsLimitedToFailedWrites removes a remotely
// saved session before shutting Redis down and writing another session. The
// first read stays missing, while the failed write supports local reads and
// exactly one local consumption in this fixture.
func TestSessionStoreRedisFallbackIsLimitedToFailedWrites(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	// Close the test-owned connection after the deferred session-store stop; cleanup
	// errors do not replace the session assertions that describe this outage scenario.
	t.Cleanup(func() { _ = client.Close() })
	store := NewGrokSessionStore(client)
	defer store.Stop()
	session := func(state string) *provider.GrokOAuthSession {
		return &provider.GrokOAuthSession{State: state, CreatedAt: time.Now()}
	}

	store.Set("remote", session("remote"))
	require.NoError(t, sessionstore.New(client, "oauth:session:xai", provider.GrokSessionTTL).Delete(context.Background(), "remote"))
	_, ok := store.Get("remote")
	require.False(t, ok, "a remote miss must not revive the stale local copy")

	// Force the next remote write to fail after proving a remote deletion remains
	// authoritative, so the later memory read belongs specifically
	// to failed-write fallback.
	mr.Close()
	store.Set("local-only", session("local"))
	got, ok := store.Get("local-only")
	require.True(t, ok)
	require.Equal(t, "local", got.State)
	require.True(t, store.TryConsumeSession("local-only"))
	require.False(t, store.TryConsumeSession("local-only"))
}
