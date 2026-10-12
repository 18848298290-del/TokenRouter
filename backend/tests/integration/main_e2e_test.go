//go:build e2e

package integration

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestMain prints the E2E configuration and runs the tests.
func TestMain(m *testing.M) {
	mode := "混合模式"
	if endpointPrefix != "" {
		mode = "Antigravity 模式"
	}
	claudeKeySet := strings.TrimSpace(os.Getenv(claudeAPIKeyEnv)) != ""
	geminiKeySet := strings.TrimSpace(os.Getenv(geminiAPIKeyEnv)) != ""
	fmt.Printf("\n🚀 E2E Gateway Tests - %s (prefix=%q, %s, %s=%v, %s=%v)\n\n",
		baseURL,
		endpointPrefix,
		mode,
		claudeAPIKeyEnv,
		claudeKeySet,
		geminiAPIKeyEnv,
		geminiKeySet,
	)
	m.Run()
}
