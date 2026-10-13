package middleware

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// Gin's mode is process-wide and these middleware tests build engines and
// isolated contexts in the same test binary. The shared entry point selects
// test mode before running the suite so individual cases start with that
// setting.

// TestMain selects Gin test mode before the middleware suite starts executing.
// It runs the testing harness so the harness determines the test binary's
// result.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
