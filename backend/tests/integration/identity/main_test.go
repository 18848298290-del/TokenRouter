package identity_test

import (
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// Identity HTTP and storage cases share UTC timestamp interpretation and Gin's
// test mode for their process. TestMain sets both before selecting the
// build-specific runner, which either runs ordinary cases directly or adds
// PostgreSQL suite cleanup for integration builds.

// TestMain selects UTC and Gin test mode before invoking the build-specific
// identity runner. It exits with that runner's status after any integration
// suite resource cleanup completes.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	gin.SetMode(gin.TestMode)
	os.Exit(runIdentityTests(m))
}
