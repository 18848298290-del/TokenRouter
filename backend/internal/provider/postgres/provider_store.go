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

// provider_store.go constructs provider storage and connects record
// conversion, clocks, and scheduler callbacks. Provider storage combines Ent
// record conversion with SQL operations for conditional health and JSONB
// changes. Application assembly supplies proxy/group conversion and the
// scheduler event adapter; constructor defaults provide clocks while storage
// clients and executors remain caller-owned.
//
// Some mutations open an Ent transaction, others participate in a context
// transaction, and dedicated health paths operate directly on the configured
// executor. Those choices determine when changes become durable and when
// snapshot callbacks may run. The store shares its conversion and publishing
// helpers across those paths, while SetEvents is intended for setup before
// concurrent callers begin using the store.

// postgresParameterBatchSize bounds ID-only IN queries so association reads stay
// below PostgreSQL parameter limits while other query arguments remain possible.
const postgresParameterBatchSize = 50000

// ProviderStoreOptions supplies record conversion, event publishing,
// diagnostics, and clock dependencies.
type ProviderStoreOptions struct {
	// Events is optional for wrapper writes and snapshot callbacks. Direct CTE
	// mutation paths call Name and require this adapter to be configured.
	Events ProviderEvents
	// OllamaIdentity classifies records for managed state preservation and group
	// writes; those paths require a callable predicate supplied by
	// application assembly.
	OllamaIdentity func(*providercore.Record) bool
	// Group converts loaded groups for association reads. It must be callable
	// when those reads find rows; hydrated provider APIs copy supported group data.
	Group func(*dbent.Group) *accessview.GroupConfig
	// Proxy converts loaded proxy rows, including fallback origins. It must be
	// callable when proxy reads find rows; hydrated APIs subsequently copy proxy data.
	Proxy func(*dbent.Proxy) *egress.Proxy
	// Observe receives best-effort event and snapshot diagnostics when configured;
	// a nil callback suppresses those messages while mutation results remain available.
	Observe func(string, ...any)
	// Now and LoadLocation supply record time dependencies and mutation clocks.
	// NewProviderStore substitutes standard-library functions when either
	// callback is nil.
	Now          func() time.Time                     // provider observation and write clock
	LoadLocation func(string) (*time.Location, error) // quota time-zone resolution function
}

// ProviderStore persists provider records with Ent conversion and conditional
// SQL writes.
type ProviderStore struct {
	// client and sql are borrowed storage handles whose transaction wiring is
	// supplied by the caller. Keep both usable until store operations
	// and callbacks finish.
	client  *dbent.Client          // provider and association builder client
	sql     postgresinfra.Executor // conditional and health SQL executor
	options ProviderStoreOptions   // stored conversion and event callbacks
}

// NewProviderStore retains the supplied Ent client, SQL executor, and option
// callbacks for provider persistence. Missing clock and location functions
// use time.Now and time.LoadLocation; other callbacks retain their supplied
// values, and the caller owns client shutdown and transaction wiring.
func NewProviderStore(client *dbent.Client, executor postgresinfra.Executor, options ProviderStoreOptions) *ProviderStore {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.LoadLocation == nil {
		options.LoadLocation = time.LoadLocation
	}
	return &ProviderStore{client: client, sql: executor, options: options}
}

// observe forwards a diagnostic message when an observation callback is
// configured.
func (r *ProviderStore) observe(message string, args ...any) {
	if r.options.Observe != nil {
		r.options.Observe(message, args...)
	}
}

// publish encodes the affected group IDs and writes a provider-change event
// through exec. A missing event adapter succeeds as a no-op; the caller
// chooses whether the write is transactional and how to handle publication
// errors.
func (r *ProviderStore) publish(ctx context.Context, exec postgresinfra.Executor, id int64, groups []int64) error {
	return r.enqueue(ctx, exec, ProviderChanged, &id, nil, r.groupPayload(groups))
}

// normalizeJSONMap substitutes an empty map for nil JSON object input. A
// nonnil map is returned by reference, so callers keep responsibility for any
// mutation or sharing until it has been serialized or copied.
func normalizeJSONMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

// derefString returns the pointed-to text or empty text for nil.
func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// recordFromEntity converts an Ent provider and installs the store's time
// dependencies on the result. Nil input returns nil; scalar pointers retain
// RecordFromEntity's sharing until a hydrated caller passes the record
// through CloneRecord.
func (r *ProviderStore) recordFromEntity(entity *dbent.Provider) *providercore.Record {
	value := RecordFromEntity(entity)
	if value != nil {
		value.Now = r.options.Now
		value.LoadLocation = r.options.LoadLocation
	}
	return value
}

// afterChange invokes immediate snapshot synchronization for one provider
// when events are configured. The adapter handles its own read and cache
// errors; each mutation decides whether this call follows a commit or uses
// caller-owned transaction timing.
func (r *ProviderStore) afterChange(ctx context.Context, id int64) {
	if r.options.Events != nil {
		r.options.Events.SyncOne(ctx, id)
	}
}

// SetEvents replaces the adapter used for event writes and snapshot
// callbacks. Application assembly calls it before sharing the store; callers
// synchronize any later replacement because options are read without a lock.
func (r *ProviderStore) SetEvents(events ProviderEvents) { r.options.Events = events }

// clientFromContext returns the Ent transaction client stored in ctx, or
// defaultClient when none is present. The caller keeps ownership of that
// transaction and its commit or rollback; this helper selects a client
// without opening a transaction.
func clientFromContext(ctx context.Context, defaultClient *dbent.Client) *dbent.Client {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return defaultClient
}
