package clientmeta

import (
	"regexp"
	"strings"
)

// openai.go recognizes Codex client formats and pairs outbound User-Agent and
// originator values. Codex format recognition serves two callers with different
// requirements. Passthrough compatibility accepts known markers inside a
// composite User-Agent, while codex_only checks require a leading marker or a
// recognized client name in the final parenthesized trailer. Both checks
// inspect requester-supplied values, so their result identifies a client format
// for an already authorized request.
//
// Outbound identity pairing also needs the originator and leading User-Agent
// name to agree. A recognized leading name wins; otherwise a recognized trailer
// can replace it while retaining the version and platform suffix. Exact known
// identifiers are canonicalized to lowercase, while Codex family names retain
// their spelling. Pairing rejects names outside the printable ASCII and length
// limits and reports failure for callers to choose a default identity.

// Codex identity constants define the separately matched family prefix, valid
// originator length, and default identity used by outbound callers.
const (
	// codexOfficialClientFamilyPrefix recognizes family names such as Codex Desktop.
	// Keep it outside the normalized prefix list, which would trim the required
	// trailing space separating the family from its display name.
	codexOfficialClientFamilyPrefix = "codex "

	// codexOriginatorMaxLen bounds clientInfo.name to a short ASCII identifier.
	codexOriginatorMaxLen = 64

	// CodexDefaultOriginator is the default Codex TUI identity.
	CodexDefaultOriginator = "codex-tui"
)

// Codex recognition tables distinguish embedded compatibility markers from
// exact originator names, and the version pattern extracts reported engine releases.
var (
	// CodexCLIUserAgentPrefixes lists historical CLI User-Agent prefixes.
	// Examples include "codex_vscode/1.0.0" and "codex_cli_rs/0.1.2".
	CodexCLIUserAgentPrefixes = []string{
		"codex_vscode/",
		"codex_cli_rs/",
	}

	// CodexOfficialClientUserAgentPrefixes lists recognized client User-Agent
	// prefixes. The space in the Codex family prefix is handled separately by
	// codexOfficialClientFamilyPrefix.
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

	// codexOfficialClientOriginators requires exact names so lookalikes such as
	// evil-codex_cli fail strict checks.
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

	// codexEngineVersionPattern extracts leading X.Y.Z components, ignoring
	// suffixes such as -alpha when reporting a client's engine release.
	codexEngineVersionPattern = regexp.MustCompile(`^(\d+\.\d+\.\d+)`)
)

// IsBrowserUserAgent recognizes the Mozilla/ prefix used by common browsers.
// Callers use this classification when handling upstream Cloudflare JavaScript
// challenges. Matching trims whitespace and ignores prefix case; an empty
// User-Agent returns false.
func IsBrowserUserAgent(userAgent string) bool {
	ua := strings.TrimSpace(userAgent)
	if ua == "" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(ua), "mozilla/")
}

// IsCodexCLIRequest recognizes historical Codex CLI User-Agent markers.
// Matching trims whitespace and folds case, accepting a configured prefix
// anywhere in a composite header. Empty or unmatched headers return false.
func IsCodexCLIRequest(userAgent string) bool {
	ua := NormalizeCodexClientHeader(userAgent)
	if ua == "" {
		return false
	}
	return matchCodexClientHeaderPrefixes(ua, CodexCLIUserAgentPrefixes)
}

// IsCodexOfficialClientRequest recognizes Codex markers in a composite User-Agent
// for passthrough compatibility. It accepts embedded known prefixes, a leading
// Codex family name, or a recognized name in the final parenthesized trailer.
func IsCodexOfficialClientRequest(userAgent string) bool {
	return isCodexOfficialClientRequest(userAgent, false)
}

// IsCodexOfficialClientRequestStrict recognizes leading Codex prefixes or a
// recognized final trailer name for codex_only checks. Embedded compatibility
// markers alone do not qualify, while a leading Codex family name does.
func IsCodexOfficialClientRequestStrict(userAgent string) bool {
	return isCodexOfficialClientRequest(userAgent, true)
}

// isCodexOfficialClientRequest applies the selected prefix rule before trying
// family and trailer recognition. Input is trimmed and case folded; strict
// selects leading-prefix matching and leaves recognized trailer support enabled.
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
		// Originator overrides can hide the known name at the start of the
		// header. A recognized final trailer still supplies the client-format identity.
		return IsCodexOfficialClientOriginator(name)
	}
	return false
}

