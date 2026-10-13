package promotion

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Partial administrator requests distinguish an omitted promotion setting
// from an explicitly supplied zero or false value. Decoding into
// AdminSettings supplies zero values for omitted fields, so normalization and
// string formatting alone would otherwise write settings the request never
// selected.
//
// SettingsParticipant keeps matching field and storage-key slices and removes
// encoded values for every absent input field before returning a prepared
// change. The settings coordinator persists that change with other modules.
// Rates are bounded through ClampRebateRate, negative periods and caps become
// defaults, and decimal rates and caps use eight fractional places in saved
// text.

// AdminSettings carries administrator-editable promotion switches and numeric
// rebate policy.
type AdminSettings struct {
	// registration Promo grant switch
	PromoCodeEnabled bool `json:"promo_code_enabled"`
	// registration invitation requirement switch
	InvitationCodeEnabled bool `json:"invitation_code_enabled"`
	// inviter binding and rebate switch
	AffiliateEnabled bool `json:"affiliate_enabled"`
	// AffiliateRebateRate, AffiliateRebateFreezeHours,
	// AffiliateRebateDurationDays, and AffiliateRebatePerInviteeCap are
	// normalized before persistence. Zero periods and zero cap leave freezing,
	// duration, and cumulative limits inactive respectively.
	// percent
	AffiliateRebateRate float64 `json:"affiliate_rebate_rate"`
	// hours
	AffiliateRebateFreezeHours int `json:"affiliate_rebate_freeze_hours"`
	// days
	AffiliateRebateDurationDays int `json:"affiliate_rebate_duration_days"`
	// reasoning points
	AffiliateRebatePerInviteeCap float64 `json:"affiliate_rebate_per_invitee_cap"`
	// administrator recharge rebate switch
	AdminRechargeRebateEnabled bool `json:"affiliate_admin_recharge_enabled"`
}

// NormalizeAdminSettings returns a copy with the rebate percentage bounded
// and negative periods or caps replaced by defaults.
//
// Periods are also capped; cap comparisons retain NaN and positive infinity.
func NormalizeAdminSettings(value AdminSettings) AdminSettings {
	value.AffiliateRebateRate = ClampRebateRate(value.AffiliateRebateRate)
	if value.AffiliateRebateFreezeHours < 0 {
		value.AffiliateRebateFreezeHours = AffiliateRebateFreezeHoursDefault
	}
	if value.AffiliateRebateFreezeHours > AffiliateRebateFreezeHoursMax {
		// RuntimeSettings applies this same ceiling when reading saved freeze
		// policy.
		value.AffiliateRebateFreezeHours = AffiliateRebateFreezeHoursMax
	}
	if value.AffiliateRebateDurationDays < 0 {
		value.AffiliateRebateDurationDays = AffiliateRebateDurationDaysDefault
	}
	if value.AffiliateRebateDurationDays > AffiliateRebateDurationDaysMax {
		// RuntimeSettings applies this same ceiling when reading saved lifetime
		// policy.
		value.AffiliateRebateDurationDays = AffiliateRebateDurationDaysMax
	}
	if value.AffiliateRebatePerInviteeCap < 0 {
		value.AffiliateRebatePerInviteeCap = AffiliateRebatePerInviteeCapDefault
	}

	return value
}

// PrepareAdminSettings returns normalized settings and fresh storage text
// keyed by promotion setting names.
//
// Percentages and caps use eight fractional decimal places; switches and
// integer periods use their standard textual forms.
func PrepareAdminSettings(value AdminSettings) (AdminSettings, map[string]string) {
	value = NormalizeAdminSettings(value)
	// A cap of 0.000000001 formats as zero at eight places, and runtime
	// interprets that saved zero as unlimited.
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

// SettingsParticipant returns preparation rules for the promotion fields in a
// combined administrator update.
//
// The callback decodes the supplied field map, normalizes its values, and
// formats every promotion key. Present JSON null fields retain decoded zero
// values and count as supplied entries. It then pairs fields with keys by
// index and deletes values whose fields were omitted, so an explicit zero or
// false can be saved while an absent field retains its stored value. The
// returned participant only prepares changes; the settings coordinator owns
// persistence and publication.
//
// Fields and Keys share the slices captured by Prepare. Callers keep their
// contents and ordering intact; NewRegistry clones declared ownership lists
// while the callback retains those captured originals.
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
		// Decoding fills omitted fields with zero values; remove their matching
		// keys so a partial request cannot overwrite unrelated settings.
		for i, field := range fields {
			if _, ok := input[field]; !ok {
				delete(values, keys[i])
			}
		}
		return settings.PreparedChange{Values: values}, nil
	}}
}
