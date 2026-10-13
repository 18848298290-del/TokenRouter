//go:build e2e

package integration

import (
	"os"
)

// Deployment URLs, endpoint prefixes and administrator credentials are selected
// before E2E requests are built. Empty environment values select caller
// defaults, while whitespace remains part of a configured value and can
// therefore affect request paths or credentials.

// getEnv reads key and returns defaultVal when the variable is unset or empty.
// Whitespace is retained, so callers that need trimmed credentials perform that
// check themselves.
func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
