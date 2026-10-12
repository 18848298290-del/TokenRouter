package clientip

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain configures Gin test mode before client-IP fixtures build their engines.
// The tests then run with the same mode for their synthetic direct-peer and
// forwarded-header requests throughout this package.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
