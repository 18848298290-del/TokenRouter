package openai

import (
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
)

const (
	// CodexUpstreamMinVersion is the minimum version header accepted by /backend-api/codex.
	// Older explicit versions produced HTTP 404 in July 2026 (issue #3901).
	CodexUpstreamMinVersion = "0.144.0"

	CodexClientVersionMaxLen = 64

	CodexCLIVersion   = "0.144.1"
	CodexCLIUserAgent = CodexDefaultOriginator + "/" + CodexCLIVersion + " (Ubuntu 22.4.0; x86_64) xterm-256color"

	CodexDefaultOriginator = clientmeta.CodexDefaultOriginator
)

var (
	codexClientVersionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}(-[0-9A-Za-z.]+)?$`)

	codexCanonicalUAMu       sync.RWMutex
	codexCanonicalUAResolver func() string
)

type CodexOutboundIdentity struct {
	UserAgent  string
	Originator string
	Version    string
}

// SetCodexCanonicalUserAgentResolver installs the settings-backed UA resolver.
// Missing or invalid configured identities fall back to the compiled default.
func SetCodexCanonicalUserAgentResolver(resolver func() string) {
	codexCanonicalUAMu.Lock()
	defer codexCanonicalUAMu.Unlock()
	codexCanonicalUAResolver = resolver
}

// CodexCanonicalUserAgent returns the effective canonical Codex User-Agent.
func CodexCanonicalUserAgent() string {
	return ResolveCodexOutboundIdentity("").UserAgent
}

// CodexCanonicalAuthIdentity returns the UA and originator for credential requests, which omit version.
func CodexCanonicalAuthIdentity() (userAgent, originator string) {
	identity := ResolveCodexOutboundIdentity("")
	return identity.UserAgent, identity.Originator
}

// ApplyCodexCanonicalAuthIdentity sets the canonical UA and originator on credential requests.
func ApplyCodexCanonicalAuthIdentity(h http.Header) {
	if h == nil {
		return
	}
	userAgent, originator := CodexCanonicalAuthIdentity()
	h.Set("user-agent", userAgent)
	h.Set("originator", originator)
}

// CodexCanonicalClientVersion returns the version derived from the canonical UA.
func CodexCanonicalClientVersion() string {
	return ResolveCodexOutboundIdentity("").Version
}

func ConfiguredCodexUserAgent() string {
	codexCanonicalUAMu.RLock()
	resolver := codexCanonicalUAResolver
	codexCanonicalUAMu.RUnlock()
	if resolver != nil {
		if value := strings.TrimSpace(resolver()); value != "" {
			return value
		}
	}
	return CodexCLIUserAgent
}

// NormalizeCodexClientVersion accepts bounded ASCII version strings for outbound headers.
func NormalizeCodexClientVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" || len(version) > CodexClientVersionMaxLen || !codexClientVersionPattern.MatchString(version) {
		return ""
	}
	return version
}

func ResolveCodexOutboundIdentity(candidateUA string) CodexOutboundIdentity {
	canonical := ConfiguredCodexUserAgent()
	ua := strings.TrimSpace(candidateUA)
	if ua == "" {
		ua = canonical
	}
	originator, pairedUA, ok := clientmeta.PairCodexClientIdentity(ua)
	if !ok {
		originator, pairedUA, ok = clientmeta.PairCodexClientIdentity(canonical)
	}
	if !ok {
		originator, pairedUA = clientmeta.CodexDefaultOriginator, CodexCLIUserAgent
	}
	version := CodexClientVersionFromUA(canonical)
	if rebuilt := ReplaceCodexUserAgentVersion(pairedUA, version); rebuilt != "" {
		pairedUA = rebuilt
	}
	return CodexOutboundIdentity{UserAgent: pairedUA, Originator: originator, Version: version}
}

func CodexClientVersionFromUA(userAgent string) string {
	version, ok := clientmeta.ParseCodexEngineVersion(userAgent)
	if !ok {
		return CodexCLIVersion
	}
	version = NormalizeCodexClientVersion(version)
	if version == "" || clientmeta.CompareVersions(version, CodexUpstreamMinVersion) < 0 {
		return CodexCLIVersion
	}
	return version
}

func ReplaceCodexUserAgentVersion(userAgent, version string) string {
	version = NormalizeCodexClientVersion(version)
	if version == "" {
		return ""
	}
	slash := strings.IndexByte(userAgent, '/')
	if slash <= 0 || slash+1 >= len(userAgent) {
		return ""
	}
	end := slash + 1
	for end < len(userAgent) && userAgent[end] != ' ' && userAgent[end] != '(' {
		end++
	}
	return userAgent[:slash+1] + version + userAgent[end:]
}

// EnsureCodexIdentityHeaders fills missing OAuth identity headers and sets the Responses beta.
// Call EnforceCodexIdentityHeaders afterward to pair the identity and correct old versions.
func EnsureCodexIdentityHeaders(h http.Header) {
	if h == nil {
		return
	}
	identity := ResolveCodexOutboundIdentity("")
	if strings.TrimSpace(h.Get("user-agent")) == "" {
		h.Set("user-agent", identity.UserAgent)
	}
	if strings.TrimSpace(h.Get("originator")) == "" {
		h.Set("originator", identity.Originator)
	}
	if strings.TrimSpace(h.Get("version")) == "" {
		h.Set("version", identity.Version)
	}
	h.Set("OpenAI-Beta", "responses=experimental")
}

// ApplyOpenAICodexProbeHeaders adds Codex identity headers and a fresh window ID to Responses probes.
func ApplyOpenAICodexProbeHeaders(h http.Header) {
	if h == nil {
		return
	}
	EnsureCodexIdentityHeaders(h)
	h.Set("X-Codex-Window-ID", uuid.NewString())
}

// EnforceCodexIdentityHeaders pairs the OAuth originator with the final official User-Agent
// and raises an explicit version below CodexUpstreamMinVersion. Unrecognized UAs use
// the canonical identity; mismatched identities can cause upstream 404s (issue #3901).
// Call after all UA overrides. Requests without originator are unchanged; use
// EnsureCodexIdentityHeaders first when missing identity headers need to be filled.
func EnforceCodexIdentityHeaders(h http.Header) {
	EnforceCodexIdentityHeadersWithUA(h, "")
}

// EnforceCodexIdentityHeadersWithUA pairs an official UA with its originator,
// using overrideUA when supplied. Unrecognized identities use the canonical UA.
// An old version header is corrected independently of the UA.
func EnforceCodexIdentityHeadersWithUA(h http.Header, overrideUA string) {
	if h == nil || h.Get("originator") == "" {
		return
	}
	candidateUA := strings.TrimSpace(overrideUA)
	if candidateUA == "" {
		candidateUA = h.Get("user-agent")
	}
	originator, pairedUA, ok := clientmeta.PairCodexClientIdentity(candidateUA)
	if !ok {
		identity := ResolveCodexOutboundIdentity("")
		originator, pairedUA = identity.Originator, identity.UserAgent
	}
	h.Set("user-agent", pairedUA)
	h.Set("originator", originator)
	if v := strings.TrimSpace(h.Get("version")); v != "" && clientmeta.CompareVersions(v, CodexUpstreamMinVersion) < 0 {
		h.Set("version", CodexCanonicalClientVersion())
	}
}

// ResolveUpstreamOriginator chooses a routed override, inbound value, official default, or compatibility default.
func ResolveUpstreamOriginator(read func() string, official, matched bool, routed string) string {
	if matched {
		if value := strings.TrimSpace(routed); value != "" {
			return value
		}
	}
	if value := strings.TrimSpace(read()); value != "" {
		return value
	}
	if official {
		return ResolveCodexOutboundIdentity("").Originator
	}
	return "opencode"
}

// IsBrowserUserAgent reports whether the User-Agent identifies a browser.
func IsBrowserUserAgent(userAgent string) bool { return clientmeta.IsBrowserUserAgent(userAgent) }

// IsCodexOfficialClientRequest reports whether the UA identifies an official Codex client.
func IsCodexOfficialClientRequest(userAgent string) bool {
	return clientmeta.IsCodexOfficialClientRequest(userAgent)
}

// IsCodexOfficialClientRequestStrict validates the full official Codex identity format.
func IsCodexOfficialClientRequestStrict(userAgent string) bool {
	return clientmeta.IsCodexOfficialClientRequestStrict(userAgent)
}

// IsCodexOfficialClientOriginator recognizes official Codex originators.
func IsCodexOfficialClientOriginator(originator string) bool {
	return clientmeta.IsCodexOfficialClientOriginator(originator)
}

// IsCodexOfficialClientByHeaders recognizes official clients from their UA and originator.
func IsCodexOfficialClientByHeaders(userAgent, originator string) bool {
	return clientmeta.IsCodexOfficialClientByHeaders(userAgent, originator)
}

// PairCodexClientIdentity derives a matching originator and User-Agent.
func PairCodexClientIdentity(userAgent string) (originator string, pairedUA string, ok bool) {
	return clientmeta.PairCodexClientIdentity(userAgent)
}

// normalizeCodexClientHeader normalizes headers used by client allow rules.
func normalizeCodexClientHeader(value string) string {
	return clientmeta.NormalizeCodexClientHeader(value)
}
