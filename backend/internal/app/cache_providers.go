//go:build wireinject

package app

import (
	"github.com/google/wire"

	redisinfra "github.com/TokenFlux/TokenRouter/internal/infra/redis"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/rediscache"
)

// cacheProviders supplies counter caches and the shared periodic-task leader lock.
var cacheProviders = wire.NewSet(
	rediscache.NewInternal500CounterCache,
	redisinfra.NewLeaderLockCache,
	wire.Bind(new(provider.CNMonitorLeader), new(*redisinfra.LeaderLock)),
)
