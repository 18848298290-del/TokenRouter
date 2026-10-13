package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain puts Gin into test mode before running the HTTP adapter tests.
// Routers created by these tests use Gin's test-mode behavior.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
