package provider_test

import (
	"os"
	"testing"

	"github.com/gin-gonic/gin"
)

// Gin's mode is process-wide, so provider tests select test mode before the
// test runner begins. The runner callback normally invokes testing.M.Run and is
// replaced by integration setup when that build tag is enabled.

// runProviderTests selects the process-wide runner callback; integration init
// replaces its ordinary testing.M.Run behavior with PostgreSQL setup and cleanup.
var runProviderTests = func(m *testing.M) int { return m.Run() }

// TestMain selects Gin test mode before running provider tests through the
// configured callback. Integration builds replace that callback with database
// setup, and its return code becomes the process exit status.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(runProviderTests(m))
}
