package httpx

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain selects Gin test mode before body and response helpers build request contexts.
// Running the package after this setup keeps Gin's diagnostic mode consistent
// across the HTTP helper fixtures.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
