package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain selects Gin's shared test mode before HTTP diagnostics tests
// execute. It runs the package's test entries under that mode so their router
// setup uses the same global configuration.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
