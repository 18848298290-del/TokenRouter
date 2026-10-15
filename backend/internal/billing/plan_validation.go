package billing

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// plan_validation.go validates plan terms and normalizes group, currency and
// validity inputs.
// Plan creation checks required sale terms, while patches validate
// only the fields they request. Nullable quota and comparison-price
// patches can therefore clear a value or write zero without making
// omitted fields fail creation-style requirements. Numeric checks here
// use their stated comparisons and do not perform a separate finite-value
// validation for programmatically supplied floats.
//
// Group normalization first determines the selected positive identities
// and their supplied order, then keeps rates that apply to that
// selection. A bad rate for an unselected group is discarded before
// rate validation, whereas an applicable nonpositive rate is rejected.
// Duration conversion uses fixed seven- and thirty-day units; currency
// normalization keeps an empty unspecified value for callers to
// interpret in their own checkout or display context.

// ComputeValidityDays converts exact week/weeks and month/months
// unit names to seven- and thirty-day durations. Other unit text
// returns days unchanged, including different casing or whitespace;
// callers validate the count and accepted units separately from this
// unchecked integer arithmetic.
func ComputeValidityDays(days int, unit string) int {
	switch unit {
	case "week", "weeks":
		// Week duration uses seven issued days per configured unit.
		return days * 7
	case "month", "months":
		// Month duration uses thirty issued days per configured unit.
		return days * 30
	default:
		return days
	}
}

// ValidatePlanQuotas rejects a negative supplied quota in daily,
// weekly, then monthly order. Nil pointers and zero are accepted
// for unlimited windows; comparisons are used directly, with no
// separate check rejecting NaN or positive infinity.
func ValidatePlanQuotas(daily, weekly, monthly *float64) error {
	for _, item := range []struct {
		value *float64 // optional USD quota
		code  string   // structured rejection reason
		label string   // quota period label
	}{
		{value: daily, code: "PLAN_DAILY_LIMIT_INVALID", label: "daily limit"},
		{value: weekly, code: "PLAN_WEEKLY_LIMIT_INVALID", label: "weekly limit"},
		{value: monthly, code: "PLAN_MONTHLY_LIMIT_INVALID", label: "monthly limit"},
	} {
		if item.value != nil && *item.value < 0 {
			return apperror.BadRequest(item.code, item.label+" must be >= 0")
		}
	}
	return nil
}

// NormalizePlanCurrency trims and uppercases a three-letter ASCII
// code while keeping empty input unspecified. Invalid length or
// nonletter content returns a structured error; valid syntax does
// not imply lookup in a currency-membership registry.
func NormalizePlanCurrency(raw string) (string, error) {
	currency := strings.ToUpper(strings.TrimSpace(raw))
	if currency == "" {
		return "", nil
	}
	// Check the three-letter ASCII format while retaining an empty
	// unspecified currency above; callers do not obtain registry membership
	// validation from this text normalization.
	valid := len(currency) == 3
	for _, r := range currency {
		if r < 'A' || r > 'Z' {
			valid = false
		}
	}
	if !valid {
		return "", apperror.BadRequest("PLAN_CURRENCY_INVALID", "currency must be a 3-letter ISO currency code")
	}
	return currency, nil
}

