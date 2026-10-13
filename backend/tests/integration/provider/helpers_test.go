package provider_test

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	egresspostgres "github.com/TokenFlux/TokenRouter/internal/egress/postgres"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	schedulerpostgres "github.com/TokenFlux/TokenRouter/internal/scheduler/postgres"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache/codec"
)

// Provider and proxy tests connect persistence to the scheduler outbox and
// snapshot publisher through small adapters. SQL recorders expose emitted
// statements and arguments, while the store factories choose the existing
// provider, proxy, group, and Ollama adapters used by repository tests.

// Retired billing-probe keys identify fixture input that provider writes must
// discard while retaining supported Extra fields used by current consumers.
const (
	// deprecatedUpstreamBillingProbeExtraKey identifies the retired upstream billing snapshot.
	deprecatedUpstreamBillingProbeExtraKey = "upstream_billing_probe"

	// deprecatedUpstreamBillingProbeEnabledExtraKey identifies the retired billing probe switch.
	deprecatedUpstreamBillingProbeEnabledExtraKey = "upstream_billing_probe_enabled"
)

// captureEntQueryMatcher records Ent query text for later SQL-shape assertions.
type captureEntQueryMatcher struct {
	// actual receives every matched query, so callers retain this pointer until
	// the SQL mock operation finishes. A nil target makes Match fail.
	actual *string
}

// captureQuerySQL executes database queries while exposing optional SQL and
// argument captures.
type captureQuerySQL struct {
	db *sql.DB // fixture query database
	// captured receives query text when configured; nil disables text capture
	// while execution still delegates to the same fixture database.
	captured *string
	// args receives a copied argument slice when configured, retaining its
	// element references so tests can inspect the submitted values after execution.
	args *[]any
}

// rowsAffectedResult supplies a fixed affected-row count for synthetic write
// results.
type rowsAffectedResult int64

// recordingSQLExecutor records write arguments and returns configured results
// for mutation assertions.
type recordingSQLExecutor struct {
	// result, err, and afterExec govern the recorded write outcome. err stops
	// execution before afterExec; successful writes invoke the callback before
	// returning result to the repository under test.
	result    sql.Result // configured write result
	err       error      // configured write error
	afterExec func()     // successful write callback
	// execQueries and execArgs align in call order, including writes returning a
	// configured error. Only the outer argument slices are copied; mutable argument
	// values remain shared with their callers.
	execQueries []string // recorded write statements
	execArgs    [][]any  // copied write argument slices
}

// providerEventsFixture maps provider storage events to scheduler persistence
// and snapshot publication.
type providerEventsFixture struct {
	publisher scheduler.SnapshotPublisher // scheduler record publication adapter
}

// Match stores the actual SQL text for later assertions regardless of the
// expected pattern. A missing capture target returns an error; callers provide
// storage that remains valid through the mock query.
func (m captureEntQueryMatcher) Match(_, actual string) error {
	if m.actual == nil {
		return fmt.Errorf("query capture target is nil")
	}
	*m.actual = actual
	return nil
}

// newProxyStoreContract constructs proxy storage with provider cleanup and
// scheduler-event participants. The store's transaction path supplies executors
// to those participants; callers own cleanup of the Ent client and any outer
// transaction used by the fixture.
func newProxyStoreContract(client *dbent.Client, exec postgresinfra.Executor) *egresspostgres.ProxyStore {
	return egresspostgres.NewProxyStore(client, exec, egresspostgres.ProxyStoreOptions{
		Providers: func(tx postgresinfra.Executor) egresspostgres.ProxyProviderParticipant {
			return providerpostgres.ProxyChangesInTx(tx)
		},
		Enqueue: func(ctx context.Context, tx postgresinfra.Executor, payload any) error {
			return schedulerpostgres.EnqueueSchedulerChange(ctx, tx, scheduler.SchedulerOutboxEventProviderBulkChanged, nil, nil, payload)
		},
	})
}

// ExecContext executes the supplied write directly on the fixture database.
func (c captureQuerySQL) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.db.ExecContext(ctx, query, args...)
}

// QueryContext records SQL and a copy of its argument slice before delegating
// to the database. Either capture pointer may be nil; callers close the
// returned rows after their assertions or scans.
func (c captureQuerySQL) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if c.captured != nil {
		*c.captured = query
	}
	if c.args != nil {
		*c.args = append([]any(nil), args...)
	}
	return c.db.QueryContext(ctx, query, args...)
}

// normalizeSQLWhitespace trims SQL and collapses whitespace for stable clause
// assertions.
func normalizeSQLWhitespace(sql string) string {
	return strings.Join(regexp.MustCompile(`\s+`).Split(strings.TrimSpace(sql), -1), " ")
}

// LastInsertId returns zero as the fixture's unused insert identifier.
func (r rowsAffectedResult) LastInsertId() (int64, error) { return 0, nil }

// RowsAffected returns the fixed affected-row count represented by the fixture.
func (r rowsAffectedResult) RowsAffected() (int64, error) { return int64(r), nil }

