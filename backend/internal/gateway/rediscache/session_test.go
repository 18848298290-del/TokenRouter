package rediscache

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
)

// cyberRedisCommandHook captures actual command widths and write batch sizes
// for block tests.
type cyberRedisCommandHook struct {
	// mu protects mgetKeyCounts and setBatchSizes while Redis hooks append their
	// observations. Tests inspect those histories after their synchronous commands
	// finish, and the two slices represent different read and transaction widths.
	mu            sync.Mutex // command observation history access lock
	mgetKeyCounts []int      // observed MGET exact-key counts
	setBatchSizes []int      // observed pipeline SET command counts
}

// DialHook retains the supplied Redis dialing hook for these command-width
// assertions.
func (h *cyberRedisCommandHook) DialHook(next redis.DialHook) redis.DialHook { return next }

// ProcessHook wraps command execution to record MGET key count before
// delegation. The command-name argument is excluded, and a mutex guards
// history; returned command errors do not remove the attempted observation.
func (h *cyberRedisCommandHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "mget" {
			// Record actual key width before the Redis call, excluding its command
			// name; the shared lock keeps hook observations coherent when client
			// callbacks progress independently.
			h.mu.Lock()
			h.mgetKeyCounts = append(h.mgetKeyCounts, len(cmd.Args())-1)
			h.mu.Unlock()
		}
		return next(ctx, cmd)
	}
}

// ProcessPipelineHook records the number of SET commands in each nonempty
// write pipeline before delegation. Non-SET commands are ignored, the history
// is locked, and command errors retain their attempted batch observation.
func (h *cyberRedisCommandHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		setCount := 0
		for _, cmd := range cmds {
			if cmd.Name() == "set" {
				setCount++
			}
		}
		if setCount > 0 {
			// Record only SET width for this transaction before delegation, so
			// MULTI/EXEC framing and other commands do not inflate the exact-block
			// batch size asserted by tests.
			h.mu.Lock()
			h.setBatchSizes = append(h.setBatchSizes, setCount)
			h.mu.Unlock()
		}
		return next(ctx, cmds)
	}
}

// TestGatewayCacheCyberBlockWritesScopeAndExactKeysTogether stores two exact
// blocks and their coarse scope, then checks the final Redis state. Scope is
// active with positive retention, lookup returns the first blocked key, and
// exact keys share the same measured TTL.
func TestGatewayCacheCyberBlockWritesScopeAndExactKeysTogether(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	// Close the test Redis client after its assertions so command connections and
	// hook callbacks do not remain owned by this completed fixture. Cleanup
	// errors are discarded.
	t.Cleanup(func() { _ = client.Close() })
	store, ok := NewGatewayCache(client).(session.CyberSessionBlockStore)
	require.True(t, ok)

	ctx := context.Background()
	require.NoError(t, store.SetCyberSessionBlocked(ctx, "scope-1", []string{"block-1", "block-2"}, time.Minute))
	active, err := store.IsCyberSessionScopeActive(ctx, "scope-1")
	require.NoError(t, err)
	require.True(t, active)
	matched, err := store.FindCyberSessionBlocked(ctx, []string{"missing", "block-1", "block-2"})
	require.NoError(t, err)
	require.Equal(t, "block-1", matched)
	require.Greater(t, server.TTL(cyberSessionScopePrefix+"scope-1"), time.Duration(0))
	require.Equal(t, server.TTL(cyberSessionBlockPrefix+"block-1"), server.TTL(cyberSessionBlockPrefix+"block-2"))
}

// TestGatewayCacheCyberBlockCommandsAreBoundedAndLookupShortCircuits writes
// more than two full exact-key batches and puts one lookup match in the second
// batch. Hook observations must show bounded writes and stop reads before the
// third batch.
func TestGatewayCacheCyberBlockCommandsAreBoundedAndLookupShortCircuits(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	// Close the test Redis client after its assertions so command connections and
	// hook callbacks do not remain owned by this completed fixture. Cleanup
	// errors are discarded.
	t.Cleanup(func() { _ = client.Close() })
	hook := &cyberRedisCommandHook{}
	client.AddHook(hook)
	store, ok := NewGatewayCache(client).(session.CyberSessionBlockStore)
	require.True(t, ok)

	// Exercise two full batches and a shorter trailing write batch.
	keys := make([]string, cyberSessionRedisCommandMaxKeys*2+44)
	for i := range keys {
		keys[i] = "block-" + strconv.Itoa(i)
	}
	ctx := context.Background()
	require.NoError(t, store.SetCyberSessionBlocked(ctx, "large-scope", keys, time.Minute))
	require.Equal(t, []int{cyberSessionRedisCommandMaxKeys, cyberSessionRedisCommandMaxKeys, 44}, hook.setBatchSizes)

	lookup := make([]string, len(keys))
	for i := range lookup {
		lookup[i] = "missing-" + strconv.Itoa(i)
	}
	// Put the match in the second batch so trailing reads can be omitted.
	lookup[cyberSessionRedisCommandMaxKeys+3] = keys[cyberSessionRedisCommandMaxKeys+3]
	matched, err := store.FindCyberSessionBlocked(ctx, lookup)
	require.NoError(t, err)
	require.Equal(t, keys[cyberSessionRedisCommandMaxKeys+3], matched)
	require.Equal(t, []int{cyberSessionRedisCommandMaxKeys, cyberSessionRedisCommandMaxKeys}, hook.mgetKeyCounts)
}

