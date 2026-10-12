package anthropic

import (
	"log/slog"
	"os"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
)

// CLIVersionEnv overrides the built-in CLI version at process startup.
// This supports upstream models that require a newer Claude Code version.
const CLIVersionEnv = "TOKENROUTER_CLAUDE_CLI_VERSION"

// resolvedCLIVersion is read once so separately constructed User-Agent and
// billing markers use the same version throughout the process.
var resolvedCLIVersion = resolveCLIVersion(cliVersionOverride())

// CLIVersion returns the effective three-part CLI version. Use it for outgoing
// requests; CLICurrentVersion is the built-in default before overrides.
func CLIVersion() string {
	return resolvedCLIVersion
}

// IsSupportedCLIVersion validates version against the built-in CLI version.
func IsSupportedCLIVersion(version string) bool {
	return clientmeta.IsSupportedClaudeCLIVersion(version, CLICurrentVersion)
}

// resolveCLIVersion uses the built-in version for empty or invalid overrides.
// A nonempty invalid value also produces a warning.
func resolveCLIVersion(raw string) string {
	version := strings.TrimSpace(raw)
	if version == "" {
		return CLICurrentVersion
	}
	if !IsSupportedCLIVersion(version) {
		slog.Warn("ignoring invalid Claude CLI version override; falling back to the built-in pin",
			"env", CLIVersionEnv,
			"value", version,
			"builtin", CLICurrentVersion,
			"requirement", "strict three-part semver, not older than the built-in pin")
		return CLICurrentVersion
	}
	return version
}

// cliVersionOverride prefers a nonempty current variable over the legacy variable.
func cliVersionOverride() string {
	if value := os.Getenv(CLIVersionEnv); value != "" {
		return value
	}
	return os.Getenv("SUB2API_CLAUDE_CLI_VERSION")
}
