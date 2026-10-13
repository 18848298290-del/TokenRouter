//go:build integration

package promotion_test

import (
	"os"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/testutil/postgrescontainer"
)

// Promotion integration cases clone independent databases from one process-wide
// migrated PostgreSQL template. TestMain keeps the suite available through
// per-case fixture cleanup, then closes its shared container and exits with the
// suite's test and cleanup result.

// databaseSuite owns the migrated PostgreSQL template and independent promotion
// database clones until the package runner ends.
var databaseSuite postgrescontainer.Suite

// TestMain runs promotion cases through the shared PostgreSQL suite and exits
// with its result. Per-case clone cleanup finishes before the suite releases
// the process-wide container.
func TestMain(m *testing.M) { os.Exit(databaseSuite.Run(m)) }
