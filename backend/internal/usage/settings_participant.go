package usage

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Ranking configuration combines an ordering metric with fields visible in
// the response. Updating the metric can require that metric's visibility flag
// to become true, and normalization applies the configured limit cap before
// values are prepared for storage.
//
// Direct updates retain current values for omitted pointer fields.
// SettingsParticipant instead decodes the supplied settings fields,
// normalizes their ranking values and publishes string entries only for names
// present in that input. This presence check prevents absent JSON fields from
// being persisted as the decoder's zero values; readers normalize persisted
// settings again when serving rankings.

// RankingSettingsUpdate describes a partial update to ranking order,
// visibility and enablement.
type RankingSettingsUpdate struct {
	// Limit replaces the current result limit when positive. Nonpositive values
	// leave the current value for later normalization and maximum-limit
	// handling.
	Limit int // requested ranking row limit
	// Enabled, SortBy, ShowTotalTokens, ShowRequests and ShowActualCost use nil
	// to retain their corresponding current values. Supplied pointers
	// participate in the partial update before normalization.
	Enabled         *bool   // optional ranking enable flag
	SortBy          *string // optional ranking order metric
	ShowTotalTokens *bool   // optional token visibility flag
	ShowRequests    *bool   // optional request visibility flag
	ShowActualCost  *bool   // optional cost visibility flag
}

// rankingSortError identifies a rejected ranking order field in HTTP updates.
type rankingSortError struct{}

// ResolveRankingSettings applies supplied update fields to current and
// returns normalized ranking settings. A positive limit replaces the current
// limit, and omitted pointer fields keep their current values; a supplied
// unsupported sort field returns rankingSortError.
//
// Normalization couples sort order with visibility by making the chosen
// metric visible and resolving limit defaults. The returned configuration
// therefore reflects both the requested changes and the display fields
// required to explain its ranking order.
func ResolveRankingSettings(current UsageRankingSettings, input RankingSettingsUpdate) (UsageRankingSettings, error) {
	if input.Limit > 0 {
		current.Limit = input.Limit
	}
	// Bound the result count consumed by the public ranking query.
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

// PrepareRankingSettings returns normalized ranking settings and their
// persisted string entries. It encodes each ranking field after
// normalization, so callers can store the resolved limit, order and
// visibility together.
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

// SettingsParticipant declares the usage settings fields and their
// persistence preparation callback. Empty input produces an empty change;
// supplied fields are decoded into ranking and user error-visibility values
// before ranking normalization and string encoding.
//
// The callback removes prepared values whose corresponding field names were
// absent in the input. This keeps decoder zero values from overwriting
// omitted settings during a partial update, while retaining normalization of
// the fields actually submitted. Persistence and later settings publication
// belong to the settings service.
func SettingsParticipant() settings.Participant {
	fields := []string{"usage_ranking_limit", "usage_ranking_enabled", "usage_ranking_sort_by", "usage_ranking_show_total_tokens", "usage_ranking_show_requests", "usage_ranking_show_actual_cost", "allow_user_view_error_requests"}
	keys := []string{SettingKeyUsageRankingLimit, SettingKeyUsageRankingEnabled, SettingKeyUsageRankingSortBy, SettingKeyUsageRankingShowTotalTokens, SettingKeyUsageRankingShowRequests, SettingKeyUsageRankingShowActualCost, SettingKeyAllowUserViewErrorRequests}
	return settings.Participant{Module: "usage", Fields: fields, Keys: keys, Prepare: func(_ context.Context, input settings.Fields, _ map[string]string) (settings.PreparedChange, error) {
		if len(input) == 0 {
			return settings.PreparedChange{}, nil
		}
		var projected struct {
			Limit           int                `json:"usage_ranking_limit"`             // submitted ranking row limit
			Enabled         bool               `json:"usage_ranking_enabled"`           // submitted ranking enable flag
			SortBy          UsageRankingSortBy `json:"usage_ranking_sort_by"`           // submitted ranking order metric
			ShowTotalTokens bool               `json:"usage_ranking_show_total_tokens"` // submitted token visibility flag
			ShowRequests    bool               `json:"usage_ranking_show_requests"`     // submitted request visibility flag
			ShowActualCost  bool               `json:"usage_ranking_show_actual_cost"`  // submitted charge visibility flag
			ErrorsAllowed   bool               `json:"allow_user_view_error_requests"`  // submitted error visibility flag
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
		// Normalization produces values for every ranking field. Remove entries whose
		// input names were absent so partial settings updates persist the submitted subset.
		for i, field := range fields {
			if _, ok := input[field]; !ok {
				delete(values, keys[i])
			}
		}
		return settings.PreparedChange{Values: values}, nil
	}}
}

// Error returns the rejected ranking-field message used by HTTP callers.
func (rankingSortError) Error() string { return "Invalid usage ranking sort field" }
