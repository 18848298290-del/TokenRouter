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

// pttlFailureHook fails standalone PTTL commands while allowing the Lua script to run.
type pttlFailureHook struct{}

func (pttlFailureHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (pttlFailureHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func (pttlFailureHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "pttl" {
			return errors.New("PTTL unavailable")
		}
		return next(ctx, cmd)
	}
}

// TestFixedWindowKeyAndRetryAfterFallback checks the key and delay when PTTL fails.
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

func TestWindowTTLMillis(t *testing.T) {
	require.Equal(t, int64(1), windowTTLMillis(500*time.Microsecond))
	require.Equal(t, int64(1), windowTTLMillis(1500*time.Microsecond))
	require.Equal(t, int64(2), windowTTLMillis(2500*time.Microsecond))
}
