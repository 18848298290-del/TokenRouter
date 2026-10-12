package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// The unit fixture allows Lua counting while rejecting a standalone PTTL
// command, separating admission from retry-delay lookup. It checks the exact
// namespaced key and full-window delay after that lookup fails, then verifies
// millisecond expiry conversion using small duration inputs.

// pttlFailureHook fails standalone PTTL commands while allowing the Lua script to run.
type pttlFailureHook struct{}

// DialHook passes the fixture's connection setup to its original Redis dial
// hook.
func (pttlFailureHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

// ProcessPipelineHook leaves pipeline execution unchanged so only standalone
// PTTL is rejected.
func (pttlFailureHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// ProcessHook wraps next and returns the fixture error for standalone pttl
// commands. Other commands delegate unchanged, allowing script-internal TTL
// calls to execute while Allow's separate delay lookup fails.
func (pttlFailureHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "pttl" {
			return errors.New("PTTL unavailable")
		}
		return next(ctx, cmd)
	}
}

// TestFixedWindowKeyAndRetryAfterFallback counts two requests while an
// injected hook rejects standalone PTTL. It requires the exact prefixed Redis
// key, first admission, second rejection, and a full-window retry delay.
func TestFixedWindowKeyAndRetryAfterFallback(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
	})
	client.AddHook(pttlFailureHook{})
	limiter := NewFixedWindowLimiter(client, "rate_limit:")
	ctx := t.Context()
	allowed, count, retry, err := limiter.Allow(ctx, "panel:global:user:42", 1, time.Minute)
	require.NoError(t, err)
	require.True(t, allowed)
	require.Equal(t, int64(1), count)
	require.Zero(t, retry)
	value, err := server.Get("rate_limit:panel:global:user:42")
	require.NoError(t, err)
	require.Equal(t, "1", value)
	allowed, count, retry, err = limiter.Allow(ctx, "panel:global:user:42", 1, time.Minute)
	require.NoError(t, err)
	require.False(t, allowed)
	require.Equal(t, int64(2), count)
	require.Equal(t, time.Minute, retry)
}

// TestWindowTTLMillis supplies half, one-and-a-half, and two-and-a-half
// millisecond durations. It requires a one-millisecond minimum and
// whole-millisecond truncation for the positive inputs used to expire
// counters.
func TestWindowTTLMillis(t *testing.T) {
	require.Equal(t, int64(1), windowTTLMillis(500*time.Microsecond))
	require.Equal(t, int64(1), windowTTLMillis(1500*time.Microsecond))
	require.Equal(t, int64(2), windowTTLMillis(2500*time.Microsecond))
}
