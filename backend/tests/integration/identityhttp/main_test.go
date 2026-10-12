package identityhttp_test

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain sets Gin test mode before parallel tests begin.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
