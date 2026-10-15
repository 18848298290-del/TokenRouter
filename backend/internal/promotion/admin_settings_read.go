package promotion

import (
	"strconv"
)

// admin_settings_read.go builds administrator promotion settings from stored
// string values.
// Administrator settings display depends on the exact stored boolean text:
// Promo remains enabled unless its value is false, while other switches
// require true. Numeric fields use their own parse defaults and bounds when
// building the response.

// AdminReadSettings is the promotion policy display parsed from a saved
// string map.
type AdminReadSettings struct {
	AdminRechargeRebateEnabled bool // admin recharge rebate switch
	AffiliateEnabled           bool // invite rebate master switch
	// AffiliateRebateDurationDays starts at zero for absent, invalid, or
	// negative text; positive parsed values are capped at the supported duration
	// maximum.
	AffiliateRebateDurationDays int // days
	// AffiliateRebateFreezeHours uses zero for absent, invalid, or negative
	// text; parsed positive values are capped before the administrator response
	// is returned.
	AffiliateRebateFreezeHours int // hours
	// AffiliateRebatePerInviteeCap accepts nonnegative parsed values, including
	// positive infinity. Absent or rejected text leaves the zero value displayed
	// to administrators.
	AffiliateRebatePerInviteeCap float64 // reasoning points
	// AffiliateRebateRate uses ClampRebateRate for parsed text and the default
	// percentage on parsing failure, matching the display's bounded rate
	// representation.
	AffiliateRebateRate   float64 // percent
	InvitationCodeEnabled bool    // signup invitation requirement switch
	PromoCodeEnabled      bool    // signup balance promotion switch
}

// ReadAdminSettings returns a new administrator view from supplied settings
// text.
//
// Missing keys follow the same rules as empty strings. Boolean checks use
// exact text, while numeric parsing does not trim surrounding whitespace.
func ReadAdminSettings(settings map[string]string) *AdminReadSettings {
	result := &AdminReadSettings{}
	result.PromoCodeEnabled = settings[SettingKeyPromoCodeEnabled] != "false"
	result.InvitationCodeEnabled = settings[SettingKeyInvitationCodeEnabled] == "true"
	result.AffiliateEnabled = settings[SettingKeyAffiliateEnabled] == "true"
	if rebateRate, err := strconv.ParseFloat(settings[SettingKeyAffiliateRebateRate], 64); err == nil {
		result.AffiliateRebateRate = ClampRebateRate(rebateRate)
	} else {
		// Unparseable saved rate text receives the display default, allowing
		// administrators to read the remaining promotion settings together.
		result.AffiliateRebateRate = AffiliateRebateRateDefault
	}
	if freezeHours, err := strconv.Atoi(settings[SettingKeyAffiliateRebateFreezeHours]); err == nil && freezeHours >= 0 {
		if freezeHours > AffiliateRebateFreezeHoursMax {
			// Share the runtime reader's thirty-day freeze ceiling.
			freezeHours = AffiliateRebateFreezeHoursMax
		}
		result.AffiliateRebateFreezeHours = freezeHours
	}
	if durationDays, err := strconv.Atoi(settings[SettingKeyAffiliateRebateDurationDays]); err == nil && durationDays >= 0 {
		if durationDays > AffiliateRebateDurationDaysMax {
			// Share the runtime reader's 3,650-day lifetime ceiling.
			durationDays = AffiliateRebateDurationDaysMax
		}
		result.AffiliateRebateDurationDays = durationDays
	}
	if perInviteeCap, err := strconv.ParseFloat(settings[SettingKeyAffiliateRebatePerInviteeCap], 64); err == nil && perInviteeCap >= 0 {
		result.AffiliateRebatePerInviteeCap = perInviteeCap
	}
	result.AdminRechargeRebateEnabled = settings[SettingKeyAffiliateAdminRechargeEnabled] == "true"
	return result
}
