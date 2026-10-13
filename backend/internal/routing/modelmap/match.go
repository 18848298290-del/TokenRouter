package modelmap

import (
	"sort"
	"strings"
)

// Model rewrites apply one selected mapping so an alias does not recursively
// follow its target through the same table. Resolve gives an exact key
// precedence, while MatchWildcard chooses overlapping matches by longest byte
// length with lexical order breaking ties.
//
// Wildcard matching recognizes a final star as a prefix rule and treats other
// text literally. Sorting the matching candidates makes selection independent
// of Go map traversal, allowing the same saved rules to choose the same
// upstream name on every request. Unmatched input remains available with a
// false match flag for callers that retain the requested model.

// Resolve applies one model mapping, preferring a literal key before matching
// patterns. Empty requests return empty and false; unmatched nonempty requests
// retain their supplied name, and targets are not mapped again.
func Resolve(mapping map[string]string, requestedModel string) (mappedModel string, matched bool) {
	if requestedModel == "" {
		return "", false
	}
	if mappedModel, exists := mapping[requestedModel]; exists {
		return mappedModel, true
	}
	return MatchWildcard(mapping, requestedModel)
}

// MatchWildcard chooses the longest matching pattern and returns its configured
// target with true. Exact strings are also eligible because Matches handles
// them alongside trailing-star prefixes.
//
// Matches are gathered from a map, so sorting by byte length and then lexical
// order removes map-iteration dependence. A trailing star can match an empty
// suffix, and the star alone matches any name. When no pattern matches, the
// request name is returned unchanged with false.
func MatchWildcard(mapping map[string]string, requestedModel string) (string, bool) {
	// patternMatch pairs a matching source pattern with its configured target.
	type patternMatch struct {
		pattern string // matching mapping source pattern
		target  string // configured one-step model target
	}
	var matches []patternMatch

	for pattern, target := range mapping {
		if Matches(pattern, requestedModel) {
			matches = append(matches, patternMatch{pattern, target})
		}
	}

	if len(matches) == 0 {
		return requestedModel, false
	}

	// Map traversal has no precedence, so sort matching keys by byte length and
	// lexical order before selecting a target for overlapping configured rules.
	sort.Slice(matches, func(i, j int) bool {
		if len(matches[i].pattern) != len(matches[j].pattern) {
			return len(matches[i].pattern) > len(matches[j].pattern)
		}
		return matches[i].pattern < matches[j].pattern
	})

	return matches[0].target, true
}

// Matches reports whether str equals a literal pattern or has its trailing-star
// prefix. Matching is case-sensitive, internal stars stay literal, and a star
// alone accepts any string.
func Matches(pattern, str string) bool {
	if strings.HasSuffix(pattern, "*") {
		prefix := pattern[:len(pattern)-1]
		return strings.HasPrefix(str, prefix)
	}
	return pattern == str
}
