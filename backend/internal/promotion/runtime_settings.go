package promotion

import (
	"context"
	"math"
	"strconv"
	"strings"
)

// runtime_settings.go reads live promotion policy from saved settings and
// applies defaults and bounds.
// Promotion policy is read from saved settings at each decision, so changes
// can affect later calls on the same service. Boolean values use exact text
// matching; numeric settings trim whitespace and select defaults when storage
// or parsing fails.

// Setting keys connect administrator persistence with the live switches and
// numeric rebate policy read by promotion services.
const (
	// administrator recharge rebate switch key.
	SettingKeyAffiliateAdminRechargeEnabled = "affiliate_admin_recharge_enabled"
	// invite rebate enablement key.
	SettingKeyAffiliateEnabled = "affiliate_enabled"
	// invitee eligible lifetime key.
	SettingKeyAffiliateRebateDurationDays = "affiliate_rebate_duration_days"
	// new rebate freeze period key.
	SettingKeyAffiliateRebateFreezeHours = "affiliate_rebate_freeze_hours"
	// pair lifetime quota cap key.
	SettingKeyAffiliateRebatePerInviteeCap = "affiliate_rebate_per_invitee_cap"
	// global rebate percentage key.
	SettingKeyAffiliateRebateRate = "affiliate_rebate_rate"
	// signup invitation requirement key.
	SettingKeyInvitationCodeEnabled = "invitation_code_enabled"
	// signup Promo enablement key.
	SettingKeyPromoCodeEnabled = "promo_code_enabled"
)

// RuntimeSettingsStore retrieves saved promotion text through the caller's
// context.
type RuntimeSettingsStore interface {
	// GetValue returns saved text for the requested setting key or a read error.
	//
	// RuntimeSettings applies each key's default on errors, including
	// cancellation returned by the implementation.
	GetValue(context.Context, string) (string, error)
}

// RuntimeSettings interprets saved promotion policy separately for each
// method call.
type RuntimeSettings struct {
	// settingRepo is retained by NewRuntimeSettings and used on every policy
	// call. Callers provide a usable reader and manage its lifetime.
	settingRepo RuntimeSettingsStore // live saved settings reader
}

// NewRuntimeSettings retains repo for later live promotion-policy reads
// without fetching values.
//
// The reader is required by policy methods; callers manage its availability
// and concurrent access.
func NewRuntimeSettings(repo RuntimeSettingsStore) *RuntimeSettings {
	return &RuntimeSettings{settingRepo: repo}
}

// IsPromoCodeEnabled reports whether registration Promo grants are enabled by
// saved text.
//
// Only exact false disables the feature; failed reads select enabled so
// signup previews remain available.
func (s *RuntimeSettings) IsPromoCodeEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyPromoCodeEnabled)
	if err != nil {
		// Signup promotion policy remains available when the saved switch cannot be
		// read, including a reader-reported context cancellation.
		return true
	}
	return value != "false"
}

// IsInvitationCodeEnabled reports whether saved policy requires a
// registration invitation code.
//
// Only exact true enables the requirement; failed reads select disabled and
// allow the signup caller to proceed.
func (s *RuntimeSettings) IsInvitationCodeEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyInvitationCodeEnabled)
	if err != nil {
		// A failed switch read removes the invitation requirement for this
		// decision, allowing the registration caller's default admission path.
		return false
	}
	return value == "true"
}

// IsAffiliateEnabled reports whether saved policy enables inviter binding and
// new rebate accrual.
//
// Only exact true enables the feature; failed reads use the disabled
// affiliate default.
func (s *RuntimeSettings) IsAffiliateEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateEnabled)
	if err != nil {
		// Failed policy reads select disabled so a missing switch cannot start new
		// inviter binding or rebate accrual.
		return AffiliateEnabledDefault
	}
	return value == "true"
}

