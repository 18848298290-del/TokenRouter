package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain sets Gin test mode before running the backup HTTP fixture suite.
// Legacy availability and restore-password scenarios therefore construct routers with the same global mode.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
