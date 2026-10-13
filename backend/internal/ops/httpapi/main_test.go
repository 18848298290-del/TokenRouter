package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain sets Gin global test mode before package cases create routers and
// request contexts. It runs the package suite after that shared mode is
// established, leaving test status handling to the generated testing harness.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
