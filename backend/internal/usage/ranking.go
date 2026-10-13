package usage

import (
	"strconv"
	"strings"
)

// Public rankings order users by one configured metric while allowing
// administrators to hide other counters. A hidden ordering metric would leave
// users unable to interpret the rank, so normalized settings enable the
// selected metric's visibility flag. Invalid sort text selects total tokens,
// and nonpositive or oversized row limits pass through the configured default
// and cap.
//
// The parsers and settings writers use this normalization before persistence or
// queries. Each visibility switch still controls its own response fields;
// selecting a sorting metric only forces that corresponding switch to true.
// Input text validation is separate from normalization, letting update callers
// reject unsupported sort fields while runtime reads use a default.

// Ranking labels select fixed query ordering rules, while the row limits bound
// normalized settings used by public ranking requests.
const (
	UsageRankingSortByTotalTokens UsageRankingSortBy = "total_tokens" // combined token count ordering
	UsageRankingSortByRequests    UsageRankingSortBy = "requests"     // recorded request count ordering
	UsageRankingSortByActualCost  UsageRankingSortBy = "actual_cost"  // recorded user charge ordering

	DefaultUsageRankingLimit = 20  // rows for absent or nonpositive limits
	MaxUsageRankingLimit     = 100 // maximum normalized public ranking rows
)

// UsageRankingSortBy names the selected ordering metric for public rankings.
type UsageRankingSortBy string

// UsageRankingSettings selects ranking availability, ordering, row limits, and
// displayed counters.
type UsageRankingSettings struct {
	Enabled bool // ranking availability setting flag
	// SortBy selects which of ShowTotalTokens, ShowRequests, and ShowActualCost
	// normalization enables. The remaining visibility flags retain their supplied
	// values, and callers normalize these settings before constructing responses.
	SortBy          UsageRankingSortBy // selected ranking ordering metric
	ShowTotalTokens bool               // token totals visibility flag
	ShowRequests    bool               // request totals visibility flag
	ShowActualCost  bool               // user charge visibility flag
	Limit           int                // normalized ranking row limit
}

// IsValidUsageRankingSortBy reports whether trimmed text identifies one of the
// supported ranking metrics.
func IsValidUsageRankingSortBy(value string) bool {
	switch UsageRankingSortBy(strings.TrimSpace(value)) {
	case UsageRankingSortByTotalTokens, UsageRankingSortByRequests, UsageRankingSortByActualCost:
		return true
	default:
		return false
	}
}

// NormalizeUsageRankingSortByInternal returns a supported metric from
// whitespace-trimmed stored text. Matching is case sensitive; empty or
// unrecognized values select total tokens. Callers requiring rejection check
// IsValidUsageRankingSortBy before normalization.
func NormalizeUsageRankingSortByInternal(value string) UsageRankingSortBy {
	if IsValidUsageRankingSortBy(value) {
		return UsageRankingSortBy(strings.TrimSpace(value))
	}
	return UsageRankingSortByTotalTokens
}

// NormalizeUsageRankingSortBy selects the supported ranking metric from
// configured text. It delegates trimming and default selection to
// NormalizeUsageRankingSortByInternal, with total tokens used for an
// unrecognized value.
func NormalizeUsageRankingSortBy(value string) UsageRankingSortBy {
	return NormalizeUsageRankingSortByInternal(value)
}

// NormalizeUsageRankingSettings returns settings with a recognized ordering
// metric, a bounded row limit, and a visible ordering counter. It first
// normalizes the metric and limit, then enables the visibility switch
// corresponding to the selected metric.
//
// The remaining counter switches retain their supplied values. Runtime parsing
// and administrator writes share this rule so the user-visible ranking exposes
// the quantity responsible for its order even when that switch was supplied as
// false.
func NormalizeUsageRankingSettings(settings UsageRankingSettings) UsageRankingSettings {
	settings.SortBy = NormalizeUsageRankingSortByInternal(string(settings.SortBy))
	settings.Limit = NormalizeUsageRankingLimit(settings.Limit)
	switch settings.SortBy {
	case UsageRankingSortByRequests:
		settings.ShowRequests = true
	case UsageRankingSortByActualCost:
		settings.ShowActualCost = true
	default:
		settings.ShowTotalTokens = true
	}
	return settings
}

// NormalizeUsageRankingLimit bounds the number of rows returned by public
// rankings. Nonpositive limits use twenty rows, and limits above one hundred
// are capped at that maximum.
func NormalizeUsageRankingLimit(value int) int {
	if value <= 0 {
		return DefaultUsageRankingLimit
	}
	// Bound the public setting before it becomes a repository row limit.
	if value > MaxUsageRankingLimit {
		return MaxUsageRankingLimit
	}
	return value
}

// NormalizeUsageRankingLimitString parses a whitespace-trimmed decimal row
// limit. Invalid text and integer overflow select twenty rows; signed values
// otherwise use NormalizeUsageRankingLimit for its nonpositive default and
// upper cap.
func NormalizeUsageRankingLimitString(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		// Malformed stored text still permits ranking reads, using the default row
		// count before any query is constructed from the normalized settings.
		return DefaultUsageRankingLimit
	}
	return NormalizeUsageRankingLimit(value)
}
