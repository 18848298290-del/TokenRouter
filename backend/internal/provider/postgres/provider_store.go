package postgres

import (
	"context"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
)

const postgresParameterBatchSize = 50000

// ProviderStoreOptions supplies record conversion, event publishing, diagnostics, and clock dependencies.
type ProviderStoreOptions struct {
	Events         ProviderEvents
	OllamaIdentity func(*providercore.Record) bool
	Group          func(*dbent.Group) *accessview.GroupConfig
	Proxy          func(*dbent.Proxy) *egress.Proxy
	Observe        func(string, ...any)
	Now            func() time.Time
	LoadLocation   func(string) (*time.Location, error)
}
type ProviderStore struct {
	client  *dbent.Client
	sql     postgresinfra.Executor
	options ProviderStoreOptions
}

func NewProviderStore(client *dbent.Client, executor postgresinfra.Executor, options ProviderStoreOptions) *ProviderStore {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.LoadLocation == nil {
		options.LoadLocation = time.LoadLocation
	}
	return &ProviderStore{client: client, sql: executor, options: options}
}

func (r *ProviderStore) observe(message string, args ...any) {
	if r.options.Observe != nil {
		r.options.Observe(message, args...)
	}
}

func (r *ProviderStore) publish(ctx context.Context, exec postgresinfra.Executor, id int64, groups []int64) error {
	return r.enqueue(ctx, exec, ProviderChanged, &id, nil, r.groupPayload(groups))
}

func normalizeJSONMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (r *ProviderStore) recordFromEntity(entity *dbent.Provider) *providercore.Record {
	value := RecordFromEntity(entity)
	if value != nil {
		value.Now = r.options.Now
		value.LoadLocation = r.options.LoadLocation
	}
	return value
}

func (r *ProviderStore) afterChange(ctx context.Context, id int64) {
	if r.options.Events != nil {
		r.options.Events.SyncOne(ctx, id)
	}
}

// SetEvents replaces the provider event callbacks.
func (r *ProviderStore) SetEvents(events ProviderEvents) { r.options.Events = events }

// clientFromContext returns the client from ent.NewTxContext, or defaultClient when no transaction is present.
func clientFromContext(ctx context.Context, defaultClient *dbent.Client) *dbent.Client {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return defaultClient
}
