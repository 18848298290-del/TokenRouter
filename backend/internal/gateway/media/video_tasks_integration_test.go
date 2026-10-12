//go:build integration

package media

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// TestVideoTasksRedisOwnershipAndCompletion checks task ownership, billing claims, and release using isolated Redis.
func TestVideoTasksRedisOwnershipAndCompletion(t *testing.T) {
	ctx := context.Background()
	container, err := rediscontainer.Run(ctx, "redis:8.4-alpine")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)
	rdb := redis.NewClient(&redis.Options{Addr: host + ":" + port.Port()})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	store := rediscache.NewGatewayCache(rdb)
	billing, ok := store.(session.GrokVideoBillingCache)
	require.True(t, ok)
	tasks := NewVideoTasks(store, billing, VideoOptions{})
	group := int64(9)
	const task = "official-task"
	require.NoError(t, tasks.BindGrokMediaVideoRequestProvider(ctx, &group, task, 2, 3, 15))
	providerID, err := tasks.ResolveGrokMediaVideoRequestProvider(ctx, &group, task, 2, 3)
	require.NoError(t, err)
	require.EqualValues(t, 15, providerID)
	owner, err := tasks.ResolveGrokMediaVideoRequestGroup(ctx, task, 2, 3)
	require.NoError(t, err)
	require.EqualValues(t, 9, owner)
	restored, err := tasks.ResolveCompositeVideo(ctx, task, 2, 3, nil)
	require.NoError(t, err)
	require.EqualValues(t, 9, restored.GroupID)
	require.EqualValues(t, 15, restored.ProviderID)
	require.Equal(t, -1, restored.BindingIndex, "映射删除后仍按持久归属恢复最小查询投影")
	legacyTask := "old-without-owner"
	legacyHash := "openai:" + GrokMediaVideoRequestSessionHash(legacyTask, 2, 3)
	require.NoError(t, store.SetSessionProviderID(ctx, group, legacyHash, 15, VideoPendingTTL))
	restored, err = tasks.ResolveCompositeVideo(ctx, legacyTask, 2, 3, []VideoBinding{{GroupID: 5, Present: true}, {GroupID: 9, Present: true}})
	require.NoError(t, err)
	require.EqualValues(t, 15, restored.ProviderID)
	require.Equal(t, 1, restored.BindingIndex)

	other := int64(10)
	require.ErrorContains(t, tasks.BindGrokMediaVideoRequestProvider(ctx, &other, task, 2, 3, 16), "another group")
	_, err = tasks.ResolveGrokMediaVideoRequestGroup(ctx, task, 2, 4)
	require.Error(t, err, "不同 Key 不能读取原任务归属")
	key := "sticky_session:9:openai:" + GrokMediaVideoRequestSessionHash(task, 2, 3)
	require.Equal(t, "15", rdb.Get(ctx, key).Val())
	ttl, err := rdb.TTL(ctx, key).Result()
	require.NoError(t, err)
	require.InDelta(t, VideoPendingTTL.Seconds(), ttl.Seconds(), 2)
	created := time.Now().UTC().Add(-time.Minute)
	pending := GrokVideoPendingBilling{Model: "create-model", BillingModel: "bill-model", UpstreamModel: "upstream-model", VideoResolution: "720p", VideoDurationSeconds: 10, CreatedAt: created.Format(time.RFC3339Nano)}
	require.NoError(t, tasks.StoreGrokVideoPendingBilling(ctx, task, 2, 3, pending))
	saved, err := tasks.LoadGrokVideoPendingBilling(ctx, task, 2, 3)
	require.NoError(t, err)
	require.Equal(t, pending, *saved)
	status := &VideoCompletion{ResponseID: task, Model: "response-model", VideoCount: 1, ImageCount: 2, VideoDurationSeconds: 12}
	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if tasks.PrepareCompletion(ctx, 2, 3, task, status, time.Now, nil) != nil {
				winners.Add(1)
			}
		})
	}
	wg.Wait()
	require.EqualValues(t, 1, winners.Load())
	require.NoError(t, tasks.ReleaseGrokVideoBilling(ctx, task, 2, 3))
	result := tasks.PrepareCompletion(ctx, 2, 3, task, status, time.Now, nil)
	require.NotNil(t, result)
	require.Equal(t, "grok-video:"+task, result.RequestID)
	require.Equal(t, "response-model", result.Model)
	require.Equal(t, "bill-model", result.BillingModel)
	require.Equal(t, "720p", result.VideoResolution)
	require.Equal(t, 12, result.VideoDurationSeconds)
	require.Zero(t, result.ImageCount)
	require.GreaterOrEqual(t, result.Duration, time.Minute)
	require.Equal(t, 2, status.ImageCount, "不得修改调用方原观测")
	require.Nil(t, tasks.PrepareCompletion(ctx, 2, 3, "no-pending", &VideoCompletion{VideoCount: 1}, time.Now, nil))
	claimed, err := tasks.ClaimGrokVideoBilling(ctx, "no-pending", 2, 3)
	require.NoError(t, err)
	require.True(t, claimed, "缺少观测时不能提前消耗领取权")
	// Optional zero-value fields are omitted from JSON.
	data, err := json.Marshal(GrokVideoPendingBilling{Model: "old"})
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"old"}`, string(data))
}
