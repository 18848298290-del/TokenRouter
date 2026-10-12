//go:build integration

package rediscache

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache/codec"
)

// TestBucketLeaseExpiredOwnerCannotReleaseSuccessor checks that an expired owner's release leaves the successor lock intact.
func TestBucketLeaseExpiredOwnerCannotReleaseSuccessor(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := NewSnapshotCache(rdb, codec.ProviderCodec{})
	bucket := scheduler.SchedulerBucket{GroupID: 91001, Platform: capability.PlatformOpenAI, Mode: scheduler.SchedulerModeSingle}
	first, acquired, err := cache.AcquireBucketLease(ctx, bucket, 20*time.Millisecond)
	require.NoError(t, err)
	require.True(t, acquired)
	time.Sleep(40 * time.Millisecond)
	second, acquired, err := cache.AcquireBucketLease(ctx, bucket, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	key := "sched:v4:lock:" + bucket.String()
	owner, err := rdb.Get(ctx, key).Result()
	require.NoError(t, err)
	require.ErrorIs(t, first.Release(ctx), scheduler.ErrBucketLeaseLost)
	current, err := rdb.Get(ctx, key).Result()
	require.NoError(t, err)
	require.Equal(t, owner, current)
	_, acquired, err = cache.AcquireBucketLease(ctx, bucket, time.Minute)
	require.NoError(t, err)
	require.False(t, acquired)
	require.NoError(t, second.Release(ctx))
	require.NoError(t, second.Release(ctx))
	count, err := rdb.Exists(ctx, key).Result()
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestSchedulerCacheSnapshotUsesSlimMetadataButKeepsFullProvider(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := NewSchedulerCache(rdb)

	bucket := scheduler.SchedulerBucket{GroupID: 2, Platform: capability.PlatformGemini, Mode: scheduler.SchedulerModeSingle}
	now := time.Now().UTC().Truncate(time.Second)
	limitReset := now.Add(10 * time.Minute)
	overloadUntil := now.Add(2 * time.Minute)
	tempUnschedUntil := now.Add(3 * time.Minute)
	windowEnd := now.Add(5 * time.Hour)

	provider := providercore.Record{
		ID:          101,
		Name:        "gemini-heavy",
		Platform:    capability.PlatformGemini,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 3,
		Priority:    7,
		LastUsedAt:  &now,
		Credentials: map[string]any{
			"api_key":         "gemini-api-key",
			"access_token":    "secret-access-token",
			"project_id":      "proj-1",
			"oauth_type":      "ai_studio",
			"model_mapping":   map[string]any{"gemini-2.5-pro": "gemini-2.5-pro"},
			"model_whitelist": []any{"gemini-2.5-pro"},
			"huge_blob":       strings.Repeat("x", 4096),
		},
		Extra: map[string]any{
			"mixed_scheduling":             true,
			"window_cost_limit":            12.5,
			"window_cost_sticky_reserve":   8.0,
			"max_sessions":                 4,
			"session_idle_timeout_minutes": 11,
			"unused_large_field":           strings.Repeat("y", 4096),
		},
		RateLimitResetAt:       &limitReset,
		OverloadUntil:          &overloadUntil,
		TempUnschedulableUntil: &tempUnschedUntil,
		SessionWindowStart:     &now,
		SessionWindowEnd:       &windowEnd,
		SessionWindowStatus:    "active",
		GroupIDs:               []int64{bucket.GroupID},
		ProviderGroups: []providercore.GroupMembership{
			{
				ProviderID: 101,
				GroupID:    bucket.GroupID,
				Group:      &accessview.GroupConfig{ID: bucket.GroupID, Name: "gemini-group"},
			},
		},
	}

	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []providercore.Record{provider}))

	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)

	got := snapshot[0]
	require.NotNil(t, got)
	require.Equal(t, "gemini-api-key", got.GetCredential("api_key"))
	require.Equal(t, "proj-1", got.GetCredential("project_id"))
	require.Equal(t, "ai_studio", got.GetCredential("oauth_type"))
	require.NotEmpty(t, providercore.ResolveModelMapping(got, provideradapter.ModelDefaults()))
	require.Equal(t, []any{"gemini-2.5-pro"}, got.Credentials["model_whitelist"])
	require.Empty(t, got.GetCredential("access_token"))
	require.Empty(t, got.GetCredential("huge_blob"))
	require.NotContains(t, got.Extra, "mixed_scheduling")
	require.Equal(t, 12.5, (&providercore.RuntimeConfig{Extra: got.Extra}).GetWindowCostLimit())
	require.Equal(t, 8.0, (&providercore.RuntimeConfig{Extra: got.Extra}).GetWindowCostStickyReserve())
	require.Equal(t, 4, (&providercore.RuntimeConfig{Extra: got.Extra}).GetMaxSessions())
	require.Equal(t, 11, (&providercore.RuntimeConfig{Extra: got.Extra}).GetSessionIdleTimeoutMinutes())
	require.Nil(t, got.Extra["unused_large_field"])
	require.Equal(t, []int64{bucket.GroupID}, got.GroupIDs)
	require.Len(t, got.ProviderGroups, 1)
	require.Equal(t, provider.ID, got.ProviderGroups[0].ProviderID)
	require.Equal(t, bucket.GroupID, got.ProviderGroups[0].GroupID)
	require.Nil(t, got.ProviderGroups[0].Group)

	full, err := cache.GetProvider(ctx, provider.ID)
	require.NoError(t, err)
	require.NotNil(t, full)
	require.Equal(t, "secret-access-token", full.GetCredential("access_token"))
	require.Equal(t, strings.Repeat("x", 4096), full.GetCredential("huge_blob"))
	require.Len(t, full.ProviderGroups, 1)
	require.NotNil(t, full.ProviderGroups[0].Group)
}

