package googleforward

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain sets Gin test mode before running shared Google forwarding fixtures.
// The package test runner then executes the registered tests using that mode
// for HTTP context construction.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
