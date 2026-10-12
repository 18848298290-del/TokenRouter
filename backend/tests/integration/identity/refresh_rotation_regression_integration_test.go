//go:build integration

package identity_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/identity/rediscache"
	identitytestkit "github.com/TokenFlux/TokenRouter/internal/identity/testkit"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// refreshReadBarrier lets both requests read one credential before racing Redis consumption.
type refreshReadBarrier struct {
	identity.RefreshTokenCache
	arrived chan struct{}
	release chan struct{}
}

// refreshDeleteFailure injects failures into both deletion and consumption methods.
type refreshDeleteFailure struct {
	identity.RefreshTokenCache
	failure error
}

func (c *refreshReadBarrier) GetRefreshToken(ctx context.Context, key string) (*identity.RefreshTokenData, error) {
	value, err := c.RefreshTokenCache.GetRefreshToken(ctx, key)
	if err != nil {
		return nil, err
	}
	select {
	case c.arrived <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-c.release:
		return value, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c refreshDeleteFailure) DeleteRefreshToken(context.Context, string) error { return c.failure }
func (c refreshDeleteFailure) ConsumeRefreshToken(context.Context, string) (bool, error) {
	return false, c.failure
}

func TestRefreshRotationConsumesOnce(t *testing.T) {
	integrationDB, integrationEntClient := identityDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := integrationEntClient
	users := postgres.NewUserStore(client, integrationDB)
	user := mustCreateUser(t, client, &identity.User{})
	cache := rediscache.NewRefreshTokenCache(rediscontainer.New(t))
	barrier := &refreshReadBarrier{RefreshTokenCache: cache, arrived: make(chan struct{}, 2), release: make(chan struct{})}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	t.Cleanup(unblock)
	cfg := &config.Config{}
	cfg.JWT.Secret = "test-local-fixture-only"
	cfg.JWT.ExpireHour = 1
	cfg.JWT.RefreshTokenExpireDays = 1
	auth := identitytestkit.Auth(client, &identity.AuthDependencies{Users: users, RefreshTokens: barrier, Options: identitytestkit.AuthOptions(cfg)})
	pair, err := auth.GenerateTokenPair(ctx, user, "")
	require.NoError(t, err)
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() { ; _, e := auth.RefreshTokenPair(ctx, pair.RefreshToken); results <- e })
	}
	for range 2 {
		select {
		case <-barrier.arrived:
		case <-ctx.Done():
			t.Fatal("两个请求未同时到达读取屏障", ctx.Err())
		}
	}
	unblock()
	workers.Wait()
	close(results)
	successes := 0
	invalid := 0
	for e := range results {
		if e == nil {
			successes++
		} else if errors.Is(e, identity.ErrRefreshTokenInvalid) {
			invalid++
		} else {
			t.Errorf("轮换出现非预期错误: %v", e)
		}
	}
	require.Equal(t, 1, successes, "同一 refresh token 只能签发一次后继凭据")
	require.Equal(t, 1, invalid, "已被并发请求消费的 token 必须拒绝")
}

func TestRefreshRotationStorageFailureDoesNotIssueTokens(t *testing.T) {
	integrationDB, integrationEntClient := identityDatabase(t)
	ctx := context.Background()
	client := integrationEntClient
	users := postgres.NewUserStore(client, integrationDB)
	user := mustCreateUser(t, client, &identity.User{})
	cache := rediscache.NewRefreshTokenCache(rediscontainer.New(t))
	failing := refreshDeleteFailure{RefreshTokenCache: cache, failure: errors.New("test injected token consume failure")}
	cfg := &config.Config{}
	cfg.JWT.Secret = "test-local-fixture-only"
	cfg.JWT.ExpireHour = 1
	cfg.JWT.RefreshTokenExpireDays = 1
	auth := identitytestkit.Auth(client, &identity.AuthDependencies{Users: users, RefreshTokens: failing, Options: identitytestkit.AuthOptions(cfg)})
	pair, err := auth.GenerateTokenPair(ctx, user, "")
	require.NoError(t, err)
	before, err := cache.GetUserTokenHashes(ctx, user.ID)
	require.NoError(t, err)
	next, err := auth.RefreshTokenPair(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, identity.ErrServiceUnavailable, "无法使旧凭据失效时不得继续签发")
	require.Nil(t, next)
	after, err := cache.GetUserTokenHashes(ctx, user.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, before, after)
}
