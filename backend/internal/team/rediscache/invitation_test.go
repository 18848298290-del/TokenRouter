package rediscache

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// TestTeamInvitationLimiterRecipientCooldown submits the same team mailbox
// with case and surrounding-space differences. The first request is allowed
// and the normalized repeat is denied with a positive recipient-window retry
// hint.
func TestTeamInvitationLimiterRecipientCooldown(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	// Release the Redis client after assertions and let RunT close its server;
	// client cleanup errors are discarded so they do not replace admission
	// observations.
	t.Cleanup(func() { _ = client.Close() })
	limiter := NewTeamInvitationLimiter(client)

	allowed, retryAfter, err := limiter.CheckAndRecord(context.Background(), 11, " Member@Example.com ")
	require.NoError(t, err)
	require.True(t, allowed)
	require.Zero(t, retryAfter)

	allowed, retryAfter, err = limiter.CheckAndRecord(context.Background(), 11, "member@example.com")
	require.NoError(t, err)
	require.False(t, allowed)
	require.Greater(t, retryAfter, time.Duration(0))
	require.LessOrEqual(t, retryAfter, TeamInvitationRecipientCooldown)
}

// TestTeamInvitationLimiterHourlyLimit admits distinct mailboxes until one
// team reaches its hourly ceiling. An additional mailbox is denied with a
// positive retry duration bounded by the hourly window.
func TestTeamInvitationLimiterHourlyLimit(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	// Release the Redis client after assertions and let RunT close its server;
	// client cleanup errors are discarded so they do not replace admission
	// observations.
	t.Cleanup(func() { _ = client.Close() })
	limiter := NewTeamInvitationLimiter(client)
	ctx := context.Background()

	for index := range TeamInvitationHourlyLimit {
		allowed, retryAfter, err := limiter.CheckAndRecord(ctx, 22, fmt.Sprintf("member-%d@example.com", index))
		require.NoError(t, err)
		require.True(t, allowed)
		require.Zero(t, retryAfter)
	}

	allowed, retryAfter, err := limiter.CheckAndRecord(ctx, 22, "overflow@example.com")
	require.NoError(t, err)
	require.False(t, allowed)
	require.Greater(t, retryAfter, time.Duration(0))
	require.LessOrEqual(t, retryAfter, TeamInvitationHourlyWindow)
}
