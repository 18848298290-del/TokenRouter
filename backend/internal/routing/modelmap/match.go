package modelmap

import (
	"sort"
	"strings"
)

func Resolve(mapping map[string]string, requestedModel string) (mappedModel string, matched bool) {
	if requestedModel == "" {
		return "", false
	}
	if mappedModel, exists := mapping[requestedModel]; exists {
		return mappedModel, true
	}
	return MatchWildcard(mapping, requestedModel)
}

func MatchWildcard(mapping map[string]string, requestedModel string) (string, bool) {
	type patternMatch struct {
		pattern string
		target  string
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

	// Choose the most specific matching pattern, with lexical order breaking equal-length ties.
	sort.Slice(matches, func(i, j int) bool {
		if len(matches[i].pattern) != len(matches[j].pattern) {
			return len(matches[i].pattern) > len(matches[j].pattern)
		}
		return matches[i].pattern < matches[j].pattern
	})

	return matches[0].target, true
}

// Matches accepts an exact name or a pattern ending in *.
func Matches(pattern, str string) bool {
	if strings.HasSuffix(pattern, "*") {
		prefix := pattern[:len(pattern)-1]
		return strings.HasPrefix(str, prefix)
	}
	return pattern == str
}
