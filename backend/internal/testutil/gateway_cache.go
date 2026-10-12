package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	gatewayredis "github.com/TokenFlux/TokenRouter/internal/gateway/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
)

var _ session.GatewayCache = StubGatewayCache{}

// StubGatewayCache returns empty session data and accepts ownership and billing claims.
type StubGatewayCache struct{}

// NewRedisGatewayCache creates a miniredis-backed cache and registers connection cleanup.
func NewRedisGatewayCache(t *testing.T) session.GatewayCache {
	t.Helper()

	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })

	return gatewayredis.NewGatewayCache(redisClient)
}

// GetSessionProviderID returns zero to indicate a missing session provider.
func (c StubGatewayCache) GetSessionProviderID(_ context.Context, _ int64, _ string) (int64, error) {
	return 0, nil
}

// SetSessionProviderID reports success without storing the provider ID.
func (c StubGatewayCache) SetSessionProviderID(_ context.Context, _ int64, _ string, _ int64, _ time.Duration) error {
	return nil
}

// RefreshSessionTTL reports success without storing session state.
func (c StubGatewayCache) RefreshSessionTTL(_ context.Context, _ int64, _ string, _ time.Duration) error {
	return nil
}

// DeleteSessionProviderID reports success without storing session state.
func (c StubGatewayCache) DeleteSessionProviderID(_ context.Context, _ int64, _ string) error {
	return nil
}

// SetSessionOwnerGroupID accepts the owner group without storing it.
func (c StubGatewayCache) SetSessionOwnerGroupID(_ context.Context, _ int64, _, _ string, _ int64, _ time.Duration) (bool, error) {
	return true, nil
}

// GetSessionOwnerGroupID returns zero to indicate a missing owner group.
func (c StubGatewayCache) GetSessionOwnerGroupID(_ context.Context, _ int64, _, _ string) (int64, error) {
	return 0, nil
}

// RefreshSessionOwnerTTL reports success without storing owner state.
func (c StubGatewayCache) RefreshSessionOwnerTTL(_ context.Context, _ int64, _, _ string, _ time.Duration) error {
	return nil
}

// SetGrokVideoPendingBilling reports success without storing billing data.
func (c StubGatewayCache) SetGrokVideoPendingBilling(_ context.Context, _ string, _ []byte, _ time.Duration) error {
	return nil
}

// GetGrokVideoPendingBilling returns nil to indicate missing billing data.
func (c StubGatewayCache) GetGrokVideoPendingBilling(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

// ClaimGrokVideoBilled accepts the billing claim without storing it.
func (c StubGatewayCache) ClaimGrokVideoBilled(_ context.Context, _ string, _ time.Duration) (bool, error) {
	return true, nil
}

// ReleaseGrokVideoBilled reports success without storing billing claims.
func (c StubGatewayCache) ReleaseGrokVideoBilled(_ context.Context, _ string) error {
	return nil
}
