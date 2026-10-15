package postgres

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"time"

	servertiming "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
)

// timing.go wraps SQL driver resources to record request-scoped database
// durations and operation counts.
// Database timing observes blocking driver calls separately from how long a
// caller keeps rows or a transaction open. Prepare, execution, and query
// creation count logical SQL operations, while connection setup, row
// reads/close, transaction boundaries, Ping, and session reset add duration
// intervals without increasing that count. Application work between row reads
// therefore remains application elapsed time.
//
// Wrappers retain driver resources and contexts for later row or transaction
// calls, and the request collector merges their intervals when building a
// header. Optional driver interfaces delegate when available and supply
// database/sql-compatible defaults or ErrSkip otherwise. Legacy statement
// execution converts named values to positional values after rejecting named
// parameters; those legacy paths cannot forward a context to the underlying
// method.

// serverTimingConnector measures setup before returning wrapped driver
// connections for request timing.
type serverTimingConnector struct {
	base driver.Connector // retained driver connector dependency
}

// serverTimingConn adapts optional driver call interfaces to completed
// request timing observations.
type serverTimingConn struct {
	driver.Conn // retained underlying driver connection
}

// serverTimingStmt wraps context-aware and legacy prepared-statement
// execution for request timing.
type serverTimingStmt struct {
	driver.Stmt // retained underlying prepared statement
}

// serverTimingRows retains query context for timing later blocking row reads
// and cleanup.
type serverTimingRows struct {
	driver.Rows // retained underlying query rows
	// ctx retains the operation's collector for later resource reads or
	// completion. Those blocking intervals add duration without increasing the
	// already established SQL operation count.
	ctx context.Context // query-time request timing context
}

// serverTimingTx retains begin context for timing commit and rollback without
// adding queries.
type serverTimingTx struct {
	driver.Tx // retained underlying database transaction
	// ctx retains the operation's collector for later resource reads or
	// completion. Those blocking intervals add duration without increasing the
	// already established SQL operation count.
	ctx context.Context // begin-time request timing context
}

// NewTimingConnector wraps base for request-context driver timing and returns
// a connector. Supply a non-nil base; successful connections retain driver
// ownership and are closed through database/sql or their normal caller
// lifecycle.
func NewTimingConnector(base driver.Connector) driver.Connector {
	return &serverTimingConnector{base: base}
}

// Connect opens the underlying driver connection and records its blocking
// interval without a query count, including failure. Successful results are
// wrapped for later operations while callers retain ordinary driver
// connection cleanup.
func (c *serverTimingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	startedAt := time.Now()
	conn, err := c.base.Connect(ctx)
	servertiming.RecordInterval(ctx, servertiming.MetricDatabase, startedAt, time.Now())
	if err != nil {
		return nil, err
	}
	return &serverTimingConn{Conn: conn}, nil
}

// Driver returns the underlying connector's database driver for database/sql
// use.
func (c *serverTimingConnector) Driver() driver.Driver {
	return c.base.Driver()
}

// Prepare delegates legacy statement preparation and wraps a successful
// statement for later context-aware execution. This context-free method adds
// no timing observation itself; callers close the returned statement through
// its driver lifecycle.
func (c *serverTimingConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &serverTimingStmt{Stmt: stmt}, nil
}

// PrepareContext prepares through the optional context interface, or legacy
// Prepare when absent. It records one SQL operation and its interval even on
// failure; a successful statement is wrapped and still requires caller
// cleanup.
func (c *serverTimingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	startedAt := time.Now()
	var (
		stmt driver.Stmt
		err  error
	)
	if preparer, ok := c.Conn.(driver.ConnPrepareContext); ok {
		stmt, err = preparer.PrepareContext(ctx, query)
	} else {
		// A legacy driver still prepares usable statements for database/sql. Use
		// its context-free preparation while timing the adapter call with the
		// supplied collector.
		stmt, err = c.Conn.Prepare(query)
	}
	servertiming.Record(ctx, servertiming.MetricDatabase, startedAt, time.Now(), 1)
	if err != nil {
		return nil, err
	}
	return &serverTimingStmt{Stmt: stmt}, nil
}

