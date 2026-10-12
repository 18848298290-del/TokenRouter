package upstream

import (
	"fmt"
	"strings"
)

// Client-controlled Responses suffixes and Gemini model names become upstream paths.
// An ASCII allowlist prevents those values from changing the URL structure.
// URL.Path is already percent-decoded; validate it before constructing an upstream URL.
// Reject invalid inputs rather than rewriting them to a different request target.

const (
	// MaxUpstreamPathSegmentLen bounds one segment while allowing normal response IDs
	// and model names.
	MaxUpstreamPathSegmentLen = 128
	// MaxUpstreamPathSegments bounds suffix depth; /{id}/cancel uses two segments.
	MaxUpstreamPathSegments = 8
)

// IsSafeUpstreamPathSegmentByte accepts ASCII letters, digits, underscore, hyphen,
// and dot. Control bytes and non-ASCII input are rejected.
func IsSafeUpstreamPathSegmentByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_', b == '-', b == '.':
		return true
	default:
		return false
	}
}

// IsSafeUpstreamPathSegment reports whether segment can be appended as one URL path segment.
//
// Dot-only segments are rejected because path normalization can interpret them differently.
func IsSafeUpstreamPathSegment(segment string) bool {
	if segment == "" || len(segment) > MaxUpstreamPathSegmentLen {
		return false
	}
	dotsOnly := true
	for i := range len(segment) {
		if !IsSafeUpstreamPathSegmentByte(segment[i]) {
			return false
		}
		if segment[i] != '.' {
			dotsOnly = false
		}
	}
	return !dotsOnly
}

// SanitizedUpstreamPathSuffix validates a suffix shaped like /a/b.
// Callers must reject ok=false; replacing it with an empty suffix changes the endpoint.
// An empty suffix is valid and means no subpath.
func SanitizedUpstreamPathSuffix(raw string) (string, bool) {
	suffix := strings.TrimSpace(raw)
	if suffix == "" {
		return "", true
	}
	if !strings.HasPrefix(suffix, "/") {
		return "", false
	}
	segments := strings.Split(strings.TrimPrefix(suffix, "/"), "/")
	if len(segments) > MaxUpstreamPathSegments {
		return "", false
	}
	for _, segment := range segments {
		if !IsSafeUpstreamPathSegment(segment) {
			return "", false
		}
	}
	return suffix, true
}

// ValidateUpstreamPathSegment returns an error for an unsafe trimmed path segment.
// Callers must stop URL construction and reject the request on error.
func ValidateUpstreamPathSegment(kind, segment string) error {
	if IsSafeUpstreamPathSegment(strings.TrimSpace(segment)) {
		return nil
	}
	// Omit the raw input to keep it out of logs and error responses.
	return fmt.Errorf("invalid %s for upstream url path", kind)
}
