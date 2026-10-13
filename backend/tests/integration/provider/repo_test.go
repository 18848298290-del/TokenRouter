package provider_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"sync"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Provider entity conversion may request group memberships for more records
// than a single parameter list can hold. A database/sql driver rejects calls
// above its configured 65535-argument limit and returns empty membership rows,
// so the test can exercise conversion of 65536 providers without PostgreSQL.

// parameterLimitTestDriverName names the synthetic database/sql driver selected
// by the provider conversion fixture; its registration is shared by test calls.
const parameterLimitTestDriverName = "tokenrouter_param_limit_test"

// registerParameterLimitTestDriverOnce prevents repeated database/sql registration
// when fixture repositories are constructed; the registered driver is process-wide.
var registerParameterLimitTestDriverOnce sync.Once

// parameterLimitDriver implements the synthetic database/sql entry point for
// argument-limit tests.
type parameterLimitDriver struct{}

// parameterLimitConn implements query and transaction methods for the synthetic
// SQL fixture.
type parameterLimitConn struct{}

// parameterLimitStmt retains prepared SQL text while checking argument counts
// during execution.
type parameterLimitStmt struct {
	query string // prepared membership query text
}

// parameterLimitTx supplies successful completion calls for transactions in the
// synthetic SQL fixture.
type parameterLimitTx struct{}

// parameterLimitRows exposes fixture column names and returns an empty
// iterator.
type parameterLimitRows struct {
	columns []string // empty-iterator column names
}

// TestProvidersToService_LargeActiveProviderSetDoesNotExceedPostgresParameterLimit
// converts 65536 provider entities through a synthetic driver enforcing a
// 65535-argument limit. Conversion must succeed and return one record per input
// while membership queries yield empty rows.
func TestProvidersToService_LargeActiveProviderSetDoesNotExceedPostgresParameterLimit(t *testing.T) {
	repo := newParameterLimitProviderRepo(t)

	// Use one more provider than the synthetic driver's argument limit so a
	// single membership query covering all input IDs would fail this conversion.
	providers := make([]*dbent.Provider, 0, 65536)
	for i := range 65536 {
		providers = append(providers, &dbent.Provider{
			ID:          int64(i + 1),
			Name:        "large-active",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Credentials: map[string]any{},
			Extra:       map[string]any{},
			Status:      billing.StatusActive,
			Schedulable: true,
		})
	}

	got, err := repo.RecordsFromEntities(context.Background(), providers)
	require.NoError(t, err)
	require.Len(t, got, len(providers))
}

// newParameterLimitProviderRepo registers the synthetic SQL driver once and
// constructs an Ent-backed provider store. It registers client and database
// cleanup; returned rows are empty, and driver calls enforce only the fixture's
// parameter-count rule.
func newParameterLimitProviderRepo(t *testing.T) *providerpostgres.ProviderStore {
	t.Helper()

	registerParameterLimitTestDriverOnce.Do(func() {
		sql.Register(parameterLimitTestDriverName, parameterLimitDriver{})
	})

	db, err := sql.Open(parameterLimitTestDriverName, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	drv := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(drv))
	t.Cleanup(func() { _ = client.Close() })

	return newProviderStoreContract(client, nil, nil)
}

// Open returns a fresh synthetic connection for parameter-limit fixture
// queries.
func (parameterLimitDriver) Open(string) (driver.Conn, error) {
	return parameterLimitConn{}, nil
}

// Prepare retains SQL text in a synthetic statement for later queries.
func (parameterLimitConn) Prepare(query string) (driver.Stmt, error) {
	return parameterLimitStmt{query: query}, nil
}

// Close succeeds because the synthetic connection holds no external resources.
func (parameterLimitConn) Close() error {
	return nil
}

// Begin returns a synthetic transaction whose completion methods always
// succeed.
func (parameterLimitConn) Begin() (driver.Tx, error) {
	return parameterLimitTx{}, nil
}

// QueryContext checks the fixture argument limit and returns empty query rows.
// It ignores context cancellation, so callers use it to inspect query size
// rather than database timing or cancellation.
func (parameterLimitConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return queryWithParameterLimit(query, args)
}

// Close succeeds because the synthetic statement owns no external resources.
func (s parameterLimitStmt) Close() error {
	return nil
}

// NumInput reports unknown argument count so database/sql passes calls to the
// fixture. Returning minus one leaves parameter-limit validation to Exec or
// Query when their arguments are available.
func (s parameterLimitStmt) NumInput() int {
	return -1
}

// Exec checks positional argument count and returns a synthetic zero-row write
// result. Oversized calls carry the fixture's parameter error; accepted calls
// report zero because this driver stores no rows.
func (s parameterLimitStmt) Exec(args []driver.Value) (driver.Result, error) {
	return driver.RowsAffected(0), parameterLimitError(len(args))
}

// Query applies the fixture's argument limit to a prepared membership query and
// returns an empty row iterator. It reports the same parameter-count error as
// the direct connection query, giving both driver entry points one validation
// rule.
func (s parameterLimitStmt) Query(args []driver.Value) (driver.Rows, error) {
	// Prepared-statement queries supply positional values, while the shared
	// query fixture accepts NamedValue inputs for both driver entry points.
	namedArgs := make([]driver.NamedValue, len(args))
	for i, arg := range args {
		namedArgs[i] = driver.NamedValue{Ordinal: i + 1, Value: arg}
	}
	return queryWithParameterLimit(s.query, namedArgs)
}

// Commit succeeds without persisting state in the synthetic SQL fixture.
func (parameterLimitTx) Commit() error {
	return nil
}

// Rollback succeeds without modifying state in the synthetic SQL fixture.
func (parameterLimitTx) Rollback() error {
	return nil
}

// queryWithParameterLimit rejects argument lists exceeding the fixture's
// configured threshold. Accepted queries return an empty iterator with columns
// selected from query text, allowing Ent membership scans to complete.
func queryWithParameterLimit(query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := parameterLimitError(len(args)); err != nil {
		return nil, err
	}
	return parameterLimitRows{columns: columnsForParameterLimitQuery(query)}, nil
}

// parameterLimitError returns a pq-style error above 65535 arguments and nil
// otherwise. The driver uses this fixed threshold to reproduce the oversized
// parameter-list failure for entity conversion tests.
func parameterLimitError(paramCount int) error {
	// Reject counts above the synthetic driver's cap so an unbatched membership
	// query fails instead of receiving the fixture's ordinary empty result.
	if paramCount <= 65535 {
		return nil
	}
	return fmt.Errorf("pq: got %d parameters but PostgreSQL only supports 65535 parameters", paramCount)
}

// columnsForParameterLimitQuery returns membership column names for nonempty
// SQL text. Empty queries return nil; the synthetic driver does not parse SQL
// or produce row values for those columns.
func columnsForParameterLimitQuery(query string) []string {
	if query == "" {
		return nil
	}
	return []string{"provider_id", "group_id", "priority", "created_at"}
}

// Columns returns the fixture's shared column-name slice for database/sql
// scans. Changing that slice changes later column reads from the same empty
// iterator, so fixture consumers treat the names as immutable.
func (r parameterLimitRows) Columns() []string {
	return r.columns
}

// Close succeeds because the synthetic iterator owns no result resources.
func (parameterLimitRows) Close() error {
	return nil
}

// Next returns io.EOF immediately so membership queries yield zero rows.
func (parameterLimitRows) Next([]driver.Value) error {
	return io.EOF
}
