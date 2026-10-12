package postgres

import (
	"context"
	"database/sql/driver"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	servertiming "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
)

// A delayed fake driver supplies controlled connection, statement, row, and
// transaction calls to the timing adapter. Tests keep an
// application-processing gap between row reads, requiring that gap to remain
// outside database duration while operation counts reflect query creation
// rather than each blocking resource call.

// fakeDriverDelay makes each fake driver call observable as blocking time
// while tests separately inject a longer application gap between row reads.
const fakeDriverDelay = 2 * time.Millisecond

// timingFakeDriver creates stateless fixture connections through the legacy
// database/sql open interface.
type timingFakeDriver struct{}

// timingFakeConnector returns its retained connection after a fixed setup
// delay for observation.
type timingFakeConnector struct {
	conn driver.Conn // retained fixture driver connection
}

// timingFakeConn supplies delayed preparation, execution, query, and
// transaction fixtures without a database.
type timingFakeConn struct{}

// timingFakeStmt supports context-aware and legacy calls returning
// predetermined rows and affected counts.
type timingFakeStmt struct{}

// timingFakeRows iterates fixed values with a delay on reads and cleanup.
type timingFakeRows struct {
	// values and index define serial fixture iteration; each query constructs
	// fresh rows so reading one result cannot advance another query's position.
	values [][]driver.Value // ordered fixture row values
	index  int              // next fixture row offset
}

// timingFakeTx delays commit and rollback while reporting success for timing
// assertions.
type timingFakeTx struct{}

// Open returns a fresh timing fixture connection for every supplied DSN.
func (timingFakeDriver) Open(string) (driver.Conn, error) {
	return newTimingFakeConn(), nil
}

// Connect waits for the fixture delay and returns its retained connection. It
// ignores context cancellation while returning that retained connection, letting tests
// isolate the adapter's interval recording.
func (c timingFakeConnector) Connect(context.Context) (driver.Conn, error) {
	time.Sleep(fakeDriverDelay)
	return c.conn, nil
}

// Driver returns the fixture driver used by this connector's database/sql
// surface.
func (timingFakeConnector) Driver() driver.Driver {
	return timingFakeDriver{}
}

// newTimingFakeConn constructs a stateless delayed driver connection for
// timing fixtures.
func newTimingFakeConn() *timingFakeConn {
	return &timingFakeConn{}
}

// Prepare delays and returns a fresh stateless statement, ignoring SQL text.
func (c *timingFakeConn) Prepare(string) (driver.Stmt, error) {
	time.Sleep(fakeDriverDelay)
	return &timingFakeStmt{}, nil
}

// PrepareContext delays and returns a fresh statement for timing
// observations. It ignores context and SQL text, so cancellation and database
// behavior remain outside this fixture's assertions.
func (c *timingFakeConn) PrepareContext(context.Context, string) (driver.Stmt, error) {
	time.Sleep(fakeDriverDelay)
	return &timingFakeStmt{}, nil
}

// Close reports successful connection fixture cleanup without retaining any
// database state.
func (c *timingFakeConn) Close() error {
	return nil
}

// Begin delays and returns a fresh transaction fixture with no database
// state.
func (c *timingFakeConn) Begin() (driver.Tx, error) {
	time.Sleep(fakeDriverDelay)
	return &timingFakeTx{}, nil
}

// BeginTx delays and returns a transaction fixture, ignoring context and
// options. Tests use it to observe timing intervals rather than transaction
// isolation, read-only enforcement, or cancellation.
func (c *timingFakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	time.Sleep(fakeDriverDelay)
	return &timingFakeTx{}, nil
}

// ExecContext delays and returns one affected row for every fixture call.
// Context, SQL, and arguments are ignored; tests observe timing/count
// behavior rather than storage effects.
func (c *timingFakeConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	time.Sleep(fakeDriverDelay)
	return driver.RowsAffected(1), nil
}

// QueryContext delays and returns a fresh single-value rows fixture. It
// ignores context, SQL, and arguments, allowing each timing test to control
// row reads independently of database contents.
func (c *timingFakeConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	time.Sleep(fakeDriverDelay)
	return &timingFakeRows{values: [][]driver.Value{{"value"}}}, nil
}

