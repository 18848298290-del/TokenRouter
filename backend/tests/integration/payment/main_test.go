//go:build !integration

package payment_test

import (
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestMain configures Gin test mode and UTC before running HTTP tests.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	// UTC makes time fixtures independent of the host time zone.
	time.Local = time.UTC
	m.Run()
}
