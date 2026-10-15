package testutil

import "github.com/gin-gonic/gin"

// httptest.go sets Gin's process-wide test mode when the fixture package
// initializes.
// Packages importing these HTTP test helpers share Gin's process-wide mode
// setting. Initialization selects TestMode before those packages create test
// routers, suppressing Gin's normal debug output during handler fixtures and
// leaving the mode in effect for that process.

// init selects Gin TestMode for packages that import these shared test helpers.
// This changes Gin's process-global mode before fixture construction, so other
// tests in the same process observe that selection.
func init() {
	gin.SetMode(gin.TestMode)
}
