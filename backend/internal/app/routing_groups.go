package app

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/google/uuid"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	apikeypostgres "github.com/TokenFlux/TokenRouter/internal/apikey/postgres"
	"github.com/TokenFlux/TokenRouter/internal/config"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/selection"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	catalogprovider "github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
	routingadapter "github.com/TokenFlux/TokenRouter/internal/routing/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
	schedulerpostgres "github.com/TokenFlux/TokenRouter/internal/scheduler/postgres"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

// routing_groups.go assembles group persistence, administration, and
// availability probes with shared provider and identity services. Group
// mutations connect provider membership, user access deletion, and scheduler
// events through the selected database executor. Administration also receives
// provider and Key reads, requestable-model resolution, and shared score
// validation, while availability probing uses the configured calendar location
// and common platform tests.

// routingGroupProviders narrows provider storage reads for routing group
// administration.
type routingGroupProviders struct {
	Store *providerpostgres.ProviderStore // provider group candidates
}

// GetByIDs returns provider identity, platform, and type for the requested IDs.
// It allocates an empty slice when the source is nil and retains the repository
// error with converted rows.
func (r routingGroupProviders) GetByIDs(ctx context.Context, ids []int64) ([]routing.GroupProvider, error) {
	values, err := r.Store.GetByIDs(ctx, ids)
	// Return an empty slice for a batch miss; schedulable list conversion retains nil results.
	out := make([]routing.GroupProvider, len(values))
	for i, v := range values {
		out[i] = r.project(v)
	}
	return out, err
}

// ListSchedulableByGroupID converts stored group candidates into routing
// catalogue providers.
func (r routingGroupProviders) ListSchedulableByGroupID(ctx context.Context, id int64) ([]routing.CatalogueProvider, error) {
	values, err := r.Store.ListSchedulableByGroupID(ctx, id)
	return gatewayprovider.CatalogueProviders(values), err
}

// project selects provider identity, platform, and type from a nonnil record.
func (r routingGroupProviders) project(v *provider.Record) routing.GroupProvider {
	return routing.GroupProvider{ID: v.ID, Platform: v.Platform, Type: v.Type}
}

// provideRoutingGroupStore binds group persistence and cross-module transaction
// participants. Provider links, user access removal, and scheduler event
// callbacks all receive the executor selected by GroupStore for the operation.
func provideRoutingGroupStore(client *dbent.Client, db *sql.DB) *routingpostgres.GroupStore {
	return routingpostgres.NewGroupStore(client, db, routingpostgres.GroupStoreOptions{
		Providers: func(exec postgresinfra.Executor) routingpostgres.GroupLinkParticipant {
			return providerpostgres.GroupLinksInTx(exec)
		},
		Users: func(exec postgresinfra.Executor) routingpostgres.GroupAccessParticipant {
			return identitypostgres.GroupAccessDeletionInTx(exec)
		},
		Enqueue: func(ctx context.Context, exec postgresinfra.Executor, id *int64) error {
			return schedulerpostgres.EnqueueSchedulerChange(ctx, exec, scheduler.SchedulerOutboxEventGroupChanged, nil, id, nil)
		},
	})
}

// provideGroupReader exposes the shared GroupStore through the routing
// repository interface.
func provideGroupReader(store *routingpostgres.GroupStore) routing.GroupRepository {
	return store
}

// provideRoutingGroupAdmin combines shared group mutations, provider/Key reads,
// and model rules. Catalog model IDs are read when defaults are requested, and
// score weights use current settings with supplied startup defaults for
// validation.
func provideRoutingGroupAdmin(catalog *catalogprovider.Service, store *routingpostgres.GroupStore, providers *providerpostgres.ProviderStore, keys *apikeypostgres.KeyStore, invalidator apikey.APIKeyAuthCacheInvalidator, modelConfigs *routing.PricingConfigService, settings *settingscore.Store, defaults *scheduler.AdminDefaults, attributes *routing.ModelAttributeService) *routing.GroupAdmin {
	return routing.NewGroupAdmin(store, store, store, routingGroupProviders{Store: providers}, keys, invalidator, modelConfigs, routing.GroupAdminOptions{
		DefaultModels: func(string) []string { return catalog.ModelIDs() },
		ModelResolver: routing.RequestableResolver{
			Attributes:    attributes,
			GroupPolicies: modelConfigs,
			Warn:          slog.Warn,
		},
		GlobalWeights: func(ctx context.Context) (policy.ScoreWeights, error) {
			return scheduler.LoadValidationWeights(ctx, settings, *defaults)
		}, Mutate: store.Mutate,
	})
}

// provideGroupProbeRunner constructs group probes using common provider tests
// and selection. Valid configured timezone replaces time.Local; absent or
// invalid configuration retains the process location, and the runner gets a
// generated instance identity for coordination.
func provideGroupProbeRunner(repo routing.GroupAvailabilityProbeRepository, tests *provider.TestService, gateway *selection.Generic, openai *selection.Compatible, gemini *selection.Gemini, cfg *config.Config, attributes *routing.ModelAttributeService) *routing.GroupAvailabilityProbeRunnerService {
	location := time.Local
	if cfg != nil {
		// A failed configured-zone lookup retains the initialized process location.
		// The schedule adapter therefore receives a defined timezone for calculating future probe runs.
		if parsed, err := time.LoadLocation(cfg.Timezone); err == nil && parsed != nil {
			location = parsed
		}
	}
	executor := selection.NewProbe(tests, gateway, openai, gemini)
	executor.Attributes = attributes
	return routing.NewGroupAvailabilityProbeRunnerService(repo, executor, routing.GroupProbeOptions{InstanceID: uuid.NewString(), Now: time.Now, Schedule: routingadapter.NewGroupProbeSchedule(location), Observe: func(format string, args ...any) {
		logging.LegacyPrintf("service.group_availability_probe", format, args...)
	}})
}