// codexUATrailerName extracts clientInfo.name from the last parenthesized
// User-Agent group.
// An originator override may change the leading name while the (name; version)
// trailer still identifies codex-tui.
// Callers supply trimmed input; missing parentheses produce an empty string.
// Extraction preserves case. Matching callers lowercase input, while
// PairCodexClientIdentity needs the supplied spelling for outbound pairing.
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

// IsCodexOfficialClientOriginator accepts exact known names and the Codex
// space-prefixed family.
// Matching trims whitespace and ignores case; arbitrary names containing a
// known identifier do not qualify. The required family-prefix space separates
// family names from unrelated codex-prefixed values.
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

// IsCodexOfficialClientByHeaders recognizes a client by its User-Agent or
// originator.
// Either supplied value can establish the format. The User-Agent uses the
// compatibility matcher, so embedded markers can pass even when originator is empty.
func IsCodexOfficialClientByHeaders(userAgent, originator string) bool {
	return IsCodexOfficialClientRequest(userAgent) || IsCodexOfficialClientOriginator(originator)
}

// NormalizeCodexClientHeader trims surrounding whitespace and lowercases a client header.
func NormalizeCodexClientHeader(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// matchCodexClientHeaderPrefixes checks normalized header text for configured
// compatibility markers. Callers normalize value; prefixes are normalized here,
// empty prefixes are skipped, and a marker may occur within a composite header.
func matchCodexClientHeaderPrefixes(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		normalizedPrefix := NormalizeCodexClientHeader(prefix)
		if normalizedPrefix == "" {
			continue
		}
		// Compatibility forwarding accepts a Codex marker alongside another
		// client's User-Agent. This substring rule is separate from codex_only's
		// leading-prefix requirement.
		if strings.HasPrefix(value, normalizedPrefix) || strings.Contains(value, normalizedPrefix) {
			return true
		}
	}
	return false
}

// matchCodexClientHeaderStrictPrefixes requires a recognized prefix at the
// start of the header.
// Callers pass a normalized value; configured prefixes are normalized and empty
// entries are skipped. Embedded compatibility markers do not satisfy this check.
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

// PairCodexClientIdentity returns a matching originator and User-Agent for an
// outbound Codex request. It requires a slash after a nonempty leading name;
// when neither the leading name nor trailer supplies a usable recognized
// identity, it returns empty values and ok=false for the caller's default selection.
//
// A recognized leading name takes precedence over the final (name; version)
// trailer. Otherwise a recognized trailer replaces the leading name while the
// suffix after the first slash retains its version and platform data. Exact
// known names become lowercase and family names retain their spelling, keeping
// the returned originator equal to the rebuilt header's leading name.
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
	// A trailer may restore an identity hidden by a leading-name override.
	// Reject embedded slashes because reconstruction would otherwise split the
	// originator differently from the User-Agent's leading name.
	if trailer := codexUATrailerName(ua); trailer != "" && !strings.ContainsRune(trailer, '/') &&
		isSaneCodexOriginator(trailer) && IsCodexOfficialClientOriginator(trailer) {
		trailer = canonicalizeCodexOriginator(trailer)
		return trailer, trailer + ua[slash:], true
	}
	return "", "", false
}

// isSaneCodexOriginator checks the length bound and printable ASCII characters.
// Pairing applies this byte-level check to whichever name it will write into
// both outbound headers. Empty, oversized, control-byte, and non-ASCII names fail.
func isSaneCodexOriginator(name string) bool {
	if name == "" || len(name) > codexOriginatorMaxLen {
		return false
	}
	// Outbound names use printable ASCII bytes, so control and multibyte data
	// cannot become an originator even when its prefix resembles a known family.
	for i := range len(name) {
		if c := name[i]; c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// canonicalizeCodexOriginator lowercases exact names in the known-originator
// table. Other names retain their supplied spelling, so PairCodexClientIdentity
// can carry family display names into both outbound header values consistently.
func canonicalizeCodexOriginator(name string) string {
	if lower := NormalizeCodexClientHeader(name); codexOfficialClientOriginators[lower] {
		return lower
	}
	return name
}

// ParseCodexEngineVersion extracts X.Y.Z from `{originator}/{X.Y.Z} (...)`.
// The version lies after the first slash and before a space or opening parenthesis.
// OpenAI's CodexClientVersionFromUA uses the reported release when selecting
// the outbound version header.
// Leading numeric release components are returned even with a following suffix;
// missing slash or unmatched version text yields an empty string and false.
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
