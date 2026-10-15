//go:build wireinject

package app

import (
	"github.com/google/wire"

	"github.com/TokenFlux/TokenRouter/internal/infra/timingwheel"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// storage_wire.go registers shared settings, SQL-backed idempotency, and
// timing-wheel construction with Wire. Settings readers and idempotent HTTP
// operations use storage supplied by the application graph. settings.Repository
// resolves to the shared Store, while idempotency storage uses the SQL pool
// extracted from Ent; the set also supplies the timing-wheel constructor.

// storageProviders binds settings repository reads and supplies SQL-backed
// idempotency construction together with the application timing wheel.
var storageProviders = wire.NewSet(
	provideSQLDB,
	provideSettingsStore,
	wire.Bind(new(settings.Repository), new(*settings.Store)),
	provideIdempotencyRepository,
	provideIdempotencyCoordinator,
	provideIdempotencyHTTP,
	provideIdempotencyCleanupService,
	timingwheel.New,
)
