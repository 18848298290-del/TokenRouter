package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain configures Gin test mode before notification HTTP request-binding tests execute.
// The package-wide setup applies to every test in this run and then
// delegates execution to the testing framework's main runner.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
