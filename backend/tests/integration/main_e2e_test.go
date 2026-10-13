//go:build e2e

package integration

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// E2E startup reports the target address, endpoint prefix and whether each
// gateway credential is present after trimming. A nonempty prefix selects the
// displayed Antigravity label; individual tests still decide which credentials
// and prefix values they require before making requests.

// TestMain prints deployment settings and credential presence before running
// the selected E2E tests. The startup banner helps identify the requested
// target; service reachability and protocol acceptance are checked by the tests
// themselves.
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
