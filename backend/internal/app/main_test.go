package app

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// Gin's mode is process-wide and affects every engine constructed by app test
// fixtures. Selecting TestMode before running the suite lets those fixtures
// share the test setting from their first engine construction onward.

// TestMain selects Gin's TestMode before running the app suite. Handler
// fixtures inherit that global setting when they construct Gin engines during
// individual tests.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
