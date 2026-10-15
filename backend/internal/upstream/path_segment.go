package upstream

import (
	"fmt"
	"strings"
)

// path_segment.go validates upstream URL path segments and Responses suffixes
// against bounded ASCII path rules.
// Client-controlled Responses suffixes and Gemini model names become upstream paths.
// An ASCII allowlist prevents those values from changing the URL structure.
// URL.Path is already percent-decoded; validate it before constructing an upstream URL.
// Reject invalid inputs rather than rewriting them to a different request target.

// These bounds limit the size and nesting depth of client-derived URL suffixes
// before forwarding constructs an upstream endpoint.
const (
	// MaxUpstreamPathSegmentLen bounds one segment while allowing normal response IDs
	// and model names.
	MaxUpstreamPathSegmentLen = 128
	// MaxUpstreamPathSegments bounds suffix depth; /{id}/cancel uses two segments.
	MaxUpstreamPathSegments = 8
)

// IsSafeUpstreamPathSegmentByte reports whether b belongs to the URL segment
// allowlist. Accepted bytes are ASCII letters, digits, underscore, hyphen, and
// dot; callers check whole-segment dot rules separately.
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

// IsSafeUpstreamPathSegment reports whether a bounded, nonempty segment passes
// the ASCII allowlist. It rejects every dot-only spelling because downstream
// path normalization may interpret those spellings as navigation components.
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
	// A sequence made entirely of dots can be interpreted differently by URL
	// and proxy normalization; reject it before any caller appends the segment.
	return !dotsOnly
}

// SanitizedUpstreamPathSuffix validates a trimmed suffix shaped like /a/b and
// returns its spelling. Empty input means no subpath; callers reject ok=false
// because substituting empty would select a different endpoint.
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

// ValidateUpstreamPathSegment validates the trimmed segment before upstream
// URL construction. It returns an error containing the supplied kind when
// invalid; callers retain responsibility for using the validated trimmed
// spelling.
func ValidateUpstreamPathSegment(kind, segment string) error {
	if IsSafeUpstreamPathSegment(strings.TrimSpace(segment)) {
		return nil
	}
	// Error text can reach logs or client responses. Report the caller-supplied
	// field kind while keeping the rejected segment out of that diagnostic text.
	return fmt.Errorf("invalid %s for upstream url path", kind)
}
