package mediaentry

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain sets Gin test mode before running the tests.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
