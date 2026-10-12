package clientmeta

import (
	"regexp"
	"strings"
)

const (
	// codexOfficialClientFamilyPrefix recognizes family names such as Codex Desktop.
	// Keep it outside the normalized prefix list, which would trim the required trailing space.
	codexOfficialClientFamilyPrefix = "codex "

	// codexOriginatorMaxLen bounds clientInfo.name to a short ASCII identifier.
	codexOriginatorMaxLen = 64

	// CodexDefaultOriginator is the default Codex TUI identity.
	CodexDefaultOriginator = "codex-tui"
)

var (
	// CodexCLIUserAgentPrefixes lists historical CLI User-Agent prefixes.
	// Examples include "codex_vscode/1.0.0" and "codex_cli_rs/0.1.2".
	CodexCLIUserAgentPrefixes = []string{
		"codex_vscode/",
		"codex_cli_rs/",
	}

	// CodexOfficialClientUserAgentPrefixes lists recognized client User-Agent prefixes.
	// The space in the Codex family prefix is handled separately by codexOfficialClientFamilyPrefix.
	CodexOfficialClientUserAgentPrefixes = []string{
		"codex_cli_rs/",
		"codex-tui/",
		"codex_vscode/",
		"codex_vscode_copilot/",
		"codex_app/",
		"codex_chatgpt_desktop/",
		"codex_atlas/",
		"codex_exec/",
		"codex_sdk_ts/",
	}

	// codexOfficialClientOriginators requires exact names so lookalikes such as evil-codex_cli fail strict checks.
	codexOfficialClientOriginators = map[string]bool{
		"codex_cli_rs":          true,
		"codex-tui":             true,
		"codex_vscode":          true,
		"codex_vscode_copilot":  true,
		"codex_app":             true,
		"codex_chatgpt_desktop": true,
		"codex_atlas":           true,
		"codex_exec":            true,
		"codex_sdk_ts":          true,
	}

	// codexEngineVersionPattern extracts leading X.Y.Z components, ignoring suffixes such as -alpha.
	codexEngineVersionPattern = regexp.MustCompile(`^(\d+\.\d+\.\d+)`)
)

// IsBrowserUserAgent recognizes the Mozilla/ prefix used by common browsers.
// Callers use this classification when handling upstream Cloudflare JavaScript challenges.
func IsBrowserUserAgent(userAgent string) bool {
	ua := strings.TrimSpace(userAgent)
	if ua == "" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(ua), "mozilla/")
}

// IsCodexCLIRequest recognizes historical Codex CLI User-Agent markers.
func IsCodexCLIRequest(userAgent string) bool {
	ua := NormalizeCodexClientHeader(userAgent)
	if ua == "" {
		return false
	}
	return matchCodexClientHeaderPrefixes(ua, CodexCLIUserAgentPrefixes)
}

// IsCodexOfficialClientRequest accepts client markers embedded in composite User-Agent values for passthrough compatibility.
func IsCodexOfficialClientRequest(userAgent string) bool {
	return isCodexOfficialClientRequest(userAgent, false)
}

// IsCodexOfficialClientRequestStrict recognizes official prefixes or a recognized trailer name for codex_only checks.
func IsCodexOfficialClientRequestStrict(userAgent string) bool {
	return isCodexOfficialClientRequest(userAgent, true)
}

func isCodexOfficialClientRequest(userAgent string, strict bool) bool {
	ua := NormalizeCodexClientHeader(userAgent)
	if ua == "" {
		return false
	}
	if strict {
		if matchCodexClientHeaderStrictPrefixes(ua, CodexOfficialClientUserAgentPrefixes) {
			return true
		}
	} else if matchCodexClientHeaderPrefixes(ua, CodexOfficialClientUserAgentPrefixes) {
		return true
	}
	if strings.HasPrefix(ua, codexOfficialClientFamilyPrefix) {
		return true
	}
	if name := codexUATrailerName(ua); name != "" {
		return IsCodexOfficialClientOriginator(name)
	}
	return false
}

// codexUATrailerName extracts clientInfo.name from the last parenthesized User-Agent group.
// An originator override may change the leading name while the (name; version) trailer still identifies codex-tui.
// Callers supply trimmed input; missing parentheses produce an empty string.
// Extraction preserves case. Matching callers lowercase input; PairCodexClientIdentity needs the original spelling.
func codexUATrailerName(ua string) string {
	_, after0, ok0 := strings.CutLast(ua, "(")
	if !ok0 {
		return ""
	}
	rest := after0
	before, _, ok := strings.Cut(rest, ")")
	if !ok {
		return ""
	}
	inner := strings.TrimSpace(before)
	if semi := strings.Index(inner, ";"); semi >= 0 {
		inner = strings.TrimSpace(inner[:semi])
	}
	return inner
}

