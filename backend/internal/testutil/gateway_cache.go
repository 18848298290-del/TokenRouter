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

// Gateway session tests can choose between fixed empty cache observations and a
// miniredis-backed adapter. The stub keeps session affinity, ownership, and
// pending billing data absent while accepting claims; NewRedisGatewayCache
// supplies the stateful Redis implementation when the tested path needs stored
// values.

// The interface assertion keeps StubGatewayCache available wherever gateway
// session fixtures require the methods defined by session.GatewayCache,
// including ownership and billing claims.
var _ session.GatewayCache = StubGatewayCache{}

// StubGatewayCache supplies empty session data and fixed successful claims for
// fixtures. Inputs and cancellation are ignored, and writes do not affect later
// reads, so stateful session tests use another cache.
type StubGatewayCache struct{}

// NewRedisGatewayCache constructs a gateway cache over a per-test miniredis
// server and client. Test cleanup closes the client before the server cleanup
// registered by RunT; client-close errors are discarded by this helper.
func NewRedisGatewayCache(t *testing.T) session.GatewayCache {
	t.Helper()

	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	// Register after RunT so reverse cleanup order closes the client before its
	// server. This helper discards client-close errors while the server fixture owns termination.
	t.Cleanup(func() { _ = redisClient.Close() })

	return gatewayredis.NewGatewayCache(redisClient)
}

// GetSessionProviderID returns the missing-provider sentinel zero with a nil
// fixture error.
func (c StubGatewayCache) GetSessionProviderID(_ context.Context, _ int64, _ string) (int64, error) {
	return 0, nil
}

// SetSessionProviderID accepts an affinity write and returns a nil fixture
// error.
func (c StubGatewayCache) SetSessionProviderID(_ context.Context, _ int64, _ string, _ int64, _ time.Duration) error {
	return nil
}

// RefreshSessionTTL accepts session expiration refreshes and returns a nil
// fixture error.
func (c StubGatewayCache) RefreshSessionTTL(_ context.Context, _ int64, _ string, _ time.Duration) error {
	return nil
}

// DeleteSessionProviderID accepts affinity deletion and returns a nil fixture
// error.
func (c StubGatewayCache) DeleteSessionProviderID(_ context.Context, _ int64, _ string) error {
	return nil
}

// SetSessionOwnerGroupID returns a successful ownership claim for each fixture
// request. It stores no owner, so GetSessionOwnerGroupID continues returning
// zero and repeated claims also receive true.
func (c StubGatewayCache) SetSessionOwnerGroupID(_ context.Context, _ int64, _, _ string, _ int64, _ time.Duration) (bool, error) {
	return true, nil
}

// GetSessionOwnerGroupID returns the missing-owner sentinel zero with a nil
// fixture error.
func (c StubGatewayCache) GetSessionOwnerGroupID(_ context.Context, _ int64, _, _ string) (int64, error) {
	return 0, nil
}

// RefreshSessionOwnerTTL accepts owner expiration refreshes and returns a nil
// fixture error.
func (c StubGatewayCache) RefreshSessionOwnerTTL(_ context.Context, _ int64, _, _ string, _ time.Duration) error {
	return nil
}

// SetGrokVideoPendingBilling accepts pending billing data and returns a nil
// fixture error.
func (c StubGatewayCache) SetGrokVideoPendingBilling(_ context.Context, _ string, _ []byte, _ time.Duration) error {
	return nil
}

// GetGrokVideoPendingBilling returns a nil payload indicating absent pending
// billing data. Earlier writes leave that result unchanged because the fixed
// stub retains no payload or expiration state.
func (c StubGatewayCache) GetGrokVideoPendingBilling(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

// ClaimGrokVideoBilled returns a successful billed claim for every fixture
// call. Repeated calls keep succeeding because this stub has no claim state;
// tests requiring deduplication use a stateful cache.
func (c StubGatewayCache) ClaimGrokVideoBilled(_ context.Context, _ string, _ time.Duration) (bool, error) {
	return true, nil
}

// ReleaseGrokVideoBilled accepts billing-claim cleanup and returns a nil
// fixture error.
func (c StubGatewayCache) ReleaseGrokVideoBilled(_ context.Context, _ string) error {
	return nil
}