// TestGatewayCacheLiveCallIdentityAndController saves a call and reads its
// selected identity/model metadata through another cache instance.
// Observer-to-proxy takeover, matching release, and the first-only close claim
// must follow stored controller transitions.
func TestGatewayCacheLiveCallIdentityAndController(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	cache, ok := NewGatewayCache(client).(session.LiveCallStore)
	require.True(t, ok)
	otherInstance, ok := NewGatewayCache(client).(session.LiveCallStore)
	require.True(t, ok)
	record := &session.LiveCallRecord{
		CallID:                "call_secret",
		CallHash:              HashLiveCallID("call_secret"),
		ProviderID:            11,
		APIKeyID:              22,
		UserID:                33,
		GroupID:               44,
		LeaseID:               "lease",
		Model:                 "gpt-live-test",
		RequestedModel:        "live-alias",
		UpstreamModel:         "gpt-live-upstream",
		ModelMappingChain:     "live-alias→gpt-live-test→gpt-live-upstream",
		APIKeyModelMapping:    map[string]string{"live-alias": "gpt-live-test"},
		AttestationCiphertext: "encrypted-attestation",
		CreatedAt:             time.Now(),
		ExpiresAt:             time.Now().Add(time.Hour),
		Controller:            session.LiveControllerPending,
	}
	require.NoError(t, cache.SaveLiveCall(context.Background(), record, time.Hour))

	loaded, err := otherInstance.GetLiveCall(context.Background(), record.CallHash)
	require.NoError(t, err)
	require.Equal(t, record.CallID, loaded.CallID)
	require.Equal(t, record.ProviderID, loaded.ProviderID)
	require.Equal(t, record.AttestationCiphertext, loaded.AttestationCiphertext)
	require.Equal(t, record.RequestedModel, loaded.RequestedModel)
	require.Equal(t, record.UpstreamModel, loaded.UpstreamModel)
	require.Equal(t, record.ModelMappingChain, loaded.ModelMappingChain)
	require.Equal(t, record.APIKeyModelMapping, loaded.APIKeyModelMapping)

	claimed, err := cache.ClaimLiveController(context.Background(), record.CallHash, session.LiveControllerObserver, "observer-1")
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = cache.ClaimLiveController(context.Background(), record.CallHash, session.LiveControllerProxy, "proxy-1")
	require.NoError(t, err)
	require.True(t, claimed)
	controller, err := cache.GetLiveController(context.Background(), record.CallHash)
	require.NoError(t, err)
	require.Equal(t, session.LiveControllerProxy, controller)

	released, err := cache.ReleaseLiveController(context.Background(), record.CallHash, "proxy-1")
	require.NoError(t, err)
	require.True(t, released)
	closed, err := cache.MarkLiveCallClosed(context.Background(), record.CallHash, time.Hour)
	require.NoError(t, err)
	require.True(t, closed)
	closed, err = cache.MarkLiveCallClosed(context.Background(), record.CallHash, time.Hour)
	require.NoError(t, err)
	require.False(t, closed)
}

// TestGatewayCacheReasoningContentRoundTrip stores and retrieves reasoning
// text by item identifier. A missing key and blank identifier must both return
// the recovery not-found sentinel rather than cached text.
func TestGatewayCacheReasoningContentRoundTrip(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	cache, ok := NewGatewayCache(client).(session.ReasoningContentCache)
	require.True(t, ok)

	ctx := context.Background()
	require.NoError(t, cache.SetReasoningContent(ctx, "item_reasoning", "cached thought", time.Hour))
	value, err := cache.GetReasoningContent(ctx, "item_reasoning")
	require.NoError(t, err)
	require.Equal(t, "cached thought", value)

	_, err = cache.GetReasoningContent(ctx, "missing")
	require.ErrorIs(t, err, session.ErrReasoningContentNotFound)
	_, err = cache.GetReasoningContent(ctx, "")
	require.ErrorIs(t, err, session.ErrReasoningContentNotFound)
}
