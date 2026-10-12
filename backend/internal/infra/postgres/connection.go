package postgres

import (
	"database/sql"

	"github.com/lib/pq"
)

// Open creates a lazy database handle. The caller configures the pool,
// checks connectivity, and closes the handle.
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
