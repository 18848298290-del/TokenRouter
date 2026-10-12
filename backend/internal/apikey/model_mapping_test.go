package apikey

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNormalizeAPIKeyModelMapping supplies a padded exact rule alongside an
// unpadded wildcard rule. It checks the trimmed exact source and target while
// retaining the accepted wildcard source and its target.
func TestNormalizeAPIKeyModelMapping(t *testing.T) {
	normalized, err := NormalizeAPIKeyModelMapping(map[string]string{
		" codex-auto-review ": " gpt-5.6-luna ",
		"claude-*":            "claude-sonnet-4-6",
	})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"codex-auto-review": "gpt-5.6-luna",
		"claude-*":          "claude-sonnet-4-6",
	}, normalized)
}

// TestNormalizeAPIKeyModelMappingRejectsInvalidRules supplies empty,
// self-targeting, oversized, duplicate, and malformed wildcard rules. It checks
// the invalid mapping error identity for each shape and for a rule count above
// the configured maximum.
func TestNormalizeAPIKeyModelMappingRejectsInvalidRules(t *testing.T) {
	tests := map[string]map[string]string{
		"空来源":    {" ": "target"},
		"空目标":    {"source": " "},
		"自身映射":   {"source": "source"},
		"来源中间通配": {"source*-x": "target"},
		"来源多个通配": {"source**": "target"},
		"目标通配":   {"source": "target*"},
		"去空格后重复": {"source": "one", " source ": "two"},
		"来源过长":   {strings.Repeat("源", MaxAPIKeyModelNameRunes+1): "target"},
		"目标过长":   {"source": strings.Repeat("目", MaxAPIKeyModelNameRunes+1)},
	}
	for name, mapping := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeAPIKeyModelMapping(mapping)
			require.Error(t, err)
			require.True(t, errors.Is(err, ErrInvalidAPIKeyModelMapping))
		})
	}

	tooMany := make(map[string]string, MaxAPIKeyModelMappingRules+1)
	for i := 0; i <= MaxAPIKeyModelMappingRules; i++ {
		tooMany[fmt.Sprintf("source-%d", i)] = fmt.Sprintf("target-%d", i)
	}
	_, err := NormalizeAPIKeyModelMapping(tooMany)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidAPIKeyModelMapping))
}

// TestResolveAPIKeyModelMappingPriorityAndSinglePass combines exact,
// overlapping wildcard, chained-target, and differently cased source rules. It
// checks exact priority, longest-prefix selection, case-sensitive matching, and
// return of a target after one pass.
func TestResolveAPIKeyModelMappingPriorityAndSinglePass(t *testing.T) {
	mapping := map[string]string{
		"codex-*":           "wildcard-short",
		"codex-auto-*":      "wildcard-long",
		"codex-auto-review": "gpt-5.6-luna",
		"gpt-5.6-luna":      "must-not-chain",
		"Codex-*":           "case-sensitive",
	}

	model, matched := ResolveModelMapping(mapping, "codex-auto-review")
	require.True(t, matched)
	require.Equal(t, "gpt-5.6-luna", model)

	model, matched = ResolveModelMapping(mapping, "codex-auto-fix")
	require.True(t, matched)
	require.Equal(t, "wildcard-long", model)

	model, matched = ResolveModelMapping(mapping, "Codex-review")
	require.True(t, matched)
	require.Equal(t, "case-sensitive", model)

	model, matched = ResolveModelMapping(mapping, "CODEX-review")
	require.False(t, matched)
	require.Equal(t, "CODEX-review", model)
}

// TestAppendAPIKeyModelAliases supplies repeated models and unsorted aliases
// with available and absent targets plus a wildcard source. It checks model deduplication,
// retained model order, and lexical ordering of the eligible exact aliases.
func TestAppendAPIKeyModelAliases(t *testing.T) {
	models := AppendAPIKeyModelAliases(
		[]string{"gpt-5.6-luna", "claude-sonnet-4-6", "gpt-5.6-luna"},
		map[string]string{
			"z-review": "gpt-5.6-luna",
			"a-review": "gpt-5.6-luna",
			"missing":  "not-requestable",
			"wild-*":   "gpt-5.6-luna",
		},
	)
	require.Equal(t, []string{
		"gpt-5.6-luna",
		"claude-sonnet-4-6",
		"a-review",
		"z-review",
	}, models)
}
