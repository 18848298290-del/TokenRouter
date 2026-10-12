package clientmeta

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

var claudeUAVersionPattern = regexp.MustCompile(`(?i)^claude-cli/(\d+\.\d+\.\d+)`)

// IsSupportedClaudeCLIVersion validates an operational version override.
// The version must contain three numeric components and meet the supplied minimum.
// Prerelease and build suffixes are rejected because persisted client fingerprints require a release version.
// The caller supplies the baseline used by identity version checks.
func IsSupportedClaudeCLIVersion(version, minimum string) bool {
	version = strings.TrimSpace(version)
	if version == "" {
		return false
	}
	// Canonical equality rejects shortened versions; separate checks reject prerelease and build metadata.
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
// It returns -1, 0, or 1. Missing or nonnumeric components count as zero.
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

// parseSemver reads major, minor, and patch numbers, treating missing or invalid components as zero.
func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	result := [3]int{0, 0, 0}
	for i := 0; i < len(parts) && i < 3; i++ {
		if parsed, err := strconv.Atoi(parts[i]); err == nil {
			result[i] = parsed
		}
	}
	return result
}

func ExtractClaudeCLIVersion(ua string) string {
	matches := claudeUAVersionPattern.FindStringSubmatch(ua)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}

// ClaudeVersionRejection returns a rejection message for missing or out-of-range versions, otherwise an empty string.
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
