package egress

import (
	"net/url"
	"strings"
)

// Ollama cloud identity is tied to an HTTPS authority and an exact supported
// base path. Provider capability and usage selection use this check to exclude
// credentials, alternate ports, escaped paths, queries, and fragments from the
// recognized cloud endpoint.

// IsOllamaCloudBaseURL reports whether trimmed input names ollama.com or
// www.ollama.com over HTTPS with no port or port 443. Accepted paths are empty
// or /v1; escaped paths, credentials, queries, fragments, and trailing path
// slashes fail.
func IsOllamaCloudBaseURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "?#") {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || !strings.EqualFold(parsed.Scheme, "https") || parsed.User != nil || parsed.ForceQuery || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawFragment != "" {
		return false
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname != "ollama.com" && hostname != "www.ollama.com" {
		return false
	}
	authority := strings.ToLower(parsed.Host)
	if authority != hostname && authority != hostname+":443" {
		return false
	}
	if parsed.RawPath != "" {
		return false
	}
	return parsed.Path == "" || parsed.Path == "/v1"
}
