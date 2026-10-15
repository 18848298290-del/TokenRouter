package clientmeta

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

// claude_version.go extracts Claude CLI versions, validates overrides, and
// reports configured version-range rejections. Headers can report release
// components before a trailing suffix, while overrides need a complete release
// version; configured range checks compare three numeric components and ignore
// a leading lowercase v.

// claudeUAVersionPattern captures the release components of a leading CLI
// User-Agent for client reporting and configured ingress version checks.
var claudeUAVersionPattern = regexp.MustCompile(`(?i)^claude-cli/(\d+\.\d+\.\d+)`)

// IsSupportedClaudeCLIVersion validates an operational version override.
// The version must contain three numeric components and meet the supplied minimum.
// Prerelease and build suffixes are rejected because persisted client
// fingerprints require a release version.
// Pass minimum as a valid three-component release without a leading v; it is
// prefixed directly for semver comparison with the normalized override.
func IsSupportedClaudeCLIVersion(version, minimum string) bool {
	version = strings.TrimSpace(version)
	if version == "" {
		return false
	}
	// Persisted fingerprint overrides use complete release versions. Canonical
	// equality rejects shortened spellings, and the following suffix checks keep
	// prerelease and build metadata out of that stored identity.
	canonical := "v" + version
	if !semver.IsValid(canonical) || semver.Canonical(canonical) != canonical {
		return false
	}
	if semver.Prerelease(canonical) != "" || semver.Build(canonical) != "" {
		return false
	}
	return semver.Compare(canonical, "v"+minimum) >= 0
}

// CompareVersions compares three numeric version components.
// It returns -1, 0, or 1 after ignoring a leading lowercase v and components
// beyond patch. Missing, nonnumeric, or overflowing components count as zero.
func CompareVersions(a, b string) int {
	aParts := parseSemver(a)
	bParts := parseSemver(b)
	for i := range 3 {
		if aParts[i] < bParts[i] {
			return -1
		}
		if aParts[i] > bParts[i] {
			return 1
		}
	}
	return 0
}

// parseSemver reads major, minor, and patch numbers, treating missing or invalid
// components as zero.
// It removes one leading lowercase v and ignores further components; callers
// use the resulting numeric tuple for permissive client range comparisons.
func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	result := [3]int{0, 0, 0}
	// Range comparison treats unusable components as zero, allowing partial
	// client versions to reach a numeric ordering while override validation remains stricter.
	for i := 0; i < len(parts) && i < 3; i++ {
		if parsed, err := strconv.Atoi(parts[i]); err == nil {
			result[i] = parsed
		}
	}
	return result
}

// ExtractClaudeCLIVersion returns three numeric components from a leading CLI
// User-Agent. The prefix is case insensitive and may have trailing client or
// prerelease text; unmatched input returns an empty string.
func ExtractClaudeCLIVersion(ua string) string {
	matches := claudeUAVersionPattern.FindStringSubmatch(ua)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}

// ClaudeVersionRejection returns a client update or downgrade message for a
// missing version or a violated configured range. Empty minimum and maximum
// values omit those checks; a missing client version is rejected before either.
func ClaudeVersionRejection(clientVersion, minVersion, maxVersion string) string {
	if clientVersion == "" {
		return "Unable to determine Claude Code version. Please update Claude Code: npm update -g @anthropic-ai/claude-code"
	}

	if minVersion != "" && CompareVersions(clientVersion, minVersion) < 0 {
		return fmt.Sprintf("Your Claude Code version (%s) is below the minimum required version (%s). Please update: npm update -g @anthropic-ai/claude-code",
			clientVersion, minVersion)
	}

	if maxVersion != "" && CompareVersions(clientVersion, maxVersion) > 0 {
		return fmt.Sprintf("Your Claude Code version (%s) exceeds the maximum allowed version (%s). "+
			"Please downgrade: npm install -g @anthropic-ai/claude-code@%s && "+
			"set CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 to prevent auto-upgrade",
			clientVersion, maxVersion, maxVersion)
	}

	return ""
}
