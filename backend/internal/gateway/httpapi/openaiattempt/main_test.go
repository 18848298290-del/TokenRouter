package openaiattempt

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// Gin's test mode is selected before the attempt package runs its test suite.
// The package-level setup avoids production-mode router diagnostics while
// individual fixtures create their own HTTP contexts, request identities, and
// injected execution callbacks for synchronous assertions.

// TestMain selects Gin test mode for the attempt package's test process.
// It runs the suite after that global mode change, allowing individual fixtures
// to construct HTTP contexts under the common test setting.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
