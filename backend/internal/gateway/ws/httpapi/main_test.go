package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain runs the package with Gin's test mode enabled. Setting the mode
// before m.Run keeps HTTP and WebSocket fixtures from emitting Gin's
// development-mode routing output.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
