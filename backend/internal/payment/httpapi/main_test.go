package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain sets Gin mode before protocol tests run in parallel.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
