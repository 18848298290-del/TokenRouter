package openai

import (
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
)

// Codex OAuth requests pair the originator header with the leading identity in
// the outgoing User-Agent. A routed or client-supplied official identity can
// remain recognizable, while invalid candidates fall back through the
// configured canonical User-Agent to the compiled identity.
//
// The canonical version comes from the configured identity and is used when
// rebuilding outbound identities or filling absent headers. Enforcement treats
// an existing version header separately: values below the accepted floor are
// raised, while an absent version stays absent. Resolver publication is
// protected by a mutex, but invoking the resolver happens after releasing that
// mutex so settings reads do not block installation or replacement of the
// callback.

// These identity constants define the accepted version floor, bounded header
// size, and compiled client identity used when settings cannot supply one.
const (
	// CodexUpstreamMinVersion is the local floor used when preparing Codex
	// version headers; identity enforcement raises supplied values below it.
	CodexUpstreamMinVersion = "0.144.0"

	CodexClientVersionMaxLen = 64

	CodexCLIVersion   = "0.144.1"
	CodexCLIUserAgent = CodexDefaultOriginator + "/" + CodexCLIVersion + " (Ubuntu 22.4.0; x86_64) xterm-256color"

	CodexDefaultOriginator = clientmeta.CodexDefaultOriginator
)

