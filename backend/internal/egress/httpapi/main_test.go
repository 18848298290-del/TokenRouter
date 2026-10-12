package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain switches Gin into test mode before running this package's handler
// scenarios. It invokes the test runner with that shared mode, allowing fixture
// routers to construct their responses under test configuration.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