// IsAffiliateAdminRechargeEnabled reports whether saved policy allows
// administrator recharges to generate invite rebates.
//
// Only exact true enables that path; failed reads select the default that
// skips those rebates.
func (s *RuntimeSettings) IsAffiliateAdminRechargeEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateAdminRechargeEnabled)
	if err != nil {
		// A failed switch read selects the policy that skips administrator recharge
		// rebates while the caller completes its recharge.
		return AdminRechargeRebateEnabledDefault
	}
	return value == "true"
}

// GetAffiliateRebateRatePercent returns the saved global percentage after
// numeric validation and clamping.
//
// Whitespace is trimmed. Read failures, parse failures, NaN, and infinity
// select the default percentage.
func (s *RuntimeSettings) GetAffiliateRebateRatePercent(ctx context.Context) float64 {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateRebateRate)
	if err != nil {
		// Failed setting retrieval supplies the percentage default so this policy
		// decision still has a usable grant rate.
		return AffiliateRebateRateDefault
	}
	rate, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(rate) || math.IsInf(rate, 0) {
		// Malformed or nonfinite saved percentages select the default, preventing
		// unusable arithmetic from entering the invite rebate calculation.
		return AffiliateRebateRateDefault
	}
	return ClampRebateRate(rate)
}

// GetAffiliateRebateFreezeHours returns the saved freeze period for newly
// accrued rebates.
//
// Whitespace is trimmed; failed reads, invalid integers, and negative values
// select zero, while excessive values select the supported maximum.
func (s *RuntimeSettings) GetAffiliateRebateFreezeHours(ctx context.Context) int {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateRebateFreezeHours)
	if err != nil {
		// When saved freeze policy is unavailable, credit remains immediately
		// available because accrual receives the zero-hour default for this
		// decision.
		return AffiliateRebateFreezeHoursDefault
	}
	hours, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || hours < 0 {
		// Malformed or negative freeze text selects zero hours, so an eligible
		// purchase can accrue directly into transferable quota under the other
		// policy checks.
		return AffiliateRebateFreezeHoursDefault
	}
	if hours > AffiliateRebateFreezeHoursMax {
		// Match the administrator writer's thirty-day freeze ceiling.
		return AffiliateRebateFreezeHoursMax
	}
	return hours
}

// GetAffiliateRebateDurationDays returns the saved eligible lifetime from
// invitee profile creation.
//
// Whitespace is trimmed; failed reads, invalid integers, and negative values
// select zero, while excessive values select the supported maximum.
func (s *RuntimeSettings) GetAffiliateRebateDurationDays(ctx context.Context) int {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateRebateDurationDays)
	if err != nil {
		// When the saved lifetime cannot be read, zero keeps later purchases
		// eligible regardless of profile age while the other rebate rules still
		// apply.
		return AffiliateRebateDurationDaysDefault
	}
	days, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || days < 0 {
		// An unusable or negative duration supplies zero days, leaving profile age
		// unrestricted for this rebate decision while inviter and rate checks
		// remain active.
		return AffiliateRebateDurationDaysDefault
	}
	if days > AffiliateRebateDurationDaysMax {
		// Match the administrator writer's 3,650-day lifetime ceiling.
		return AffiliateRebateDurationDaysMax
	}
	return days
}

// GetAffiliateRebatePerInviteeCap returns the saved reasoning-point lifetime
// cap for an inviter and invitee pair.
//
// Failed reads and invalid, negative, or nonfinite numbers select zero,
// leaving the cap unlimited.
func (s *RuntimeSettings) GetAffiliateRebatePerInviteeCap(ctx context.Context) float64 {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAffiliateRebatePerInviteeCap)
	if err != nil {
		// When the saved cap cannot be read, zero leaves this pair's cumulative
		// grant amount unlimited for the current purchase.
		return AffiliateRebatePerInviteeCapDefault
	}
	capValue, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || capValue < 0 || math.IsNaN(capValue) || math.IsInf(capValue, 0) {
		// Invalid, negative, or nonfinite cap text selects zero, so cumulative pair
		// totals do not restrict this purchase's grant under the remaining policy
		// rules.
		return AffiliateRebatePerInviteeCapDefault
	}
	return capValue
}
