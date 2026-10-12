package web

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain enables Gin test mode before running tests.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
