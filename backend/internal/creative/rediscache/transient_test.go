package rediscache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/creative"
)

func newCreativeTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return server, client
}

func newCreativeTestTransientStore(client *redis.Client) creative.CreativeTransientStore {
	return NewCreativeTransientStore(client, &TransientOptions{TransientTTLSeconds: 60})
}

// TestCreativeTransientStoreRoundTrip covers payload, media, and deletion operations.
func TestCreativeTransientStoreRoundTrip(t *testing.T) {
	_, client := newCreativeTestRedis(t)
	store := newCreativeTestTransientStore(client)
	ctx := context.Background()
	runID := "crun_testroundtrip0123"

	payload := &creative.CreativeRunPayload{
		RunID:       runID,
		UserID:      7,
		GroupID:     12,
		APIKeyID:    900,
		Model:       "gemini-3.1-flash-image",
		Operation:   creative.CreativeOperationInpaint,
		Prompt:      "临时 prompt",
		SourceCount: 2,
		HasMask:     true,
	}
	require.NoError(t, store.SavePayload(ctx, runID, payload))
	loaded, err := store.LoadPayload(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, payload.Prompt, loaded.Prompt)

	require.NoError(t, store.SaveInput(ctx, runID, 0, []byte("img0")))
	require.NoError(t, store.SaveInput(ctx, runID, 1, []byte("img1")))
	inputs, err := store.LoadInputs(ctx, runID, 2)
	require.NoError(t, err)
	require.Equal(t, [][]byte{[]byte("img0"), []byte("img1")}, inputs)

	require.NoError(t, store.SaveMask(ctx, runID, []byte("mask")))
	mask, err := store.LoadMask(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, []byte("mask"), mask)

	require.NoError(t, store.SaveOutput(ctx, runID, 0, []byte("out0"), time.Minute))
	output, err := store.LoadOutput(ctx, runID, 0)
	require.NoError(t, err)
	require.Equal(t, []byte("out0"), output)

	// Deleting an output twice succeeds.
	require.NoError(t, store.DeleteOutput(ctx, runID, 0))
	require.NoError(t, store.DeleteOutput(ctx, runID, 0))
	_, err = store.LoadOutput(ctx, runID, 0)
	require.Error(t, err)

	// Task cleanup removes the remaining payload, mask, and inputs.
	require.NoError(t, store.DeleteRunTransient(ctx, runID, 2, 1))
	_, err = store.LoadPayload(ctx, runID)
	require.Error(t, err)
	_, err = store.LoadMask(ctx, runID)
	require.Error(t, err)
	_, err = store.LoadInputs(ctx, runID, 2)
	require.Error(t, err)
}

// TestCreativeTransientStoreExpiry checks that payloads and outputs become unreadable after expiry.
func TestCreativeTransientStoreExpiry(t *testing.T) {
	server, client := newCreativeTestRedis(t)
	store := newCreativeTestTransientStore(client)
	ctx := context.Background()
	runID := "crun_testexpiry012345"

	require.NoError(t, store.SavePayload(ctx, runID, &creative.CreativeRunPayload{RunID: runID, Prompt: "x"}))
	require.NoError(t, store.SaveOutput(ctx, runID, 0, []byte("out"), time.Minute))

	// Advance beyond the configured 60-second TTL.
	server.FastForward(61 * time.Second)

	_, err := store.LoadPayload(ctx, runID)
	require.ErrorIs(t, err, creative.ErrCreativeTransientFailed)
	_, err = store.LoadOutput(ctx, runID, 0)
	require.ErrorIs(t, err, creative.ErrCreativeTransientFailed)
}

// TestCreativeTransientStoreInputsMissing checks failure when one requested source image is absent.
func TestCreativeTransientStoreInputsMissing(t *testing.T) {
	_, client := newCreativeTestRedis(t)
	store := newCreativeTestTransientStore(client)
	ctx := context.Background()
	runID := "crun_testmissing012345"

	require.NoError(t, store.SaveInput(ctx, runID, 0, []byte("img0")))
	_, err := store.LoadInputs(ctx, runID, 2)
	require.Error(t, err)
}

