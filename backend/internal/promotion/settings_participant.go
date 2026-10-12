package promotion

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// AdminSettings contains promotion switches and rebate configuration.
type AdminSettings struct {
	PromoCodeEnabled             bool    `json:"promo_code_enabled"`
	InvitationCodeEnabled        bool    `json:"invitation_code_enabled"`
	AffiliateEnabled             bool    `json:"affiliate_enabled"`
	AffiliateRebateRate          float64 `json:"affiliate_rebate_rate"`
	AffiliateRebateFreezeHours   int     `json:"affiliate_rebate_freeze_hours"`
	AffiliateRebateDurationDays  int     `json:"affiliate_rebate_duration_days"`
	AffiliateRebatePerInviteeCap float64 `json:"affiliate_rebate_per_invitee_cap"`
	AdminRechargeRebateEnabled   bool    `json:"affiliate_admin_recharge_enabled"`
}

// NormalizeAdminSettings clamps rebate rates and applies duration, freeze, and cap limits.
func NormalizeAdminSettings(value AdminSettings) AdminSettings {
	value.AffiliateRebateRate = ClampRebateRate(value.AffiliateRebateRate)
	if value.AffiliateRebateFreezeHours < 0 {
		value.AffiliateRebateFreezeHours = AffiliateRebateFreezeHoursDefault
	}
	if value.AffiliateRebateFreezeHours > AffiliateRebateFreezeHoursMax {
		value.AffiliateRebateFreezeHours = AffiliateRebateFreezeHoursMax
	}
	if value.AffiliateRebateDurationDays < 0 {
		value.AffiliateRebateDurationDays = AffiliateRebateDurationDaysDefault
	}
	if value.AffiliateRebateDurationDays > AffiliateRebateDurationDaysMax {
		value.AffiliateRebateDurationDays = AffiliateRebateDurationDaysMax
	}
	if value.AffiliateRebatePerInviteeCap < 0 {
		value.AffiliateRebatePerInviteeCap = AffiliateRebatePerInviteeCapDefault
	}

	return value
}

// PrepareAdminSettings normalizes settings and formats persisted decimal values to eight places.
func PrepareAdminSettings(value AdminSettings) (AdminSettings, map[string]string) {
	value = NormalizeAdminSettings(value)
	return value, map[string]string{
		SettingKeyPromoCodeEnabled:              strconv.FormatBool(value.PromoCodeEnabled),
		SettingKeyInvitationCodeEnabled:         strconv.FormatBool(value.InvitationCodeEnabled),
		SettingKeyAffiliateEnabled:              strconv.FormatBool(value.AffiliateEnabled),
		SettingKeyAffiliateRebateRate:           strconv.FormatFloat(value.AffiliateRebateRate, 'f', 8, 64),
		SettingKeyAffiliateRebateFreezeHours:    strconv.Itoa(value.AffiliateRebateFreezeHours),
		SettingKeyAffiliateRebateDurationDays:   strconv.Itoa(value.AffiliateRebateDurationDays),
		SettingKeyAffiliateRebatePerInviteeCap:  strconv.FormatFloat(value.AffiliateRebatePerInviteeCap, 'f', 8, 64),
		SettingKeyAffiliateAdminRechargeEnabled: strconv.FormatBool(value.AdminRechargeRebateEnabled),
	}
}

// SettingsParticipant prepares supplied promotion fields and leaves omitted values in storage unchanged.
func SettingsParticipant() settings.Participant {
	fields := []string{"promo_code_enabled", "invitation_code_enabled", "affiliate_enabled", "affiliate_rebate_rate", "affiliate_rebate_freeze_hours", "affiliate_rebate_duration_days", "affiliate_rebate_per_invitee_cap", "affiliate_admin_recharge_enabled"}
	keys := []string{SettingKeyPromoCodeEnabled, SettingKeyInvitationCodeEnabled, SettingKeyAffiliateEnabled, SettingKeyAffiliateRebateRate, SettingKeyAffiliateRebateFreezeHours, SettingKeyAffiliateRebateDurationDays, SettingKeyAffiliateRebatePerInviteeCap, SettingKeyAffiliateAdminRechargeEnabled}
	return settings.Participant{Module: "promotion", Fields: fields, Keys: keys, Prepare: func(_ context.Context, input settings.Fields, _ map[string]string) (settings.PreparedChange, error) {
		if len(input) == 0 {
			return settings.PreparedChange{}, nil
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		var value AdminSettings
		if err = json.Unmarshal(encoded, &value); err != nil {
			return settings.PreparedChange{}, err
		}
		_, values := PrepareAdminSettings(value)
		for i, field := range fields {
			if _, ok := input[field]; !ok {
				delete(values, keys[i])
			}
		}
		return settings.PreparedChange{Values: values}, nil
	}}
}
