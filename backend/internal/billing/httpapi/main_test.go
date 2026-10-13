package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain enables Gin test mode before running this package's test process.
// It invokes the supplied test runner so handler and route cases share the
// same Gin mode during execution.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
