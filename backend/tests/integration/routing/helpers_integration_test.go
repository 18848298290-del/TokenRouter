//go:build integration

package routing_test

import (
	"context"
	"database/sql"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	schedulerpostgres "github.com/TokenFlux/TokenRouter/internal/scheduler/postgres"
	"github.com/TokenFlux/TokenRouter/internal/testutil/postgrescontainer"
)

// Routing persistence scenarios connect Ent and SQL operations to the same
// migrated PostgreSQL fixture. Group storage receives provider-link, user-access,
// and scheduler-outbox participants through the executor supplied by each
// mutation, allowing its transaction to encompass the participating writes.

// routingDatabase returns an Ent client and the pool from a dedicated migrated
// PostgreSQL container. The underlying fixture registers pool closure and container
// termination; callers register rollback for transactions they open.
func routingDatabase(t *testing.T) (*dbent.Client, *sql.DB) {
	t.Helper()
	db := postgrescontainer.New(t)
	return dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db))), db
}

// newGroupStoreFixture assembles group storage with provider-link changes,
// user-access deletion, and scheduler events. Each participant uses the executor
// supplied by GroupStore for its mutation, allowing transaction-scoped calls to
// share that transaction's database writes.
func newGroupStoreFixture(client *dbent.Client, db postgresinfra.Executor) *routingpostgres.GroupStore {
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