// ExecContext invokes the underlying context-aware execution interface and
// records one SQL operation, including failure. An unsupported interface
// returns driver.ErrSkip before recording so database/sql can select its
// prepared-statement execution path.
func (c *serverTimingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	startedAt := time.Now()
	result, err := execer.ExecContext(ctx, query, args)
	servertiming.Record(ctx, servertiming.MetricDatabase, startedAt, time.Now(), 1)
	return result, err
}

// QueryContext invokes the optional context query interface and records one
// operation and its interval. Unsupported drivers return ErrSkip before
// recording; successful non-nil rows retain ctx for later timed reads and
// cleanup, while returned errors or nil rows pass through.
func (c *serverTimingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	startedAt := time.Now()
	rows, err := queryer.QueryContext(ctx, query, args)
	servertiming.Record(ctx, servertiming.MetricDatabase, startedAt, time.Now(), 1)
	if err != nil || rows == nil {
		return rows, err
	}
	return newServerTimingRows(ctx, rows), nil
}

// BeginTx records transaction-start blocking time without increasing SQL
// operation count and wraps a successful non-nil transaction. It uses
// ConnBeginTx when available; legacy Begin requires default isolation and
// writable mode, and cannot receive the supplied context.
func (c *serverTimingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	startedAt := time.Now()
	var (
		tx  driver.Tx
		err error
	)
	if beginner, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err = beginner.BeginTx(ctx, opts)
	} else {
		if opts.Isolation != driver.IsolationLevel(0) {
			return nil, errors.New("driver does not support non-default isolation")
		}
		if opts.ReadOnly {
			return nil, errors.New("driver does not support read-only transactions")
		}
		// Legacy Begin cannot receive context or nondefault transaction options.
		// Reject those options first, then retain compatibility with its simpler
		// writable transaction.
		tx, err = c.Conn.Begin() //nolint:staticcheck // Compatibility with legacy drivers.
	}
	servertiming.RecordInterval(ctx, servertiming.MetricDatabase, startedAt, time.Now())
	if err != nil || tx == nil {
		return tx, err
	}
	return &serverTimingTx{Tx: tx, ctx: ctx}, nil
}

// Ping records the underlying optional pinger's interval without a query
// count, returning its error unchanged. Drivers without Pinger return success
// without a recorded interval; connectivity policy remains with database/sql
// callers.
func (c *serverTimingConn) Ping(ctx context.Context) error {
	if pinger, ok := c.Conn.(driver.Pinger); ok {
		startedAt := time.Now()
		err := pinger.Ping(ctx)
		servertiming.RecordInterval(ctx, servertiming.MetricDatabase, startedAt, time.Now())
		return err
	}
	return nil
}

// ResetSession records the optional session-reset interval without increasing
// query count and returns its error. Drivers lacking SessionResetter return
// success without timing; database/sql still owns connection reuse and
// lifecycle selection.
func (c *serverTimingConn) ResetSession(ctx context.Context) error {
	if resetter, ok := c.Conn.(driver.SessionResetter); ok {
		startedAt := time.Now()
		err := resetter.ResetSession(ctx)
		servertiming.RecordInterval(ctx, servertiming.MetricDatabase, startedAt, time.Now())
		return err
	}
	return nil
}

// IsValid delegates the optional driver validation check, defaulting to true
// when absent. database/sql uses the result for connection reuse; this
// adapter adds no timing observation for that check.
func (c *serverTimingConn) IsValid() bool {
	if validator, ok := c.Conn.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}

// CheckNamedValue delegates the retained driver's optional value checker or
// returns ErrSkip when absent. This allows database/sql to continue its
// normal argument conversion rather than treating absent checking support as
// an error.
func (c *serverTimingConn) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := c.Conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

