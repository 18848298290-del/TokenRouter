package identityhttp_test

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// Gin mode is process-wide, so authentication HTTP fixtures share the mode
// selected before package tests run. Selecting test mode once keeps handler
// contexts, including parallel cases, under the same Gin configuration
// throughout runner execution.

// TestMain selects Gin's test mode before executing this package's tests. The
// shared mode is set before m.Run, including before parallel cases construct
// their handler contexts.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
