package identity_test

import (
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestMain sets UTC and Gin test mode once before HTTP and storage tests run.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	gin.SetMode(gin.TestMode)
	os.Exit(runIdentityTests(m))
}