// NormalizePlanGroupIDs returns a new ordered slice of distinct
// positive group IDs. A positive legacy groupID is first, followed
// by accepted list entries in supplied order; an empty selection
// returns an allocated empty slice.
func NormalizePlanGroupIDs(groupID int64, groupIDs []int64) []int64 {
	seen := make(map[int64]struct{}, len(groupIDs)+1)
	out := make([]int64, 0, len(groupIDs)+1)
	if groupID > 0 {
		seen[groupID] = struct{}{}
		out = append(out, groupID)
	}
	for _, id := range groupIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// NormalizePlanGroupRateMultipliers returns a new map containing
// rates for selected positive group IDs. Entries with nonpositive
// or unselected IDs are discarded before their rate is inspected.
// An applicable rate at or below zero returns a structured error.
//
// The selected identity set and the rate map can come from different
// parts of a create or update request, so filtering determines which
// rate errors affect that request. Empty selection therefore produces
// an empty map even when unused rate entries are invalid. Float
// comparisons here do not separately reject NaN or positive infinity.
func NormalizePlanGroupRateMultipliers(groupIDs []int64, rates map[int64]float64) (map[int64]float64, error) {
	selected := make(map[int64]struct{}, len(groupIDs))
	for _, groupID := range groupIDs {
		if groupID <= 0 {
			continue
		}
		selected[groupID] = struct{}{}
	}

	out := make(map[int64]float64, len(selected))
	for groupID, rate := range rates {
		if groupID <= 0 {
			continue
		}
		if _, ok := selected[groupID]; !ok {
			continue
		}
		if rate <= 0 {
			return nil, apperror.BadRequest("PLAN_GROUP_RATE_INVALID", "plan group rate multiplier must be > 0")
		}
		out[groupID] = rate
	}
	return out, nil
}

// ValidatePlanRequired checks name, price, validity count, unit,
// and optional comparison price in that order, returning the first
// structured error. Empty text and nonpositive required values fail;
// comparison price may be nil or zero. Unit names and float finiteness
// are not independently validated by these comparisons.
func ValidatePlanRequired(name string, price float64, validityDays int, validityUnit string, originalPrice *float64) error {
	if strings.TrimSpace(name) == "" {
		return apperror.BadRequest("PLAN_NAME_REQUIRED", "plan name is required")
	}
	if price <= 0 {
		return apperror.BadRequest("PLAN_PRICE_INVALID", "price must be > 0")
	}
	if validityDays <= 0 {
		return apperror.BadRequest("PLAN_VALIDITY_REQUIRED", "validity days must be > 0")
	}
	if strings.TrimSpace(validityUnit) == "" {
		return apperror.BadRequest("PLAN_VALIDITY_UNIT_REQUIRED", "validity unit is required")
	}
	if originalPrice != nil && *originalPrice < 0 {
		return apperror.BadRequest("PLAN_ORIGINAL_PRICE_INVALID", "original price must be >= 0")
	}
	return nil
}

// ValidatePlanPatch checks supplied ordinary pointers and a present
// numeric comparison-price patch, then delegates quota-patch checks.
// Omitted fields and explicit nullable clears are accepted. Group and
// currency changes and localization use their owning service or adapter
// rules; this helper performs no separate float-finiteness check.
func ValidatePlanPatch(req UpdatePlanRequest) error {
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		return apperror.BadRequest("PLAN_NAME_REQUIRED", "plan name is required")
	}
	if req.Price != nil && *req.Price <= 0 {
		return apperror.BadRequest("PLAN_PRICE_INVALID", "price must be > 0")
	}
	if req.ValidityDays != nil && *req.ValidityDays <= 0 {
		return apperror.BadRequest("PLAN_VALIDITY_REQUIRED", "validity days must be > 0")
	}
	if req.ValidityUnit != nil && strings.TrimSpace(*req.ValidityUnit) == "" {
		return apperror.BadRequest("PLAN_VALIDITY_UNIT_REQUIRED", "validity unit is required")
	}
	if req.OriginalPrice.Present && req.OriginalPrice.Value != nil && *req.OriginalPrice.Value < 0 {
		return apperror.BadRequest("PLAN_ORIGINAL_PRICE_INVALID", "original price must be >= 0")
	}
	return ValidatePlanQuotaPatch(req)
}

// ValidatePlanQuotaPatch rejects negative received quota numbers
// in daily, weekly, then monthly order. A missing patch or present
// nil Value is accepted, and zero remains a valid replacement;
// there is no separate finite-value check.
func ValidatePlanQuotaPatch(req UpdatePlanRequest) error {
	for _, item := range []struct {
		field NullableFloat64Patch // received quota number patch
		code  string               // structured rejection reason
		label string               // quota period label
	}{
		{field: req.DailyLimitUSD, code: "PLAN_DAILY_LIMIT_INVALID", label: "daily limit"},
		{field: req.WeeklyLimitUSD, code: "PLAN_WEEKLY_LIMIT_INVALID", label: "weekly limit"},
		{field: req.MonthlyLimitUSD, code: "PLAN_MONTHLY_LIMIT_INVALID", label: "monthly limit"},
	} {
		if item.field.Present && item.field.Value != nil && *item.field.Value < 0 {
			return apperror.BadRequest(item.code, item.label+" must be >= 0")
		}
	}
	return nil
}
