package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Provider migration fixtures combine string and sorted-set state with expiry
// and unrelated namespaces. Collision recovery checks that completed earlier
// prefix work survives and conflicting values remain until a caller removes
// the target, after which retry migrates the retained source.

// TestProviderMigrationPreservesStateAndTTL supplies expiring string and
// sorted-set account keys alongside unrelated state. It requires renamed
// values, scores, and TTLs, retained unrelated keys, the removed string source, and a
// successful repeated migration.
func TestProviderMigrationPreservesStateAndTTL(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	require.NoError(t, client.Set(ctx, "temp_unsched:account:7", `{"reason":"fixture"}`, time.Minute).Err())
	require.NoError(t, client.ZAdd(ctx, "session_limit:account:7", redis.Z{Score: 42, Member: "session"}).Err())
	require.NoError(t, client.Expire(ctx, "session_limit:account:7", 2*time.Minute).Err())
	require.NoError(t, client.Set(ctx, "sticky_session:1:fixture", 7, time.Minute).Err())
	require.NoError(t, client.Set(ctx, "unrelated:account:7", "keep", 0).Err())
	require.NoError(t, MigrateProviderNames(ctx, client))
	require.Equal(t, `{"reason":"fixture"}`, client.Get(ctx, "temp_unsched:provider:7").Val())
	require.Equal(t, time.Minute, client.PTTL(ctx, "temp_unsched:provider:7").Val())
	require.Equal(t, float64(42), client.ZScore(ctx, "session_limit:provider:7", "session").Val())
	require.Equal(t, 2*time.Minute, client.PTTL(ctx, "session_limit:provider:7").Val())
	require.Equal(t, "7", client.Get(ctx, "sticky_session:1:fixture").Val())
	require.Equal(t, "keep", client.Get(ctx, "unrelated:account:7").Val())
	require.False(t, server.Exists("temp_unsched:account:7"))
	require.NoError(t, MigrateProviderNames(ctx, client))
}

// TestProviderMigrationResumesWithoutOverwritingConflict creates a target
// collision after an earlier prefix can migrate. It requires both conflicting
// values and the earlier renamed value to survive, then verifies retry and retained
// expiry after target removal.
func TestProviderMigrationResumesWithoutOverwritingConflict(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	require.NoError(t, client.Set(ctx, "concurrency:account:1", "first", 0).Err())
	require.NoError(t, client.Set(ctx, "window_cost:account:2", "old", time.Minute).Err())
	require.NoError(t, client.Set(ctx, "window_cost:provider:2", "new", time.Minute).Err())
	require.ErrorContains(t, MigrateProviderNames(ctx, client), "conflict")
	require.Equal(t, "first", client.Get(ctx, "concurrency:provider:1").Val())
	require.Equal(t, "old", client.Get(ctx, "window_cost:account:2").Val())
	require.Equal(t, "new", client.Get(ctx, "window_cost:provider:2").Val())
	require.NoError(t, client.Del(ctx, "window_cost:provider:2").Err())
	require.NoError(t, MigrateProviderNames(ctx, client))
	require.Equal(t, "old", client.Get(ctx, "window_cost:provider:2").Val())
	server.FastForward(time.Minute)
	require.False(t, server.Exists("window_cost:provider:2"))
}
