package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// Identity HTTP tests create Gin contexts and routers inside one package
// process. Gin test mode suppresses framework debug output before those
// fixtures run; TestMain then delegates case execution to the Go testing
// harness.

// TestMain selects Gin test mode before this package creates HTTP fixtures. It
// delegates all test execution to testing.M, which supplies the package test
// result to the harness.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
