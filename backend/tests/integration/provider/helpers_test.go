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

const (
	// deprecatedUpstreamBillingProbeExtraKey identifies the retired upstream billing snapshot.
	deprecatedUpstreamBillingProbeExtraKey = "upstream_billing_probe"

	// deprecatedUpstreamBillingProbeEnabledExtraKey identifies the retired billing probe switch.
	deprecatedUpstreamBillingProbeEnabledExtraKey = "upstream_billing_probe_enabled"
)

// captureEntQueryMatcher records SQL emitted by Ent.
type captureEntQueryMatcher struct {
	actual *string
}

// captureQuerySQL records SQL and arguments before executing queries.
type captureQuerySQL struct {
	db       *sql.DB
	captured *string
	args     *[]any
}

// rowsAffectedResult returns a fixed affected-row count.
type rowsAffectedResult int64

// recordingSQLExecutor records writes and supplies configured errors and post-write callbacks.
type recordingSQLExecutor struct {
	result      sql.Result
	err         error
	afterExec   func()
	execQueries []string
	execArgs    [][]any
}

// providerEventsFixture writes scheduler outbox events and publishes provider snapshots.
type providerEventsFixture struct{ publisher scheduler.SnapshotPublisher }

// Match captures query text for assertions.
func (m captureEntQueryMatcher) Match(_, actual string) error {
	if m.actual == nil {
		return fmt.Errorf("query capture target is nil")
	}
	*m.actual = actual
	return nil
}

// newProxyStoreContract binds proxy changes, provider cleanup, and scheduler events to one transaction.
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

// ExecContext delegates writes to the database.
func (c captureQuerySQL) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.db.ExecContext(ctx, query, args...)
}

// QueryContext records query text and arguments before execution.
func (c captureQuerySQL) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if c.captured != nil {
		*c.captured = query
	}
	if c.args != nil {
		*c.args = append([]any(nil), args...)
	}
	return c.db.QueryContext(ctx, query, args...)
}

// normalizeSQLWhitespace collapses consecutive whitespace to one space.
func normalizeSQLWhitespace(sql string) string {
	return strings.Join(regexp.MustCompile(`\s+`).Split(strings.TrimSpace(sql), -1), " ")
}

// LastInsertId returns a placeholder ID for the fixture.
func (r rowsAffectedResult) LastInsertId() (int64, error) { return 0, nil }

// RowsAffected returns the configured count.
func (r rowsAffectedResult) RowsAffected() (int64, error) { return int64(r), nil }

// ExecContext records a write and returns its configured result.
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

// QueryContext returns sql.ErrNoRows.
func (e *recordingSQLExecutor) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return nil, sql.ErrNoRows
}

// newProviderStoreContract binds provider storage to outbox writes and the supplied scheduler cache.
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

// Name maps provider events to scheduler outbox event names.
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

// Write appends a scheduler event using the supplied transaction.
func (f providerEventsFixture) Write(ctx context.Context, exec postgresinfra.Executor, event providerpostgres.ProviderEvent, id, group *int64, payload any) error {
	return schedulerpostgres.EnqueueSchedulerChange(ctx, exec, f.Name(event), id, group, payload)
}

// GroupPayload builds group event data.
func (providerEventsFixture) GroupPayload(ids []int64) any { return scheduler.GroupPayload(ids) }

// SyncOne publishes a provider snapshot.
func (f providerEventsFixture) SyncOne(ctx context.Context, id int64) { f.publisher.Publish(ctx, id) }

// SyncMany publishes provider snapshots in a batch.
func (f providerEventsFixture) SyncMany(ctx context.Context, ids []int64) {
	f.publisher.PublishMany(ctx, ids)
}

// Drop removes a provider snapshot.
func (f providerEventsFixture) Drop(ctx context.Context, id int64) { f.publisher.Drop(ctx, id) }

// providerPublicationEvents connects provider storage to scheduler snapshot publication.
func providerPublicationEvents(store *providerpostgres.ProviderStore, cache scheduler.SnapshotPublicationCache) providerEventsFixture {
	return providerEventsFixture{publisher: scheduler.SnapshotPublisher{
		Cache: cache,
		Read: func(ctx context.Context, id int64) (scheduler.SnapshotProvider, error) {
			v, err := store.GetByID(ctx, id)
			return codec.WrapRecord(v), err
		},
		ReadMany: func(ctx context.Context, ids []int64) ([]scheduler.SnapshotProvider, error) {
			values, err := store.GetByIDs(ctx, ids)
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
