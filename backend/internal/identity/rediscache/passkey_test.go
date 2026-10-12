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

	// A token consumed through GETDEL cannot complete authentication again.
	_, err = store.Consume(ctx, token)
	require.ErrorIs(t, err, identity.ErrPasskeySession)
}

func TestPasskeySessionStoreExpiresSession(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewPasskeySessionStore(client)
	ctx := context.Background()

	token, err := store.Store(ctx, &identity.PasskeySession{Kind: "registration", UserID: 7}, time.Minute)
	require.NoError(t, err)
	server.FastForward(time.Minute)

	_, err = store.Consume(ctx, token)
	require.ErrorIs(t, err, identity.ErrPasskeySession)
}
