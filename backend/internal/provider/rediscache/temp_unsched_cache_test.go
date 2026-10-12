package rediscache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Temporary suspension keeps its longest deadline, while OpenAI API Key
// failures count events within a sliding time window. These isolated Redis
// cases cover temp_unsched_cache.go, including expired input, replacement
// expiry, malformed JSON, repeated deletion, and threshold observations
// before and after old failures age out.

// newTempUnschedCacheTest returns a suspension adapter, client, and isolated
// miniredis server for one test. The test owns those handles; cleanup closes
// the client before RunT's registered server shutdown, and callers may
// advance the server clock independently.
func newTempUnschedCacheTest(t *testing.T) (provider.TempUnschedCache, *redis.Client, *miniredis.Miniredis) {
	t.Helper()

	// Give each fixture its own Redis clock and keys so advancing suspension expiry
	// or rolling-window time cannot alter another test's provider observations.
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		// The fixture owns this client and closes it before RunT tears down Redis;
		// cleanup failure is ignored after the test's state assertions have completed.
		_ = client.Close()
	})

	return NewTempUnschedCache(client), client, server
}

// TestTempUnschedCacheShorterStateDoesNotReplaceLongerState writes a longer
// suspension followed by a shorter one carrying different error details.
// Reading returns the entire longer observation, protecting metadata as well
// as its deadline.
func TestTempUnschedCacheShorterStateDoesNotReplaceLongerState(t *testing.T) {
	cache, _, _ := newTempUnschedCacheTest(t)
	ctx := context.Background()
	now := time.Now().Unix()
	longer := &provider.TempUnschedState{
		UntilUnix:       now + 300,
		TriggeredAtUnix: now,
		StatusCode:      429,
		MatchedKeyword:  "rate limit",
		RuleIndex:       1,
		ErrorMessage:    "longer state",
	}
	shorter := &provider.TempUnschedState{
		UntilUnix:       now + 120,
		TriggeredAtUnix: now + 1,
		StatusCode:      503,
		MatchedKeyword:  "overloaded",
		RuleIndex:       2,
		ErrorMessage:    "shorter state",
	}

	require.NoError(t, cache.SetTempUnsched(ctx, 101, longer))
	require.NoError(t, cache.SetTempUnsched(ctx, 101, shorter))

	got, err := cache.GetTempUnsched(ctx, 101)
	require.NoError(t, err)
	require.Equal(t, longer, got)
}

// TestTempUnschedCacheLongerStateExtendsExpiry replaces a short suspension
// with a longer observation and advances Redis beyond the former lifetime.
// The replacement still reads back with its complete metadata.
func TestTempUnschedCacheLongerStateExtendsExpiry(t *testing.T) {
	cache, _, server := newTempUnschedCacheTest(t)
	ctx := context.Background()
	now := time.Now().Unix()
	shorter := &provider.TempUnschedState{
		UntilUnix:       now + 120,
		TriggeredAtUnix: now,
		StatusCode:      429,
		ErrorMessage:    "shorter state",
	}
	longer := &provider.TempUnschedState{
		UntilUnix:       now + 300,
		TriggeredAtUnix: now + 1,
		StatusCode:      503,
		ErrorMessage:    "longer state",
	}

	require.NoError(t, cache.SetTempUnsched(ctx, 102, shorter))
	require.NoError(t, cache.SetTempUnsched(ctx, 102, longer))

	// Advance past the short key lifetime while remaining inside the longer one,
	// checking that replacing state also makes its later deadline survive Redis expiry.
	server.FastForward(150 * time.Second)

	got, err := cache.GetTempUnsched(ctx, 102)
	require.NoError(t, err)
	require.Equal(t, longer, got)
}

// TestTempUnschedCacheExpiredStateIsNotStored supplies a suspension whose
// deadline is already behind the application clock. The write succeeds as an
// ignored observation and a subsequent read returns nil.
func TestTempUnschedCacheExpiredStateIsNotStored(t *testing.T) {
	cache, _, _ := newTempUnschedCacheTest(t)
	ctx := context.Background()
	expired := &provider.TempUnschedState{
		UntilUnix:       time.Now().Add(-time.Minute).Unix(),
		TriggeredAtUnix: time.Now().Add(-2 * time.Minute).Unix(),
		StatusCode:      429,
		ErrorMessage:    "expired state",
	}

	require.NoError(t, cache.SetTempUnsched(ctx, 103, expired))

	got, err := cache.GetTempUnsched(ctx, 103)
	require.NoError(t, err)
	require.Nil(t, got)
}

