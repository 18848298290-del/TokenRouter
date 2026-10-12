package grok

import (
	"net/http"
	"os"
	"strings"

	"golang.org/x/mod/semver"
)

// These constants identify Grok Build requests to the CLI chat proxy.
// Versions are bundled with the binary; XAI_GROK_CLI_VERSION can override the default.
const (
	// CLIProxyHost requires the Grok CLI identity headers.
	CLIProxyHost = "cli-chat-proxy.grok.com"

	// CLIStableVersion is the minimum accepted CLI proxy client version.
	CLIStableVersion = "0.2.93"

	// CLIVersionEnv names the optional client-version override.
	CLIVersionEnv = "XAI_GROK_CLI_VERSION"

	// CLITokenAuth identifies Grok Build OAuth tokens to the CLI proxy.
	CLITokenAuth = "xai-grok-cli"

	// CLIClientIdentifier is the x-grok-client-identifier value for Grok CLI requests.
	CLIClientIdentifier = "grok-shell"
)

// ResolveCLIVersion returns a supported CLI version from the environment.
// An empty or invalid override uses CLIClientVersion.
// CLIStableVersion sets the lower bound for accepted overrides.
func ResolveCLIVersion() string {
	version := strings.TrimSpace(os.Getenv(CLIVersionEnv))
	if !IsSupportedCLIVersion(version) {
		return CLIClientVersion
	}
	return version
}

// IsSupportedCLIVersion accepts canonical SemVer at or above CLIStableVersion.
func IsSupportedCLIVersion(version string) bool {
	canonical := "v" + version
	minimum := "v" + CLIStableVersion
	return semver.IsValid(canonical) &&
		semver.Canonical(canonical) == canonical &&
		semver.Compare(canonical, minimum) >= 0
}

// CLIUserAgent builds a workspace User-Agent, defaulting to CLIClientVersion.
func CLIUserAgent(version string) string {
	if strings.TrimSpace(version) == "" {
		version = CLIClientVersion
	}
	return "xai-grok-workspace/" + version
}

// ApplyCLIProxyHeaders sets Grok CLI identity headers for CLIProxyHost.
// Requests to other hosts are left unchanged.
func ApplyCLIProxyHeaders(req *http.Request) {
	if req == nil || req.URL == nil || !strings.EqualFold(strings.TrimSpace(req.URL.Hostname()), CLIProxyHost) {
		return
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	version := ResolveCLIVersion()
	req.Header.Set("X-XAI-Token-Auth", CLITokenAuth)
	req.Header.Set("x-grok-client-version", version)
	req.Header.Set("x-grok-client-identifier", CLIClientIdentifier)
	req.Header.Set("User-Agent", CLIUserAgent(version))
}

// DefaultGrokUpstreamUserAgent returns the configured Grok workspace User-Agent.
// Forwarding uses this identity in place of the incoming User-Agent.
func DefaultGrokUpstreamUserAgent() string {
	return CLIUserAgent(ResolveCLIVersion())
}

// ApplyDefaultGrokUpstreamHeaders sets the Grok CLI request identity.
func ApplyDefaultGrokUpstreamHeaders(req *http.Request) {
	if req == nil {
		return
	}
	// xAI conversation and CLI endpoints use these headers to identify the client.
	req.Header.Set("User-Agent", DefaultGrokUpstreamUserAgent())
	req.Header.Set("x-grok-client-version", ResolveCLIVersion())
	req.Header.Set("x-grok-client-identifier", CLIClientIdentifier)
}

// ApplyGrokRuntimeHeaders sets the runtime Originator and Grok CLI identity.
func ApplyGrokRuntimeHeaders(req *http.Request, runtimeOriginator string) {
	ApplyDefaultGrokUpstreamHeaders(req)
	if req == nil {
		return
	}
	// A nonempty runtime setting supplies the Originator header.
	if originator := strings.TrimSpace(runtimeOriginator); originator != "" {
		req.Header.Set("Originator", originator)
	}
	req.Header.Set("User-Agent", DefaultGrokUpstreamUserAgent())
}
