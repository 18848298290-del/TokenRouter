package mediaentry

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain sets Gin test mode before running the mediaentry package suite.
// Every test in this process uses that mode for its HTTP fixture behavior.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
