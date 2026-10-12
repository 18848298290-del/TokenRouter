package app

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain sets Gin's global mode before tests and parallel fixtures start.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
