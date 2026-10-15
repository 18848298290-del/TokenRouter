//go:build wireinject

package app

import (
	"github.com/google/wire"

	opsredis "github.com/TokenFlux/TokenRouter/internal/ops/rediscache"
)

// ops_wire.go registers operational monitoring and release maintenance
// constructors in the application dependency graph. Ops alert evaluation and
// scheduled reports use the application OpsService and notification delivery
// adapters. Release queries separately combine the update cache, release
// client, and build metadata; update maintenance uses that query with the
// binary installer provided by app construction.

// opsProviders supplies operational monitoring, reports, cleanup, release
// queries, and update maintenance constructors to the application graph.
var opsProviders = wire.NewSet(provideOpsOptions, provideOpsRepository, provideOpsService, provideOpsCollector, provideOpsAggregation, provideOpsEvaluator, provideOpsCleanup, provideOpsReports, provideOpsIngress, provideReleaseClient, opsredis.NewUpdateCache, provideReleaseQuery, provideUpdateMaintenance)
