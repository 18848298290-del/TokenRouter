package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain sets Gin test mode before invoking the HTTP package tests. This
// setup keeps router diagnostics consistent for the suite while the Go test
// runner records the result of m.Run.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
