package usage

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// RankingSettingsUpdate uses pointers to distinguish omitted boolean and sort fields.
type RankingSettingsUpdate struct {
	Limit           int
	Enabled         *bool
	SortBy          *string
	ShowTotalTokens *bool
	ShowRequests    *bool
	ShowActualCost  *bool
}

// rankingSortError identifies an invalid ranking field and provides its HTTP error text.
type rankingSortError struct{}

// ResolveRankingSettings applies supplied fields, keeps omitted values, and makes the sorting metric visible.
func ResolveRankingSettings(current UsageRankingSettings, input RankingSettingsUpdate) (UsageRankingSettings, error) {
	if input.Limit > 0 {
		current.Limit = input.Limit
	}
	if current.Limit > MaxUsageRankingLimit {
		current.Limit = MaxUsageRankingLimit
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	if input.SortBy != nil {
		if !IsValidUsageRankingSortBy(*input.SortBy) {
			return UsageRankingSettings{}, rankingSortError{}
		}
		current.SortBy = UsageRankingSortBy(*input.SortBy)
	}
	if input.ShowTotalTokens != nil {
		current.ShowTotalTokens = *input.ShowTotalTokens
	}
	if input.ShowRequests != nil {
		current.ShowRequests = *input.ShowRequests
	}
	if input.ShowActualCost != nil {
		current.ShowActualCost = *input.ShowActualCost
	}
	return NormalizeUsageRankingSettings(current), nil
}

// PrepareRankingSettings normalizes settings and encodes their persisted string values.
func PrepareRankingSettings(value UsageRankingSettings) (UsageRankingSettings, map[string]string) {
	value = NormalizeUsageRankingSettings(value)
	return value, map[string]string{
		SettingKeyUsageRankingLimit:           strconv.Itoa(value.Limit),
		SettingKeyUsageRankingEnabled:         strconv.FormatBool(value.Enabled),
		SettingKeyUsageRankingSortBy:          string(value.SortBy),
		SettingKeyUsageRankingShowTotalTokens: strconv.FormatBool(value.ShowTotalTokens),
		SettingKeyUsageRankingShowRequests:    strconv.FormatBool(value.ShowRequests),
		SettingKeyUsageRankingShowActualCost:  strconv.FormatBool(value.ShowActualCost),
	}
}

// SettingsParticipant prepares ranking and user error-display settings.
func SettingsParticipant() settings.Participant {
	fields := []string{"usage_ranking_limit", "usage_ranking_enabled", "usage_ranking_sort_by", "usage_ranking_show_total_tokens", "usage_ranking_show_requests", "usage_ranking_show_actual_cost", "allow_user_view_error_requests"}
	keys := []string{SettingKeyUsageRankingLimit, SettingKeyUsageRankingEnabled, SettingKeyUsageRankingSortBy, SettingKeyUsageRankingShowTotalTokens, SettingKeyUsageRankingShowRequests, SettingKeyUsageRankingShowActualCost, SettingKeyAllowUserViewErrorRequests}
	return settings.Participant{Module: "usage", Fields: fields, Keys: keys, Prepare: func(_ context.Context, input settings.Fields, _ map[string]string) (settings.PreparedChange, error) {
		if len(input) == 0 {
			return settings.PreparedChange{}, nil
		}
		var projected struct {
			Limit           int                `json:"usage_ranking_limit"`
			Enabled         bool               `json:"usage_ranking_enabled"`
			SortBy          UsageRankingSortBy `json:"usage_ranking_sort_by"`
			ShowTotalTokens bool               `json:"usage_ranking_show_total_tokens"`
			ShowRequests    bool               `json:"usage_ranking_show_requests"`
			ShowActualCost  bool               `json:"usage_ranking_show_actual_cost"`
			ErrorsAllowed   bool               `json:"allow_user_view_error_requests"`
		}
		raw, err := json.Marshal(input)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		if err = json.Unmarshal(raw, &projected); err != nil {
			return settings.PreparedChange{}, err
		}
		_, values := PrepareRankingSettings(UsageRankingSettings{Limit: projected.Limit, Enabled: projected.Enabled, SortBy: projected.SortBy, ShowTotalTokens: projected.ShowTotalTokens, ShowRequests: projected.ShowRequests, ShowActualCost: projected.ShowActualCost})
		values[SettingKeyAllowUserViewErrorRequests] = strconv.FormatBool(projected.ErrorsAllowed)
		for i, field := range fields {
			if _, ok := input[field]; !ok {
				delete(values, keys[i])
			}
		}
		return settings.PreparedChange{Values: values}, nil
	}}
}

func (rankingSortError) Error() string { return "Invalid usage ranking sort field" }