// TestCreativeQueueEnqueueReserveAck covers queue reservation, duplicate rejection, and acknowledgment.
func TestCreativeQueueEnqueueReserveAck(t *testing.T) {
	_, client := newCreativeTestRedis(t)
	queue := NewCreativeQueue(client, &QueueOptions{
		QueueReadyKey:      "creative:queue:ready",
		QueueDelayedKey:    "creative:queue:delayed",
		QueueActiveKey:     "creative:queue:active",
		InflightKeyPrefix:  "creative:queue:inflight:",
		LockKeyPrefix:      "creative:queue:lock:",
		InflightTTLSeconds: 3600,
		JobLockTTLSeconds:  60,
	})
	ctx := context.Background()
	runID := "crun_testqueue0123456"

	require.NoError(t, queue.Enqueue(ctx, runID))
	// Duplicate enqueue attempts return a conflict.
	require.ErrorIs(t, queue.Enqueue(ctx, runID), creative.ErrCreativeAlreadyQueued)

	reserved, err := queue.Reserve(ctx, time.Second)
	require.NoError(t, err)
	require.Equal(t, runID, reserved.RunID)

	// A reserved task is no longer available to another reservation.
	_, err = queue.Reserve(ctx, 50*time.Millisecond)
	require.ErrorIs(t, err, creative.ErrCreativeQueueEmpty)

	// Invalid task IDs are rejected before enqueueing.
	require.ErrorIs(t, queue.Enqueue(ctx, "bad-id"), creative.ErrInvalidCreativeQueuePayload)

	// Acknowledgment leaves no delayed task to return to ready.
	require.NoError(t, queue.Ack(ctx, runID, reserved.LeaseToken))
	moved, err := queue.MoveDueDelayedToReady(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 0, moved)
}

// TestCreativeQueueRecoverStaleActive checks that fresh tasks stay active and a zero threshold is rejected.
func TestCreativeQueueRecoverStaleActive(t *testing.T) {
	_, client := newCreativeTestRedis(t)
	queue := NewCreativeQueue(client, &QueueOptions{
		QueueReadyKey:      "creative:queue:ready",
		QueueDelayedKey:    "creative:queue:delayed",
		QueueActiveKey:     "creative:queue:active",
		InflightKeyPrefix:  "creative:queue:inflight:",
		LockKeyPrefix:      "creative:queue:lock:",
		InflightTTLSeconds: 3600,
		JobLockTTLSeconds:  60,
	})
	ctx := context.Background()
	runID := "crun_testrecover012345"

	require.NoError(t, queue.Enqueue(ctx, runID))
	reserved, err := queue.Reserve(ctx, time.Second)
	require.NoError(t, err)
	require.Equal(t, runID, reserved.RunID)

	// A freshly reserved task is not stale.
	recovered, err := queue.RecoverStaleActive(ctx, time.Minute, 10)
	require.NoError(t, err)
	require.Equal(t, 0, recovered)

	// A zero stale threshold is invalid.
	_, err = queue.RecoverStaleActive(ctx, 0, 10)
	require.ErrorIs(t, err, creative.ErrInvalidCreativeQueuePayload)
}

// TestCreativeQueueLeaseFencingOldWorkerCannotAck checks that recovery invalidates the old worker's token.
func TestCreativeQueueLeaseFencingOldWorkerCannotAck(t *testing.T) {
	_, client := newCreativeTestRedis(t)
	queue := NewCreativeQueue(client, &QueueOptions{
		QueueReadyKey: "creative:queue:ready", QueueDelayedKey: "creative:queue:delayed", QueueActiveKey: "creative:queue:active",
		InflightKeyPrefix: "creative:queue:inflight:", InflightTTLSeconds: 3600,
	})
	ctx := context.Background()
	runID := "crun_testfencing012345"
	require.NoError(t, queue.Enqueue(ctx, runID))
	first, err := queue.Reserve(ctx, time.Second)
	require.NoError(t, err)
	// Age worker A's heartbeat so recovery moves the task and clears its lease token atomically.
	require.NoError(t, client.ZAdd(ctx, "creative:queue:active", redis.Z{Score: float64(time.Now().Add(-time.Hour).UnixMilli()), Member: runID}).Err())
	moved, err := queue.RecoverStaleActive(ctx, time.Minute, 10)
	require.NoError(t, err)
	require.Equal(t, 1, moved)
	second, err := queue.Reserve(ctx, time.Second)
	require.NoError(t, err)
	require.NotEqual(t, first.LeaseToken, second.LeaseToken)
	require.ErrorIs(t, queue.Ack(ctx, runID, first.LeaseToken), creative.ErrCreativeLeaseLost)
	require.NoError(t, queue.Ack(ctx, runID, second.LeaseToken))
}