// ExecContext records one statement execution interval and operation count,
// including failure. It delegates StmtExecContext when supported; otherwise
// positional conversion precedes legacy Exec, which cannot receive ctx and
// rejects named parameters.
func (s *serverTimingStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	startedAt := time.Now()
	var (
		result driver.Result
		err    error
	)
	if execer, ok := s.Stmt.(driver.StmtExecContext); ok {
		result, err = execer.ExecContext(ctx, args)
	} else {
		var values []driver.Value
		// The legacy statement accepts positional values and no context. Convert
		// before invoking it so a named argument fails instead of silently losing
		// its binding identity in the positional representation.
		values, err = namedValues(args)
		if err == nil {
			result, err = s.Stmt.Exec(values) //nolint:staticcheck // Compatibility with legacy drivers.
		}
	}
	servertiming.Record(ctx, servertiming.MetricDatabase, startedAt, time.Now(), 1)
	return result, err
}

// QueryContext records one statement query interval and operation count,
// including failure. It delegates StmtQueryContext when available; legacy
// Query accepts only converted positional values and receives no context.
// Successful rows retain ctx for later timed reads and close.
func (s *serverTimingStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	startedAt := time.Now()
	var (
		rows driver.Rows
		err  error
	)
	if queryer, ok := s.Stmt.(driver.StmtQueryContext); ok {
		rows, err = queryer.QueryContext(ctx, args)
	} else {
		var values []driver.Value
		// The legacy query receives positional values and no context. Check names
		// during conversion before its call, keeping an unrepresentable binding
		// from reaching execution under a different argument interpretation.
		values, err = namedValues(args)
		if err == nil {
			rows, err = s.Stmt.Query(values) //nolint:staticcheck // Compatibility with legacy drivers.
		}
	}
	servertiming.Record(ctx, servertiming.MetricDatabase, startedAt, time.Now(), 1)
	if err != nil || rows == nil {
		return rows, err
	}
	return newServerTimingRows(ctx, rows), nil
}

// CheckNamedValue delegates the retained driver's optional value checker or
// returns ErrSkip when absent. This allows database/sql to continue its
// normal argument conversion rather than treating absent checking support as
// an error.
func (s *serverTimingStmt) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := s.Stmt.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

// namedValues copies positional argument values into fresh driver.Value
// slice storage for legacy statement calls. Any nonempty parameter name
// returns an error before a legacy method can execute with those arguments.
//
// Values retain args slice order; Ordinal does not reorder them. A name
// cannot be represented by the positional legacy interface, so rejecting it
// prevents silent loss of binding identity. Mutable argument values remain
// shared even though the returned slice has separate storage.
func namedValues(args []driver.NamedValue) ([]driver.Value, error) {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		if arg.Name != "" {
			return nil, errors.New("named parameters are not supported")
		}
		values[i] = arg.Value
	}
	return values, nil
}

// newServerTimingRows retains rows and ctx for later timed row operations.
// The caller keeps ordinary row cleanup responsibility, and subsequent Next
// or Close intervals use this captured context even after QueryContext
// returns.
func newServerTimingRows(ctx context.Context, rows driver.Rows) *serverTimingRows {
	return &serverTimingRows{Rows: rows, ctx: ctx}
}

// Close delegates driver row cleanup and records its blocking interval
// without increasing operation count. It returns the underlying error
// unchanged; callers retain the driver's row-lifetime and repeated-close
// rules.
func (r *serverTimingRows) Close() error {
	startedAt := time.Now()
	err := r.Rows.Close()
	servertiming.RecordInterval(r.ctx, servertiming.MetricDatabase, startedAt, time.Now())
	return err
}

// Next delegates a row read and records its blocking interval, including EOF
// or error, without counting another SQL operation. Processing between calls
// is excluded; dest ownership and row-value behavior follow the underlying
// driver.
func (r *serverTimingRows) Next(dest []driver.Value) error {
	startedAt := time.Now()
	err := r.Rows.Next(dest)
	servertiming.RecordInterval(r.ctx, servertiming.MetricDatabase, startedAt, time.Now())
	return err
}

