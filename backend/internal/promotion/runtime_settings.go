package promotion

import (
	"context"
	"math"
	"strconv"
	"strings"
)

// Promotion settings keys identify the stored values interpreted by this package.
const (
	SettingKeyAffiliateAdminRechargeEnabled = "affiliate_admin_recharge_enabled"
	SettingKeyAffiliateEnabled              = "affiliate_enabled"
	SettingKeyAffiliateRebateDurationDays   = "affiliate_rebate_duration_days"
	SettingKeyAffiliateRebateFreezeHours    = "affiliate_rebate_freeze_hours"
	SettingKeyAffiliateRebatePerInviteeCap  = "affiliate_rebate_per_invitee_cap"
	SettingKeyAffiliateRebateRate           = "affiliate_rebate_rate"
	SettingKeyInvitationCodeEnabled         = "invitation_code_enabled"
	SettingKeyPromoCodeEnabled              = "promo_code_enabled"
)

// RuntimeSettingsStore reads promotion settings by key.
type RuntimeSettingsStore interface {
	GetValue(context.Context, string) (string, error)
}

// RuntimeSettings reads stored promotion settings on each call.
type RuntimeSettings struct{ settingRepo RuntimeSettingsStore }

// NewRuntimeSettings builds a settings reader without fetching values.
func NewRuntimeSettings(repo RuntimeSettingsStore) *RuntimeSettings {
	return &RuntimeSettings{settingRepo: repo}
}

// IsPromoCodeEnabled reads the promotion switch and defaults to enabled on read errors.
func (s *RuntimeSettings) IsPromoCodeEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyPromoCodeEnabled)
	if err != nil {
		return true // Enabled on read errors
	}
	return value != "false"
}

// IsInvitationCodeEnabled reads the invitation switch and defaults to disabled on read errors.
func (s *RuntimeSettings) IsInvitationCodeEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyInvitationCodeEnabled)
	if err != nil {
		return false // Disabled on read errors
	}
	return value == "true"
}

// IsAffiliateEnabled reads the affiliate switch and returns the default on read errors.
func (s *RuntimeSettings) IsAffiliateEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateEnabled)
	if err != nil {
		return AffiliateEnabledDefault
	}
	return value == "true"
}

// IsAffiliateAdminRechargeEnabled reads the admin recharge rebate switch and defaults on read errors.
func (s *RuntimeSettings) IsAffiliateAdminRechargeEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateAdminRechargeEnabled)
	if err != nil {
		return AdminRechargeRebateEnabledDefault
	}
	return value == "true"
}

// GetAffiliateRebateRatePercent reads and clamps the rate, using the default for read errors or invalid values.
func (s *RuntimeSettings) GetAffiliateRebateRatePercent(ctx context.Context) float64 {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateRebateRate)
	if err != nil {
		return AffiliateRebateRateDefault
	}
	rate, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return AffiliateRebateRateDefault
	}
	return ClampRebateRate(rate)
}

// GetAffiliateRebateFreezeHours reads and caps freeze hours, using the default for read errors or invalid
// values.
func (s *RuntimeSettings) GetAffiliateRebateFreezeHours(ctx context.Context) int {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateRebateFreezeHours)
	if err != nil {
		return AffiliateRebateFreezeHoursDefault
	}
	hours, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || hours < 0 {
		return AffiliateRebateFreezeHoursDefault
	}
	if hours > AffiliateRebateFreezeHoursMax {
		return AffiliateRebateFreezeHoursMax
	}
	return hours
}

// GetAffiliateRebateDurationDays reads and caps the duration, using the default for read errors or invalid
// values.
func (s *RuntimeSettings) GetAffiliateRebateDurationDays(ctx context.Context) int {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateRebateDurationDays)
	if err != nil {
		return AffiliateRebateDurationDaysDefault
	}
	days, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || days < 0 {
		return AffiliateRebateDurationDaysDefault
	}
	if days > AffiliateRebateDurationDaysMax {
		return AffiliateRebateDurationDaysMax
	}
	return days
}

// GetAffiliateRebatePerInviteeCap reads the cap and defaults on read errors or invalid values.
func (s *RuntimeSettings) GetAffiliateRebatePerInviteeCap(ctx context.Context) float64 {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateRebatePerInviteeCap)
	if err != nil {
		return AffiliateRebatePerInviteeCapDefault
	}
	capValue, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || capValue < 0 || math.IsNaN(capValue) || math.IsInf(capValue, 0) {
		return AffiliateRebatePerInviteeCapDefault
	}
	return capValue
}
