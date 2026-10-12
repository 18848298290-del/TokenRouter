package usage

import (
	"strconv"
	"strings"
)

const (
	UsageRankingSortByTotalTokens UsageRankingSortBy = "total_tokens"
	UsageRankingSortByRequests    UsageRankingSortBy = "requests"
	UsageRankingSortByActualCost  UsageRankingSortBy = "actual_cost"

	DefaultUsageRankingLimit = 20
	MaxUsageRankingLimit     = 100
)

// UsageRankingSortBy selects the metric used to rank users.
type UsageRankingSortBy string

// UsageRankingSettings configures public ranking queries and visible metrics.
type UsageRankingSettings struct {
	Enabled         bool
	SortBy          UsageRankingSortBy
	ShowTotalTokens bool
	ShowRequests    bool
	ShowActualCost  bool
	Limit           int
}

func IsValidUsageRankingSortBy(value string) bool {
	switch UsageRankingSortBy(strings.TrimSpace(value)) {
	case UsageRankingSortByTotalTokens, UsageRankingSortByRequests, UsageRankingSortByActualCost:
		return true
	default:
		return false
	}
}

func NormalizeUsageRankingSortByInternal(value string) UsageRankingSortBy {
	if IsValidUsageRankingSortBy(value) {
		return UsageRankingSortBy(strings.TrimSpace(value))
	}
	return UsageRankingSortByTotalTokens
}

// NormalizeUsageRankingSortBy defaults unrecognized values to total tokens.
func NormalizeUsageRankingSortBy(value string) UsageRankingSortBy {
	return NormalizeUsageRankingSortByInternal(value)
}

// NormalizeUsageRankingSettings makes the sorting metric visible so users can interpret the order.
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

func NormalizeUsageRankingLimit(value int) int {
	if value <= 0 {
		return DefaultUsageRankingLimit
	}
	if value > MaxUsageRankingLimit {
		return MaxUsageRankingLimit
	}
	return value
}

func NormalizeUsageRankingLimitString(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return DefaultUsageRankingLimit
	}
	return NormalizeUsageRankingLimit(value)
}
