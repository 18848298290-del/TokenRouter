package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain sets Gin test mode before executing the audit HTTP test suite.
// Handler and middleware scenarios therefore run with the same global router mode.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
