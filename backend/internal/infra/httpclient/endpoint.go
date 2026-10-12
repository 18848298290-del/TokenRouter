package httpclient

import (
	"net/url"
	"strings"
)

// BuildOpenAIEndpointURL appends an endpoint to an OpenAI-compatible base URL.
// An existing endpoint suffix is reused; a versioned base such as /v1 or /v4 gets
// only the relative endpoint. Parsed URLs retain queries and discard fragments.
func BuildOpenAIEndpointURL(base string, endpoint string) string {
	normalized := strings.TrimSpace(base)
	endpoint = "/" + strings.TrimLeft(strings.TrimSpace(endpoint), "/")
	relative := strings.TrimPrefix(endpoint, "/v1")
	parsed, err := url.Parse(normalized)
	if err != nil {
		return strings.TrimRight(normalized, "/") + endpoint
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, endpoint) && !strings.HasSuffix(path, relative) {
		if OpenAIBaseURLHasVersionSuffix(path) {
			path += relative
		} else {
			path += endpoint
		}
	}
	parsed.Path = path
	parsed.RawPath = ""
	parsed.Fragment = ""
	return parsed.String()
}

func BuildOpenAIResponsesInputTokensURL(base string) string {
	normalized := strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(normalized, "/responses") {
		return normalized + "/input_tokens"
	}
	return BuildOpenAIEndpointURL(base, "/v1/responses/input_tokens")
}

// OpenAIBaseURLHasVersionSuffix reports whether the final path segment is an API version.
func OpenAIBaseURLHasVersionSuffix(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}

	pathValue := ""
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		pathValue = parsed.Path
	} else if slash := strings.Index(trimmed, "/"); slash >= 0 {
		pathValue = trimmed[slash:]
	}

	pathValue = strings.TrimRight(pathValue, "/")
	if pathValue == "" {
		return false
	}
	lastSlash := strings.LastIndex(pathValue, "/")
	segment := pathValue
	if lastSlash >= 0 {
		segment = pathValue[lastSlash+1:]
	}
	return IsOpenAIAPIVersionSegment(segment)
}

// IsOpenAIAPIVersionSegment recognizes versions such as v1, v4, v1.1, v1beta, and v1preview.
func IsOpenAIAPIVersionSegment(segment string) bool {
	s := strings.ToLower(strings.TrimSpace(segment))
	if len(s) < 2 || s[0] != 'v' || !IsASCIIDigit(s[1]) {
		return false
	}

	i := 1
	for i < len(s) && IsASCIIDigit(s[i]) {
		i++
	}
	if i == len(s) {
		return true
	}
	if s[i] == '.' {
		i++
		if i == len(s) || !IsASCIIDigit(s[i]) {
			return false
		}
		for i < len(s) && IsASCIIDigit(s[i]) {
			i++
		}
		return i == len(s)
	}

	suffix := s[i:]
	return strings.HasPrefix(suffix, "alpha") ||
		strings.HasPrefix(suffix, "beta") ||
		strings.HasPrefix(suffix, "preview")
}

func IsASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}
