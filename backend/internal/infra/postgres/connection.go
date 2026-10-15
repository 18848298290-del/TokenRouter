package postgres

import (
	"database/sql"

	"github.com/lib/pq"
)

// connection.go opens PostgreSQL database handles with optional driver timing
// instrumentation.
// PostgreSQL handles are opened before their connection pool is configured
// and checked at startup. Timing-enabled handles wrap pq's connector to
// observe driver calls under request collectors; both paths return a lazy
// handle whose lifetime belongs to the caller.

// Open creates a lazy PostgreSQL database handle, optionally wrapping pq's
// connector for request timing. The caller configures pooling, checks
// connectivity, and closes the returned handle; opening alone does not
// establish a connection.
func Open(dsn string, enableTiming bool) (*sql.DB, error) {
	if !enableTiming {
		return sql.Open("postgres", dsn)
	}
	connector, err := pq.NewConnector(dsn)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(NewTimingConnector(connector)), nil
}