// Ping waits for the fixed fixture delay and returns success for every
// supplied context. Cancellation does not shorten its wait, so callers use
// this method to observe timing separately from real connection validation.
func (c *timingFakeConn) Ping(context.Context) error {
	time.Sleep(fakeDriverDelay)
	return nil
}

// ResetSession waits for the fixed fixture delay and returns success for
// every supplied context. It ignores cancellation and retains no connection
// state, allowing callers to exercise interval recording without a session-reset effect.
func (c *timingFakeConn) ResetSession(context.Context) error {
	time.Sleep(fakeDriverDelay)
	return nil
}

// Close reports successful prepared-statement fixture cleanup without
// retaining any resource state.
func (s *timingFakeStmt) Close() error {
	return nil
}

// NumInput returns negative one so database/sql accepts variable fixture
// argument counts. The statement ignores supplied arguments in its execution
// methods, leaving these timing tests independent of SQL parameter validation.
func (s *timingFakeStmt) NumInput() int {
	return -1
}

// Exec delays and reports one affected row for legacy fixture calls.
func (s *timingFakeStmt) Exec([]driver.Value) (driver.Result, error) {
	time.Sleep(fakeDriverDelay)
	return driver.RowsAffected(1), nil
}

// Query delays and returns fresh single-value rows for legacy fixture calls.
func (s *timingFakeStmt) Query([]driver.Value) (driver.Rows, error) {
	time.Sleep(fakeDriverDelay)
	return &timingFakeRows{values: [][]driver.Value{{"value"}}}, nil
}

// ExecContext delays and reports one affected row, ignoring context and
// supplied arguments. Tests observe its interval/count path; the fixture
// performs no actual statement or cancellation work.
func (s *timingFakeStmt) ExecContext(context.Context, []driver.NamedValue) (driver.Result, error) {
	time.Sleep(fakeDriverDelay)
	return driver.RowsAffected(1), nil
}

// QueryContext delays and returns fresh single-value rows while ignoring
// context and arguments. Tests retain independent result state and use it to
// observe adapter timing rather than SQL execution.
func (s *timingFakeStmt) QueryContext(context.Context, []driver.NamedValue) (driver.Rows, error) {
	time.Sleep(fakeDriverDelay)
	return &timingFakeRows{values: [][]driver.Value{{"value"}}}, nil
}

// Columns returns the fixed column name used by this single-value fixture.
func (r *timingFakeRows) Columns() []string {
	return []string{"value"}
}

// Close delays and reports successful rows cleanup, keeping fixture data and
// index unchanged. The adapter can observe a cleanup interval without an
// additional SQL operation count.
func (r *timingFakeRows) Close() error {
	time.Sleep(fakeDriverDelay)
	return nil
}