// HasNextResultSet delegates the optional multi-result-set availability check
// or returns false when absent. It records no interval for this metadata
// decision, leaving caller iteration with the driver's result-set rules.
func (r *serverTimingRows) HasNextResultSet() bool {
	if rows, ok := r.Rows.(driver.RowsNextResultSet); ok {
		return rows.HasNextResultSet()
	}
	return false
}

// NextResultSet records the underlying optional result-set advance interval
// without a query count. Drivers lacking that interface return EOF
// immediately; supported calls return their result unchanged, including
// errors.
func (r *serverTimingRows) NextResultSet() error {
	rows, ok := r.Rows.(driver.RowsNextResultSet)
	if !ok {
		return io.EOF
	}
	startedAt := time.Now()
	err := rows.NextResultSet()
	servertiming.RecordInterval(r.ctx, servertiming.MetricDatabase, startedAt, time.Now())
	return err
}

// ColumnTypeScanType returns the optional driver's preferred scan type for
// index. Drivers without that interface use any as the representation; column
// index validity remains the underlying driver's rule.
func (r *serverTimingRows) ColumnTypeScanType(index int) reflect.Type {
	if rows, ok := r.Rows.(driver.RowsColumnTypeScanType); ok {
		return rows.ColumnTypeScanType(index)
	}
	return reflect.TypeFor[any]()
}

// ColumnTypeDatabaseTypeName returns the optional driver's type name for
// index, or empty text when unsupported. The caller interprets this metadata
// using the database driver's naming conventions.
func (r *serverTimingRows) ColumnTypeDatabaseTypeName(index int) string {
	if rows, ok := r.Rows.(driver.RowsColumnTypeDatabaseTypeName); ok {
		return rows.ColumnTypeDatabaseTypeName(index)
	}
	return ""
}

// ColumnTypeLength returns the optional driver's length and availability for
// index. Unsupported metadata returns zero and false, preserving
// database/sql's ability to distinguish unknown length from an observed zero
// length.
func (r *serverTimingRows) ColumnTypeLength(index int) (int64, bool) {
	if rows, ok := r.Rows.(driver.RowsColumnTypeLength); ok {
		return rows.ColumnTypeLength(index)
	}
	return 0, false
}

// ColumnTypeNullable returns the optional driver's nullable decision and
// availability for index. Unsupported metadata returns false and false,
// distinguishing absent information from a reported non-nullable column.
func (r *serverTimingRows) ColumnTypeNullable(index int) (bool, bool) {
	if rows, ok := r.Rows.(driver.RowsColumnTypeNullable); ok {
		return rows.ColumnTypeNullable(index)
	}
	return false, false
}

// ColumnTypePrecisionScale returns the optional driver's precision, scale,
// and availability for index. Unsupported metadata returns zero, zero, and
// false; callers use the availability flag before interpreting numeric
// values.
func (r *serverTimingRows) ColumnTypePrecisionScale(index int) (int64, int64, bool) {
	if rows, ok := r.Rows.(driver.RowsColumnTypePrecisionScale); ok {
		return rows.ColumnTypePrecisionScale(index)
	}
	return 0, 0, false
}

// Commit delegates transaction completion and records its blocking interval
// through the captured begin context. It adds no SQL operation count and
// returns the underlying commit error unchanged, leaving transaction outcome
// with the driver.
func (t *serverTimingTx) Commit() error {
	startedAt := time.Now()
	err := t.Tx.Commit()
	servertiming.RecordInterval(t.ctx, servertiming.MetricDatabase, startedAt, time.Now())
	return err
}

// Rollback delegates transaction rollback and records its blocking interval
// through the captured begin context. It adds no SQL operation count and
// returns the underlying rollback error unchanged, retaining ordinary
// transaction completion rules.
func (t *serverTimingTx) Rollback() error {
	startedAt := time.Now()
	err := t.Tx.Rollback()
	servertiming.RecordInterval(t.ctx, servertiming.MetricDatabase, startedAt, time.Now())
	return err
}
