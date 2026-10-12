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

func newTempUnschedCacheTest(t *testing.T) (provider.TempUnschedCache, *redis.Client, *miniredis.Miniredis) {
	t.Helper()

	// Each test gets an isolated Redis instance so provider keys and TTLs cannot interfere.
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
	})

	return NewTempUnschedCache(client), client, server
}

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

	// Advance beyond the old deadline to check that the longer state also extends Redis expiry.
	server.FastForward(150 * time.Second)

	got, err := cache.GetTempUnsched(ctx, 102)
	require.NoError(t, err)
	require.Equal(t, longer, got)
}

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

func TestTempUnschedCacheMalformedStateReturnsClearError(t *testing.T) {
	cache, client, _ := newTempUnschedCacheTest(t)
	ctx := context.Background()
	require.NoError(t, client.Set(ctx, tempUnschedPrefix+"104", "{invalid-json", time.Minute).Err())

	got, err := cache.GetTempUnsched(ctx, 104)

	require.Nil(t, got)
	require.ErrorContains(t, err, "unmarshal state")
}

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

func TestOpenAIAPIKeyHealthCacheTripsWithinRollingWindow(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
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

func TestOpenAIAPIKeyHealthCacheDropsFailuresOutsideRollingWindow(t *testing.T) {
	server := miniredis.RunT(t)
	now := time.Unix(1_700_000_000, 0)
	server.SetTime(now)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, ok := NewTempUnschedCache(client).(provider.OpenAIAPIKeyHealthCache)
	require.True(t, ok)

	ctx := context.Background()
	count, tripped, err := store.RecordOpenAIAPIKeyHealthFailure(ctx, 42, 1, 3)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	require.False(t, tripped)

	server.SetTime(now.Add(61 * time.Second))
	count, tripped, err = store.RecordOpenAIAPIKeyHealthFailure(ctx, 42, 1, 3)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	require.False(t, tripped)
}
