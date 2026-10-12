package server

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain selects Gin test mode for the server's HTTP and routing fixtures.
// All package tests execute after that process-wide mode is set for the engines
// and request contexts they construct.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
