package clientip

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain puts Gin in test mode before running tests.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
