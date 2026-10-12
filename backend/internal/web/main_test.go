package web

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain selects Gin test mode before executing the web package tests. It
// runs the supplied testing harness so handler fixtures use the process-wide
// test-mode setting throughout their execution.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