// Next delays before copying the next fixture row into caller-owned dest and
// advancing index. Exhaustion returns EOF; callers serialize reads and supply
// destination space for the row values.
func (r *timingFakeRows) Next(dest []driver.Value) error {
	time.Sleep(fakeDriverDelay)
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

// Commit delays and reports successful transaction fixture completion with no
// database effects.
func (t *timingFakeTx) Commit() error {
	time.Sleep(fakeDriverDelay)
	return nil
}

// Rollback delays and reports successful transaction fixture rollback with no
// database effects.
func (t *timingFakeTx) Rollback() error {
	time.Sleep(fakeDriverDelay)
	return nil
}

// metricDuration extracts one metric's decimal duration from a fixture
// header. Missing or invalid text fails t before returning, so callers
// compare parsed timing values after checking metric presence.
func metricDuration(t *testing.T, header, metric string) float64 {
	t.Helper()
	re := regexp.MustCompile(`(?:^|, )` + regexp.QuoteMeta(metric) + `;dur=([0-9]+(?:\.[0-9]+)?)`)
	match := re.FindStringSubmatch(header)
	if len(match) != 2 {
		t.Fatalf("metric %q missing from header %q", metric, header)
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		t.Fatalf("parse %s duration: %v", metric, err)
	}
	return value
}

// TestServerTimingConnectorRecordsDriverCallsWithoutRowLifetime leaves a
// processing gap between timed fake row reads. It requires two SQL
// operations, absent SQL text in the header, and application duration greater
// than database duration.
func TestServerTimingConnectorRecordsDriverCallsWithoutRowLifetime(t *testing.T) {
	startedAt := time.Now()
	collector := servertiming.New(startedAt)
	ctx := servertiming.WithCollector(context.Background(), collector)

	wrapped := NewTimingConnector(timingFakeConnector{conn: newTimingFakeConn()})
	rawConn, err := wrapped.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conn, ok := rawConn.(*serverTimingConn)
	if !ok {
		t.Fatalf("Connect() returned %T, want *serverTimingConn", rawConn)
	}

	if _, err := conn.ExecContext(ctx, "sensitive update", nil); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.QueryContext(ctx, "sensitive select", nil)
	if err != nil {
		t.Fatal(err)
	}
	values := make([]driver.Value, 1)
	if err := rows.Next(values); err != nil {
		t.Fatal(err)
	}

	// Leave a larger application-processing gap between driver reads so the
	// comparison can detect an adapter that counted the whole rows lifetime as database time.
	time.Sleep(30 * time.Millisecond)
	if err := rows.Next(values); err != io.EOF {
		t.Fatalf("rows.Next() = %v, want EOF", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}

	header := collector.HeaderValue(time.Now(), "bypass")
	if !strings.Contains(header, `queries=2`) {
		t.Fatalf("header %q does not report two SQL operations", header)
	}
	if strings.Contains(header, "sensitive") {
		t.Fatalf("SQL text leaked into header: %q", header)
	}
	if app, db := metricDuration(t, header, "app"), metricDuration(t, header, "db"); app <= db {
		t.Fatalf("row processing gap was counted as DB time: app=%.1fms db=%.1fms header=%q", app, db, header)
	}
}

// TestServerTimingPreparedStatementsAndTransactions exercises prepare,
// execution, query cleanup, transaction commit, Ping, and session reset. It
// requires three counted SQL operations and positive database elapsed
// duration, separating extra blocking intervals from query counts.
func TestServerTimingPreparedStatementsAndTransactions(t *testing.T) {
	collector := servertiming.New(time.Now())
	ctx := servertiming.WithCollector(context.Background(), collector)
	conn := &serverTimingConn{Conn: newTimingFakeConn()}

	stmt, err := conn.PrepareContext(ctx, "prepare sensitive statement")
	if err != nil {
		t.Fatal(err)
	}
	timedStmt, ok := stmt.(*serverTimingStmt)
	if !ok {
		t.Fatalf("PrepareContext() returned %T, want *serverTimingStmt", stmt)
	}
	if _, err := timedStmt.ExecContext(ctx, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := timedStmt.QueryContext(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}

	tx, err := conn.BeginTx(ctx, driver.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := conn.ResetSession(ctx); err != nil {
		t.Fatal(err)
	}

	header := collector.HeaderValue(time.Now(), "bypass")
	if !strings.Contains(header, `queries=3`) {
		t.Fatalf("header %q does not report prepare, exec, and query operations", header)
	}
	if metricDuration(t, header, "db") <= 0 {
		t.Fatalf("DB duration was not recorded: %q", header)
	}
}

// TestNamedValuesRejectNamedParameters supplies a named argument and then a
// positional argument. It requires rejection of the named binding and
// retained positional value, protecting compatibility conversion before
// legacy driver calls.
func TestNamedValuesRejectNamedParameters(t *testing.T) {
	if _, err := namedValues([]driver.NamedValue{{Name: "secret", Value: 1}}); err == nil {
		t.Fatal("namedValues accepted a named parameter")
	}
	values, err := namedValues([]driver.NamedValue{{Ordinal: 1, Value: "value"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0] != "value" {
		t.Fatalf("namedValues() = %#v", values)
	}
}
