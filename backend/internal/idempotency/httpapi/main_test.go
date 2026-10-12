package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain configures Gin test mode before running tests.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
