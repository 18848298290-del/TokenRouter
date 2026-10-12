//go:build integration

package promotion_test

import (
	"os"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/testutil/postgrescontainer"
)

// databaseSuite shares the migrated database template within this test process.
var databaseSuite postgrescontainer.Suite

// TestMain releases the shared container after the promotion tests finish.
func TestMain(m *testing.M) { os.Exit(databaseSuite.Run(m)) }
