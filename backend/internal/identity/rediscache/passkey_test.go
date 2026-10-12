package rediscache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// TestPasskeySessionStoreConsumesSessionOnce stores a login ceremony and consumes
// it through Redis GETDEL. The recovered kind and user match the saved record,
// and reuse returns ErrPasskeySession for the consumed token.
func TestPasskeySessionStoreConsumesSessionOnce(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewPasskeySessionStore(client)
	ctx := context.Background()

	token, err := store.Store(ctx, &identity.PasskeySession{Kind: "login", UserID: 42}, time.Minute)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	consumed, err := store.Consume(ctx, token)
	require.NoError(t, err)
	require.Equal(t, "login", consumed.Kind)
	require.EqualValues(t, 42, consumed.UserID)

	_, err = store.Consume(ctx, token)
	require.ErrorIs(t, err, identity.ErrPasskeySession)
}

// TestPasskeySessionStoreExpiresSession advances the Redis fixture to the stored
// ceremony's expiry. Consume returns ErrPasskeySession, keeping an expired
// registration challenge from being recovered for a finish request.
func TestPasskeySessionStoreExpiresSession(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewPasskeySessionStore(client)
	ctx := context.Background()

	token, err := store.Store(ctx, &identity.PasskeySession{Kind: "registration", UserID: 7}, time.Minute)
	require.NoError(t, err)
	// Advance Redis expiry independently of wall-clock waiting so the finish
	// lookup observes a missing record at the exact supplied one-minute lifetime.
	server.FastForward(time.Minute)

	_, err = store.Consume(ctx, token)
	require.ErrorIs(t, err, identity.ErrPasskeySession)
}
