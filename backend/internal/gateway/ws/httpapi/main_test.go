package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain enables Gin test mode before running the package tests.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
