//go:build wireinject

package app

import (
	"github.com/google/wire"

	redisinfra "github.com/TokenFlux/TokenRouter/internal/infra/redis"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/rediscache"
)

// Provider internal-error counters and leader election use Redis resources
// from the application graph. The CN usage monitor receives the shared leader
// lock through its own interface, while provider consumers receive the counter
// cache constructor from this set.

// cacheProviders supplies provider error counters and binds the shared Redis
// leader lock for CN usage monitoring.
var cacheProviders = wire.NewSet(
	rediscache.NewInternal500CounterCache,
	redisinfra.NewLeaderLockCache,
	wire.Bind(new(provider.CNMonitorLeader), new(*redisinfra.LeaderLock)),
)
