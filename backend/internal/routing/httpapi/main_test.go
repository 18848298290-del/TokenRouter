package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain sets Gin test mode before the tests run.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