func TestSchedulerCacheRetireAndReopenFencesOldEpochIntegration(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := NewSchedulerCache(rdb)
	bucket := scheduler.SchedulerBucket{GroupID: 77, Platform: capability.PlatformAntigravity, Mode: scheduler.SchedulerModeForced}
	provider := providercore.Record{ID: 7701, Platform: capability.PlatformAntigravity, Type: capability.ProviderTypeOAuth}

	oldToken, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, oldToken, []providercore.Record{provider}))
	require.NoError(t, cache.RetireBucket(ctx, bucket))
	require.NoError(t, cache.RetireBucket(ctx, bucket))

	_, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.False(t, hit)
	_, err = cache.CaptureBucketWriteToken(ctx, bucket)
	require.ErrorIs(t, err, scheduler.ErrSchedulerBucketRetired)
	require.ErrorIs(t, cache.SetSnapshot(ctx, bucket, oldToken, []providercore.Record{provider}), scheduler.ErrSchedulerBucketRetired)

	newToken, err := cache.ReopenBucket(ctx, bucket)
	require.NoError(t, err)
	require.Greater(t, newToken.Epoch, oldToken.Epoch)
	require.ErrorIs(t, cache.SetSnapshot(ctx, bucket, oldToken, []providercore.Record{provider}), scheduler.ErrSchedulerBucketWriteFenced)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, newToken, []providercore.Record{provider}))

	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)
	require.Equal(t, provider.ID, snapshot[0].ID)
}

func TestSchedulerCacheGroupLifecycleLeaseOwnerAndTTLIntegration(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := NewSchedulerCache(rdb)
	const groupID int64 = 78
	const ttl = 500 * time.Millisecond

	first, acquired, err := cache.TryAcquireGroupLifecycleLease(ctx, groupID, ttl)
	require.NoError(t, err)
	require.True(t, acquired)
	pttl, err := rdb.PTTL(ctx, schedulerGroupLifecycleLockKey(groupID)).Result()
	require.NoError(t, err)
	require.Positive(t, pttl)
	require.LessOrEqual(t, pttl, ttl)

	var second scheduler.SchedulerGroupLifecycleLease
	require.Eventually(t, func() bool {
		var acquireErr error
		second, acquired, acquireErr = cache.TryAcquireGroupLifecycleLease(ctx, groupID, time.Minute)
		return acquireErr == nil && acquired
	}, 5*time.Second, 20*time.Millisecond)
	require.NotEqual(t, first.OwnerToken, second.OwnerToken)

	require.ErrorIs(t, cache.ReleaseGroupLifecycleLease(ctx, first), scheduler.ErrSchedulerGroupLifecycleLeaseLost)
	_, acquired, err = cache.TryAcquireGroupLifecycleLease(ctx, groupID, time.Minute)
	require.NoError(t, err)
	require.False(t, acquired, "a stale release must not delete the successor lease")

	require.NoError(t, cache.ReleaseGroupLifecycleLease(ctx, second))
	require.ErrorIs(t, cache.ReleaseGroupLifecycleLease(ctx, second), scheduler.ErrSchedulerGroupLifecycleLeaseLost)
	third, acquired, err := cache.TryAcquireGroupLifecycleLease(ctx, groupID, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.True(t, third.ValidFor(groupID))
	require.NoError(t, cache.ReleaseGroupLifecycleLease(ctx, third))
}

func NewSchedulerCache(rdb *redis.Client) *schedulerCache {
	return &schedulerCache{NewSnapshotCache(rdb, codec.ProviderCodec{})}
}
