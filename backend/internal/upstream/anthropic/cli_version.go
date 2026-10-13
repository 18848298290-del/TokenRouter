package anthropic

import (
	"log/slog"
	"os"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
)

// The CLI version is resolved during package initialization and feeds both
// User-Agent defaults and billing templates. Deployment variables can select a
// supported release, while invalid overrides return the built-in pin and
// produce a warning for a nonempty rejected value.

// CLIVersionEnv names the startup override for the CLI version used in both
// request Headers and billing marker templates.
const CLIVersionEnv = "TOKENROUTER_CLAUDE_CLI_VERSION"

// resolvedCLIVersion stores the startup-selected release so separately
// constructed User-Agent and billing values use one process-wide version.
var resolvedCLIVersion = resolveCLIVersion(cliVersionOverride())

// CLIVersion returns the process-wide version resolved during package
// initialization.
func CLIVersion() string {
	return resolvedCLIVersion
}

// IsSupportedCLIVersion delegates strict release-version validation against
// CLICurrentVersion. It accepts complete three-part releases at or above that
// pin and rejects prerelease, build metadata, and shortened version spellings.
func IsSupportedCLIVersion(version string) bool {
	return clientmeta.IsSupportedClaudeCLIVersion(version, CLICurrentVersion)
}

// resolveCLIVersion trims and validates an operational override, returning the
// built-in pin when absent or invalid. A nonempty invalid override also logs
// its rejected value and the selected built-in version.
func resolveCLIVersion(raw string) string {
	version := strings.TrimSpace(raw)
	if version == "" {
		return CLICurrentVersion
	}
	// Rejected operational text cannot enter the initialized CLI Header templates,
	// so return the built-in release after recording the value that was ignored.
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

// cliVersionOverride reads the current environment variable before the legacy
// variable. Any nonempty current value takes precedence, including whitespace
// that resolveCLIVersion subsequently treats as an absent version.
func cliVersionOverride() string {
	if value := os.Getenv(CLIVersionEnv); value != "" {
		return value
	}
	// Read the legacy deployment name when the current variable is empty, allowing
	// either naming scheme to supply the startup version override.
	return os.Getenv("SUB2API_CLAUDE_CLI_VERSION")
}
