package provider_test

import (
	"os"
	"testing"

	"github.com/gin-gonic/gin"
)

var runProviderTests = func(m *testing.M) int { return m.Run() }

// TestMain sets Gin test mode once before any tests run concurrently.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(runProviderTests(m))
}
