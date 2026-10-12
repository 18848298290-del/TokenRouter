package openaiattempt

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain selects Gin test mode before running tests.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