// The version pattern validates header syntax; codexCanonicalUAMu protects
// publication of codexCanonicalUAResolver while callback execution happens unlocked.
var (
	codexClientVersionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}(-[0-9A-Za-z.]+)?$`)

	codexCanonicalUAMu       sync.RWMutex
	codexCanonicalUAResolver func() string
)

// CodexOutboundIdentity contains a paired User-Agent, originator, and selected
// outbound version.
type CodexOutboundIdentity struct {
	UserAgent  string // paired outgoing User-Agent value
	Originator string // paired outgoing originator value
	Version    string // selected outgoing version header
}

// SetCodexCanonicalUserAgentResolver replaces the callback used to read the
// configured canonical User-Agent. Nil restores the compiled selection path;
// concurrent readers copy the callback under a read lock before invoking it.
func SetCodexCanonicalUserAgentResolver(resolver func() string) {
	codexCanonicalUAMu.Lock()
	defer codexCanonicalUAMu.Unlock()
	codexCanonicalUAResolver = resolver
}

// CodexCanonicalUserAgent returns the validated configured or compiled Codex
// User-Agent.
func CodexCanonicalUserAgent() string {
	return ResolveCodexOutboundIdentity("").UserAgent
}

// CodexCanonicalAuthIdentity returns the canonical User-Agent and its paired
// originator.
func CodexCanonicalAuthIdentity() (userAgent, originator string) {
	identity := ResolveCodexOutboundIdentity("")
	return identity.UserAgent, identity.Originator
}

// ApplyCodexCanonicalAuthIdentity writes the canonical User-Agent and
// originator into h. Nil headers are ignored, and any existing version header
// stays as supplied because credential requests do not require this helper to
// generate one.
func ApplyCodexCanonicalAuthIdentity(h http.Header) {
	if h == nil {
		return
	}
	userAgent, originator := CodexCanonicalAuthIdentity()
	h.Set("user-agent", userAgent)
	h.Set("originator", originator)
}

// CodexCanonicalClientVersion returns the acceptable version derived from
// canonical settings.
func CodexCanonicalClientVersion() string {
	return ResolveCodexOutboundIdentity("").Version
}

// ConfiguredCodexUserAgent reads and trims the settings callback result outside
// the resolver lock. A nil callback or blank result returns the compiled
// User-Agent; identity validation is performed by its callers.
func ConfiguredCodexUserAgent() string {
	codexCanonicalUAMu.RLock()
	resolver := codexCanonicalUAResolver
	codexCanonicalUAMu.RUnlock()
	// Invoke settings outside the resolver lock so a slow read does not prevent
	// another caller from installing or clearing the canonical identity callback.
	if resolver != nil {
		if value := strings.TrimSpace(resolver()); value != "" {
			return value
		}
	}
	return CodexCLIUserAgent
}

// NormalizeCodexClientVersion returns a trimmed ASCII version accepted for
// outbound headers. Empty values, values exceeding the byte limit, and strings
// outside the numeric-component/suffix pattern return empty text.
func NormalizeCodexClientVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" || len(version) > CodexClientVersionMaxLen || !codexClientVersionPattern.MatchString(version) {
		return ""
	}
	return version
}

// ResolveCodexOutboundIdentity selects a paired Codex User-Agent, originator,
// and canonical version. A blank candidate starts with the configured canonical
// User-Agent; an unrecognized candidate retries that canonical identity, then
// selects the compiled identity if pairing still fails. The returned version is
// derived from canonical settings.
//
// Pairing can restore a recognized trailer name when an override changed the
// leading name. Replacing the leading version preserves the paired suffix and
// platform data while keeping the version header consistent with the canonical
// selection. The returned identity is a value snapshot; a later settings change
// affects later resolutions.
func ResolveCodexOutboundIdentity(candidateUA string) CodexOutboundIdentity {
	canonical := ConfiguredCodexUserAgent()
	ua := strings.TrimSpace(candidateUA)
	if ua == "" {
		ua = canonical
	}
	originator, pairedUA, ok := clientmeta.PairCodexClientIdentity(ua)
	// Candidate identity can come from a client override. Retry the canonical
	// source before using compiled values so invalid overrides retain usable authentication.
	if !ok {
		originator, pairedUA, ok = clientmeta.PairCodexClientIdentity(canonical)
	}
	// Invalid canonical settings still need a complete recognized pair; select
	// compiled values together so originator cannot describe a different outgoing identity.
	if !ok {
		originator, pairedUA = clientmeta.CodexDefaultOriginator, CodexCLIUserAgent
	}
	version := CodexClientVersionFromUA(canonical)
	// Pairing already fixed the leading name. Rewrite only its version segment
	// so the suffix retains platform details while matching the canonical version header.
	if rebuilt := ReplaceCodexUserAgentVersion(pairedUA, version); rebuilt != "" {
		pairedUA = rebuilt
	}
	return CodexOutboundIdentity{UserAgent: pairedUA, Originator: originator, Version: version}
}

// CodexClientVersionFromUA extracts an acceptable engine version from the
// supplied User-Agent. Missing, malformed, or below-minimum versions select
// CodexCLIVersion, allowing callers to construct a version header from
// incomplete identity text.
func CodexClientVersionFromUA(userAgent string) string {
	version, ok := clientmeta.ParseCodexEngineVersion(userAgent)
	// A User-Agent with no parseable release still needs an accepted version
	// header; the compiled release supplies the value used by canonical callers.
	if !ok {
		return CodexCLIVersion
	}
	version = NormalizeCodexClientVersion(version)
	// Reject a malformed or below-floor release for header selection, since the
	// separate version header controls whether the Codex endpoint accepts this identity.
	if version == "" || clientmeta.CompareVersions(version, CodexUpstreamMinVersion) < 0 {
		return CodexCLIVersion
	}
	return version
}

// ReplaceCodexUserAgentVersion replaces the version after the first slash and
// retains the remaining suffix. An invalid requested version or missing leading
// name/version position returns empty text for the caller to handle.
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

// EnsureCodexIdentityHeaders fills blank User-Agent, originator, and version
// values with canonical identity data. It writes the Responses experimental
// beta value and ignores nil headers; enforce pairing after any User-Agent
// overrides.
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

// ApplyOpenAICodexProbeHeaders fills Codex probe identity and assigns a fresh
// window ID. Nil headers are ignored; existing usable identity fields follow
// EnsureCodexIdentityHeaders, while the window identifier is replaced for each
// probe.
func ApplyOpenAICodexProbeHeaders(h http.Header) {
	if h == nil {
		return
	}
	EnsureCodexIdentityHeaders(h)
	h.Set("X-Codex-Window-ID", uuid.NewString())
}

// EnforceCodexIdentityHeaders delegates final identity pairing using the header
// User-Agent. Call it after overrides; a missing originator skips enforcement,
// so use EnsureCodexIdentityHeaders first when absent fields should receive
// defaults.
func EnforceCodexIdentityHeaders(h http.Header) {
	EnforceCodexIdentityHeadersWithUA(h, "")
}

// EnforceCodexIdentityHeadersWithUA pairs the outgoing User-Agent and
// originator in h. A nonblank overrideUA takes precedence over the header
// value, and nil headers or a missing originator skip the operation.
// Unrecognized candidates receive the canonical identity.
//
// The paired User-Agent keeps its supplied version, while a nonblank version
// header below CodexUpstreamMinVersion is replaced independently by the
// canonical version. This separation lets callers retain a recognizable client
// identity while correcting the separate header used by Codex routing. Run
// enforcement after User-Agent selection so the two identity headers describe
// the final request.
func EnforceCodexIdentityHeadersWithUA(h http.Header, overrideUA string) {
	if h == nil || h.Get("originator") == "" {
		return
	}
	candidateUA := strings.TrimSpace(overrideUA)
	if candidateUA == "" {
		candidateUA = h.Get("user-agent")
	}
	originator, pairedUA, ok := clientmeta.PairCodexClientIdentity(candidateUA)
	// Repair an unrecognized candidate with a canonical pair before publishing
	// either header, keeping the outgoing originator consistent with the selected User-Agent.
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

// ResolveUpstreamOriginator selects a trimmed routed value when matched, then
// the inbound value read by the required callback. Missing values use the
// canonical official originator for official requests and opencode for
// compatible requests.
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

// IsBrowserUserAgent reports whether clientmeta recognizes browser markers in
// userAgent.
func IsBrowserUserAgent(userAgent string) bool { return clientmeta.IsBrowserUserAgent(userAgent) }

// IsCodexOfficialClientRequest recognizes official Codex format through the
// compatibility User-Agent matcher. Embedded known prefixes, a leading Codex
// family name, or a recognized final trailer can qualify after trimming and
// case normalization.
func IsCodexOfficialClientRequest(userAgent string) bool {
	return clientmeta.IsCodexOfficialClientRequest(userAgent)
}

// IsCodexOfficialClientRequestStrict recognizes leading Codex prefixes, family
// names, or final trailer names. Embedded compatibility markers alone do not
// qualify; the check identifies a client format rather than authenticating the
// caller.
func IsCodexOfficialClientRequestStrict(userAgent string) bool {
	return clientmeta.IsCodexOfficialClientRequestStrict(userAgent)
}

// IsCodexOfficialClientOriginator recognizes exact known originators or the
// Codex space-prefixed family. Whitespace is trimmed and case is ignored, while
// unrelated names containing a known identifier remain unmatched.
func IsCodexOfficialClientOriginator(originator string) bool {
	return clientmeta.IsCodexOfficialClientOriginator(originator)
}

// IsCodexOfficialClientByHeaders reports whether either header identifies an
// official Codex format. User-Agent uses compatibility matching, and a
// recognized originator can qualify independently even when the User-Agent is
// missing.
func IsCodexOfficialClientByHeaders(userAgent, originator string) bool {
	return clientmeta.IsCodexOfficialClientByHeaders(userAgent, originator)
}

// PairCodexClientIdentity delegates originator/User-Agent pairing to
// clientmeta. Recognized leading names precede trailer recovery; unusable
// inputs return empty values and false so callers can select their canonical
// default identity.
func PairCodexClientIdentity(userAgent string) (originator string, pairedUA string, ok bool) {
	return clientmeta.PairCodexClientIdentity(userAgent)
}

// normalizeCodexClientHeader trims whitespace and lowercases one client header
// value.
func normalizeCodexClientHeader(value string) string {
	return clientmeta.NormalizeCodexClientHeader(value)
}
