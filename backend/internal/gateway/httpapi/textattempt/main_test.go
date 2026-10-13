package textattempt

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain configures Gin test mode before the package suite starts. The suite
// then runs with the testing harness responsible for its exit status and test
// result reporting.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
