package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain selects Gin test mode before the payment HTTP test suite starts. It
// runs the suite after that shared mode change so parallel protocol cases use
// the same configuration.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
