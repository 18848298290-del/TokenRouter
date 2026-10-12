package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain enables Gin test mode for the test process.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