// IsCodexOfficialClientOriginator accepts exact known names and the Codex space-prefixed family.
func IsCodexOfficialClientOriginator(originator string) bool {
	v := NormalizeCodexClientHeader(originator)
	if v == "" {
		return false
	}
	if codexOfficialClientOriginators[v] {
		return true
	}
	return strings.HasPrefix(v, codexOfficialClientFamilyPrefix)
}

// IsCodexOfficialClientByHeaders recognizes a client by its User-Agent or originator.
func IsCodexOfficialClientByHeaders(userAgent, originator string) bool {
	return IsCodexOfficialClientRequest(userAgent) || IsCodexOfficialClientOriginator(originator)
}

func NormalizeCodexClientHeader(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func matchCodexClientHeaderPrefixes(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		normalizedPrefix := NormalizeCodexClientHeader(prefix)
		if normalizedPrefix == "" {
			continue
		}
		// Substring matching accepts markers embedded in composite gateway headers.
		if strings.HasPrefix(value, normalizedPrefix) || strings.Contains(value, normalizedPrefix) {
			return true
		}
	}
	return false
}

// matchCodexClientHeaderStrictPrefixes requires a recognized prefix at the start of the header.
func matchCodexClientHeaderStrictPrefixes(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		normalizedPrefix := NormalizeCodexClientHeader(prefix)
		if normalizedPrefix == "" {
			continue
		}
		if strings.HasPrefix(value, normalizedPrefix) {
			return true
		}
	}
	return false
}

// PairCodexClientIdentity derives a matching originator and rewrites the User-Agent leading name when needed.
// The upstream Codex endpoint requires the originator to match the name before the first slash.
// A recognized leading name takes precedence over the (name; version) trailer. The suffix retains version and OS data.
// If neither name is recognized, ok is false and callers can supply a default identity.
func PairCodexClientIdentity(userAgent string) (originator string, pairedUA string, ok bool) {
	ua := strings.TrimSpace(userAgent)
	slash := strings.IndexByte(ua, '/')
	if slash <= 0 {
		return "", "", false
	}
	if leading := strings.TrimSpace(ua[:slash]); isSaneCodexOriginator(leading) && IsCodexOfficialClientOriginator(leading) {
		leading = canonicalizeCodexOriginator(leading)
		return leading, leading + ua[slash:], true
	}
	// Keep family-name case and reject slashes so the leading name agrees with the originator.
	if trailer := codexUATrailerName(ua); trailer != "" && !strings.ContainsRune(trailer, '/') &&
		isSaneCodexOriginator(trailer) && IsCodexOfficialClientOriginator(trailer) {
		trailer = canonicalizeCodexOriginator(trailer)
		return trailer, trailer + ua[slash:], true
	}
	return "", "", false
}

// isSaneCodexOriginator checks the length bound and printable ASCII characters.
func isSaneCodexOriginator(name string) bool {
	if name == "" || len(name) > codexOriginatorMaxLen {
		return false
	}
	for i := range len(name) {
		if c := name[i]; c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// canonicalizeCodexOriginator lowercases exact known originators.
// Codex family names keep their spelling because upstream family matching
// uses the case-sensitive "Codex " prefix.
func canonicalizeCodexOriginator(name string) string {
	if lower := NormalizeCodexClientHeader(name); codexOfficialClientOriginators[lower] {
		return lower
	}
	return name
}

// ParseCodexEngineVersion extracts X.Y.Z from `{originator}/{X.Y.Z} (...)`.
// The version lies after the first slash and before a space or opening parenthesis.
// This is the codex-rs engine version shared by CLI and app-server.
func ParseCodexEngineVersion(ua string) (string, bool) {
	ua = strings.TrimSpace(ua)
	slash := strings.IndexByte(ua, '/')
	if slash < 0 {
		return "", false
	}
	rest := ua[slash+1:]
	end := len(rest)
	for i := range len(rest) {
		if rest[i] == ' ' || rest[i] == '(' {
			end = i
			break
		}
	}
	m := codexEngineVersionPattern.FindString(strings.TrimSpace(rest[:end]))
	if m == "" {
		return "", false
	}
	return m, true
}
