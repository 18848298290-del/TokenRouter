package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain sets Gin to test mode before running tests.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
