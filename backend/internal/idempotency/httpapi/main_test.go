package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain configures Gin test mode before executing the idempotency HTTP suite.
// All administrator and user fixtures therefore construct routers with the same global mode.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
