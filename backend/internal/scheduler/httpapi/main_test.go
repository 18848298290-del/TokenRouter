package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain sets the shared Gin mode before tests start.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