// TestTempUnschedCacheMalformedStateReturnsClearError inserts invalid JSON
// under a provider's suspension key. Reading returns no state and identifies
// decoding in its error, protecting callers from treating corrupt data as
// absence.
func TestTempUnschedCacheMalformedStateReturnsClearError(t *testing.T) {
	cache, client, _ := newTempUnschedCacheTest(t)
	ctx := context.Background()
	require.NoError(t, client.Set(ctx, tempUnschedPrefix+"104", "{invalid-json", time.Minute).Err())

	got, err := cache.GetTempUnsched(ctx, 104)

	require.Nil(t, got)
	require.ErrorContains(t, err, "unmarshal state")
}

// TestTempUnschedCacheDeleteIsIdempotent saves a live suspension and deletes
// the same key twice. Both deletions succeed, and the final read confirms the
// state remains absent.
func TestTempUnschedCacheDeleteIsIdempotent(t *testing.T) {
	cache, _, _ := newTempUnschedCacheTest(t)
	ctx := context.Background()
	state := &provider.TempUnschedState{
		UntilUnix:       time.Now().Add(5 * time.Minute).Unix(),
		TriggeredAtUnix: time.Now().Unix(),
		StatusCode:      429,
		ErrorMessage:    "temporary state",
	}

	require.NoError(t, cache.SetTempUnsched(ctx, 105, state))
	require.NoError(t, cache.DeleteTempUnsched(ctx, 105))
	require.NoError(t, cache.DeleteTempUnsched(ctx, 105))

	got, err := cache.GetTempUnsched(ctx, 105)
	require.NoError(t, err)
	require.Nil(t, got)
}

// TestOpenAIAPIKeyHealthCacheTripsWithinRollingWindow records three failures
// inside a one-minute window with a threshold of three. Counts increase one
// at a time and only the third call reports a trip.
func TestOpenAIAPIKeyHealthCacheTripsWithinRollingWindow(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	// Close the client after observing the threshold result, leaving RunT to
	// remove this fixture's rolling-window and sequence keys with its isolated server.
	t.Cleanup(func() { _ = client.Close() })
	store, ok := NewTempUnschedCache(client).(provider.OpenAIAPIKeyHealthCache)
	require.True(t, ok)

	ctx := context.Background()
	for attempt := 1; attempt <= 3; attempt++ {
		count, tripped, err := store.RecordOpenAIAPIKeyHealthFailure(ctx, 42, 1, 3)
		require.NoError(t, err)
		require.EqualValues(t, attempt, count)
		require.Equal(t, attempt == 3, tripped)
	}
}

// TestOpenAIAPIKeyHealthCacheDropsFailuresOutsideRollingWindow advances
// miniredis's server time beyond the first failure's one-minute window. The
// next failure starts at count one and remains below the trip threshold.
func TestOpenAIAPIKeyHealthCacheDropsFailuresOutsideRollingWindow(t *testing.T) {
	server := miniredis.RunT(t)
	now := time.Unix(1_700_000_000, 0)
	server.SetTime(now)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	// Close the connection before RunT discards this fixture's controlled server
	// clock, keeping its shifted rolling-window observation local to the test.
	t.Cleanup(func() { _ = client.Close() })
	store, ok := NewTempUnschedCache(client).(provider.OpenAIAPIKeyHealthCache)
	require.True(t, ok)

	ctx := context.Background()
	count, tripped, err := store.RecordOpenAIAPIKeyHealthFailure(ctx, 42, 1, 3)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	require.False(t, tripped)

	// Move server time beyond the first failure's rolling window while its key
	// remains live, making score eviction observable independently of key expiry.
	server.SetTime(now.Add(61 * time.Second))
	count, tripped, err = store.RecordOpenAIAPIKeyHealthFailure(ctx, 42, 1, 3)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	require.False(t, tripped)
}
