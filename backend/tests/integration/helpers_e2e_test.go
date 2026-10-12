//go:build e2e

package integration

import (
	"os"
)

// getEnv returns the environment value, or defaultVal when it is empty.
func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
