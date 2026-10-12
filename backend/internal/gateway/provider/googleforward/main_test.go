package googleforward

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain configures Gin test mode for the shared fixtures.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