// ExecContext records a statement and copied argument slice before returning
// the configured result. A configured error ends the call before afterExec;
// successful calls invoke that callback synchronously before exposing the
// result.
func (e *recordingSQLExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	e.execQueries = append(e.execQueries, query)
	e.execArgs = append(e.execArgs, append([]any(nil), args...))
	if e.err != nil {
		return nil, e.err
	}
	if e.afterExec != nil {
		e.afterExec()
	}
	return e.result, nil
}

// QueryContext returns nil rows and sql.ErrNoRows for every query through this
// write recorder. Use it for mutation paths; callers that attempt a read
// receive the synthetic error and no fixture records.
func (e *recordingSQLExecutor) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return nil, sql.ErrNoRows
}

// newProviderStoreContract constructs provider storage with group, proxy,
// Ollama, and scheduling adapters. It binds snapshot publication after
// construction; a nil cache skips immediate publication, while the store's
// transaction path determines completion of persistence and event writes.
func newProviderStoreContract(client *dbent.Client, exec postgresinfra.Executor, cache scheduler.SnapshotPublicationCache) *providerpostgres.ProviderStore {
	store := providerpostgres.NewProviderStore(client, exec, providerpostgres.ProviderStoreOptions{
		Group: func(g *dbent.Group) *accessview.GroupConfig {
			return (*accessview.GroupConfig)(routingpostgres.GroupFromEnt(g))
		},
		OllamaIdentity: provider.IsOllamaCloudUsageProvider,
		Proxy:          egresspostgres.ProxyEntity,
		Events:         providerEventsFixture{},
	})
	store.SetEvents(providerPublicationEvents(store, cache))
	return store
}

// Name returns the scheduler event name for a recognized provider event. An
// unknown enum value panics, so fixture callers pass the provider store's
// declared change kinds.
func (providerEventsFixture) Name(event providerpostgres.ProviderEvent) string {
	switch event {
	case providerpostgres.ProviderChanged:
		return scheduler.SchedulerOutboxEventProviderChanged
	case providerpostgres.ProviderGroupsChanged:
		return scheduler.SchedulerOutboxEventProviderGroupsChanged
	case providerpostgres.ProviderLastUsed:
		return scheduler.SchedulerOutboxEventProviderLastUsed
	case providerpostgres.ProviderBulkChanged:
		return scheduler.SchedulerOutboxEventProviderBulkChanged
	default:
		panic("未知提供商事件")
	}
}

// Write enqueues the mapped scheduler event through the supplied SQL executor.
// When that executor is a transaction, its caller determines commit or rollback
// and when publication may follow.
func (f providerEventsFixture) Write(ctx context.Context, exec postgresinfra.Executor, event providerpostgres.ProviderEvent, id, group *int64, payload any) error {
	return schedulerpostgres.EnqueueSchedulerChange(ctx, exec, f.Name(event), id, group, payload)
}

// GroupPayload delegates group-event encoding to scheduler.GroupPayload. Empty
// IDs yield an untyped nil payload; otherwise the returned map retains the input
// slice, which callers keep stable until event persistence completes.
func (providerEventsFixture) GroupPayload(ids []int64) any { return scheduler.GroupPayload(ids) }

// SyncOne asks the configured publisher to read and cache one provider. An
// absent cache or nonpositive ID skips publication; read and cache errors
// follow the publisher's diagnostics path.
func (f providerEventsFixture) SyncOne(ctx context.Context, id int64) { f.publisher.Publish(ctx, id) }

// SyncMany asks the configured publisher to read and cache distinct positive
// provider IDs. An absent cache or empty eligible set skips the operation; the
// publisher logs failures while processing the returned records.
func (f providerEventsFixture) SyncMany(ctx context.Context, ids []int64) {
	f.publisher.PublishMany(ctx, ids)
}

// Drop asks the configured publisher to remove a provider's cached record. An
// absent cache or nonpositive ID skips removal; cache errors follow the
// publisher's diagnostics path.
func (f providerEventsFixture) Drop(ctx context.Context, id int64) { f.publisher.Drop(ctx, id) }

// providerPublicationEvents binds snapshot publication to current records read
// from store. The supplied cache receives codec-wrapped records; a nil batch
// result passes through with its error, and store reads determine record
// freshness.
func providerPublicationEvents(store *providerpostgres.ProviderStore, cache scheduler.SnapshotPublicationCache) providerEventsFixture {
	return providerEventsFixture{publisher: scheduler.SnapshotPublisher{
		Cache: cache,
		Read: func(ctx context.Context, id int64) (scheduler.SnapshotProvider, error) {
			v, err := store.GetByID(ctx, id)
			return codec.WrapRecord(v), err
		},
		ReadMany: func(ctx context.Context, ids []int64) ([]scheduler.SnapshotProvider, error) {
			values, err := store.GetByIDs(ctx, ids)
			// Pass nil through with the accompanying read error; the publisher
			// uses that error to choose failure diagnostics or an empty successful batch.
			if values == nil {
				return nil, err
			}
			out := make([]scheduler.SnapshotProvider, len(values))
			for i, value := range values {
				out[i] = codec.WrapRecord(value)
			}
			return out, err
		},
	}}
}
