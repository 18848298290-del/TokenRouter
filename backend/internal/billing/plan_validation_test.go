package billing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidatePlanRequired_AllValid supplies a named, priced plan with
// a positive duration and no comparison price. It checks acceptance
// by the required-field validator for those creation terms.
func TestValidatePlanRequired_AllValid(t *testing.T) {
	err := ValidatePlanRequired("Pro", 9.99, 30, "days", nil)
	require.NoError(t, err)
}

// TestValidatePlanRequired_EmptyName supplies an empty name with other
// required creation terms populated. It checks that validation returns
// an error whose message identifies the plan-name requirement.
func TestValidatePlanRequired_EmptyName(t *testing.T) {
	err := ValidatePlanRequired("", 9.99, 30, "days", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "plan name")
}

// TestValidatePlanRequired_WhitespaceName supplies a whitespace-only
// name with otherwise populated creation terms. It checks rejection
// and the plan-name diagnostic after whitespace is considered blank.
func TestValidatePlanRequired_WhitespaceName(t *testing.T) {
	err := ValidatePlanRequired("   ", 9.99, 30, "days", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "plan name")
}

// TestValidatePlanRequired_ZeroPrice supplies a zero required sale price.
// It checks rejection and a price diagnostic while the name, duration,
// and unit fields otherwise satisfy the validator.
func TestValidatePlanRequired_ZeroPrice(t *testing.T) {
	err := ValidatePlanRequired("Pro", 0, 30, "days", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "price")
}

// TestValidatePlanRequired_NegativePrice supplies a negative required
// sale price with valid text and duration. It checks that validation
// returns an error identifying the rejected price.
func TestValidatePlanRequired_NegativePrice(t *testing.T) {
	err := ValidatePlanRequired("Pro", -5, 30, "days", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "price")
}

// TestValidatePlanRequired_ZeroValidityDays supplies a zero duration count
// for an otherwise populated plan. It checks rejection and a diagnostic
// that identifies validity days as the failing required value.
func TestValidatePlanRequired_ZeroValidityDays(t *testing.T) {
	err := ValidatePlanRequired("Pro", 9.99, 0, "days", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "validity days")
}

// TestValidatePlanRequired_NegativeValidityDays supplies a negative
// duration count with valid name, price, and unit text. It checks
// rejection with the validity-days diagnostic from the creation validator.
func TestValidatePlanRequired_NegativeValidityDays(t *testing.T) {
	err := ValidatePlanRequired("Pro", 9.99, -7, "days", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "validity days")
}

// TestValidatePlanRequired_EmptyValidityUnit supplies an empty unit with
// a positive duration count. It checks a validation error identifying
// the missing unit while earlier required fields remain valid.
func TestValidatePlanRequired_EmptyValidityUnit(t *testing.T) {
	err := ValidatePlanRequired("Pro", 9.99, 30, "", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "validity unit")
}

// TestValidatePlanRequired_WhitespaceValidityUnit supplies unit text made
// only of spaces. It checks rejection and the validity-unit diagnostic
// when the validator treats that text as blank.
func TestValidatePlanRequired_WhitespaceValidityUnit(t *testing.T) {
	err := ValidatePlanRequired("Pro", 9.99, 30, "   ", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "validity unit")
}

// TestValidatePlanRequired_NameValidatedFirst makes the name, price,
// duration, and unit invalid together. It checks the plan-name error
// message, protecting the required validator's first-rejection order.
func TestValidatePlanRequired_NameValidatedFirst(t *testing.T) {
	err := ValidatePlanRequired("", 0, 0, "", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "plan name")
}

// TestValidatePlanRequired_TrimmedValidName supplies a padded nonblank
// name with valid creation terms. It checks acceptance; the test
// observes validation success and does not assert stored-name trimming.
func TestValidatePlanRequired_TrimmedValidName(t *testing.T) {
	err := ValidatePlanRequired("  Pro  ", 9.99, 30, "days", nil)
	require.NoError(t, err)
}

// TestValidatePlanRequired_NegativeOriginalPrice supplies a negative
// comparison price after valid required terms. It checks rejection
// and the original-price diagnostic for that optional numeric input.
func TestValidatePlanRequired_NegativeOriginalPrice(t *testing.T) {
	neg := -10.0
	err := ValidatePlanRequired("Pro", 9.99, 30, "days", &neg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "original price")
}

// TestValidatePlanRequired_ZeroOriginalPrice supplies an explicit zero
// comparison price alongside valid required terms. It checks that
// this optional display value is accepted by creation validation.
func TestValidatePlanRequired_ZeroOriginalPrice(t *testing.T) {
	zero := 0.0
	err := ValidatePlanRequired("Pro", 9.99, 30, "days", &zero)
	require.NoError(t, err)
}

// TestValidatePlanRequired_ValidOriginalPrice supplies a positive
// comparison price with valid required sale terms. It checks successful
// creation validation for the optional price when its value is positive.
func TestValidatePlanRequired_ValidOriginalPrice(t *testing.T) {
	op := 19.99
	err := ValidatePlanRequired("Pro", 9.99, 30, "days", &op)
	require.NoError(t, err)
}

// TestValidatePlanPatch_NegativeOriginalPrice supplies a received negative
// comparison-price patch. It checks rejection and the original-price
// diagnostic while other optional patch fields remain absent.
func TestValidatePlanPatch_NegativeOriginalPrice(t *testing.T) {
	neg := -5.0
	err := ValidatePlanPatch(UpdatePlanRequest{OriginalPrice: nullablePatchFloat(neg)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "original price")
}

// TestValidatePlanPatch_ZeroOriginalPrice marks a zero comparison-price
// value as present. It checks successful patch validation, keeping
// explicit numeric zero distinct from a rejected negative replacement.
func TestValidatePlanPatch_ZeroOriginalPrice(t *testing.T) {
	zero := 0.0
	err := ValidatePlanPatch(UpdatePlanRequest{OriginalPrice: NullableFloat64Patch{Present: true, Value: &zero}})
	require.NoError(t, err)
}

// TestValidatePlanPatch_ValidOriginalPrice supplies a present positive
// comparison-price value. It checks successful validation of that
// replacement without requiring omitted creation fields to be supplied.
func TestValidatePlanPatch_ValidOriginalPrice(t *testing.T) {
	op := 29.99
	err := ValidatePlanPatch(UpdatePlanRequest{OriginalPrice: nullablePatchFloat(op)})
	require.NoError(t, err)
}

// TestValidatePlanPatch_NilOriginalPrice marks comparison price present
// with nil Value. It checks acceptance of the explicit clear state
// by validation; the test does not execute a repository update.
func TestValidatePlanPatch_NilOriginalPrice(t *testing.T) {
	err := ValidatePlanPatch(UpdatePlanRequest{OriginalPrice: NullableFloat64Patch{Present: true}})
	require.NoError(t, err)
}

// TestValidatePlanPatch_NegativeQuotaLimit supplies a received negative
// weekly quota value. It checks a validation error with the weekly-limit
// diagnostic while the other quota patches are omitted.
func TestValidatePlanPatch_NegativeQuotaLimit(t *testing.T) {
	err := ValidatePlanPatch(UpdatePlanRequest{WeeklyLimitUSD: nullablePatchFloat(-1)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "weekly limit")
}

// TestValidatePlanPatch_EmptyName supplies an ordinary name pointer
// containing empty text. It checks rejection and the plan-name
// diagnostic for that requested replacement rather than an omitted name.
func TestValidatePlanPatch_EmptyName(t *testing.T) {
	err := ValidatePlanPatch(UpdatePlanRequest{Name: ptrStr("")})
	require.Error(t, err)
	require.Contains(t, err.Error(), "plan name")
}

// TestValidatePlanPatch_ValidName supplies a nonblank ordinary name
// pointer while other fields remain absent. It checks acceptance
// by the patch validator for that single text replacement.
func TestValidatePlanPatch_ValidName(t *testing.T) {
	err := ValidatePlanPatch(UpdatePlanRequest{Name: ptrStr("Basic")})
	require.NoError(t, err)
}

// TestValidatePlanPatch_GroupIDIgnored supplies a pointer to zero
// GroupID without other patch values. It checks that this validator
// returns no error; repository handling of group selection is outside
// the assertion.
func TestValidatePlanPatch_GroupIDIgnored(t *testing.T) {
	err := ValidatePlanPatch(UpdatePlanRequest{GroupID: ptrInt64(0)})
	require.NoError(t, err)
}

// TestValidatePlanPatch_NegativePrice supplies a negative sale-price
// pointer with other patch fields omitted. It checks rejection and
// the price diagnostic for the requested numeric replacement.
func TestValidatePlanPatch_NegativePrice(t *testing.T) {
	err := ValidatePlanPatch(UpdatePlanRequest{Price: ptrFloat(-1)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "price")
}

// TestValidatePlanPatch_ZeroPrice supplies an explicit zero sale-price
// pointer. It checks rejection with the price diagnostic, retaining
// the distinction from optional zero comparison prices and quota values.
func TestValidatePlanPatch_ZeroPrice(t *testing.T) {
	err := ValidatePlanPatch(UpdatePlanRequest{Price: ptrFloat(0)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "price")
}

// TestValidatePlanPatch_ValidPrice supplies one positive sale-price
// pointer and omits other fields. It checks that the patch validator
// accepts that requested replacement without requiring complete creation
// terms.
func TestValidatePlanPatch_ValidPrice(t *testing.T) {
	err := ValidatePlanPatch(UpdatePlanRequest{Price: ptrFloat(9.99)})
	require.NoError(t, err)
}

// TestValidatePlanPatch_ZeroValidityDays supplies an explicit zero
// duration-count pointer. It checks rejection and the validity-days
// diagnostic for that replacement while all other fields are omitted.
func TestValidatePlanPatch_ZeroValidityDays(t *testing.T) {
	err := ValidatePlanPatch(UpdatePlanRequest{ValidityDays: ptrInt(0)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "validity days")
}

// TestValidatePlanPatch_EmptyValidityUnit supplies a pointer to empty
// unit text. It checks rejection and a validity-unit diagnostic for
// that requested change while other patch fields remain absent.
func TestValidatePlanPatch_EmptyValidityUnit(t *testing.T) {
	err := ValidatePlanPatch(UpdatePlanRequest{ValidityUnit: ptrStr("")})
	require.Error(t, err)
	require.Contains(t, err.Error(), "validity unit")
}

// TestValidatePlanPatch_ValidValidityUnit supplies the nonblank days
// unit through an ordinary pointer. It checks successful patch
// validation for that field without testing a duration conversion.
func TestValidatePlanPatch_ValidValidityUnit(t *testing.T) {
	err := ValidatePlanPatch(UpdatePlanRequest{ValidityUnit: ptrStr("days")})
	require.NoError(t, err)
}

// TestValidatePlanPatch_AllNil supplies an empty update request with
// all ordinary pointers and presence bits absent. It checks that
// patch validation accepts omission instead of applying required
// creation-field checks.
func TestValidatePlanPatch_AllNil(t *testing.T) {
	err := ValidatePlanPatch(UpdatePlanRequest{})
	require.NoError(t, err)
}

// TestNormalizePlanCurrency_EmptyKeepsEmpty supplies an empty currency
// string. It checks successful normalization with an empty result,
// retaining the plan's unspecified currency instead of selecting a
// payment default.
func TestNormalizePlanCurrency_EmptyKeepsEmpty(t *testing.T) {
	currency, err := NormalizePlanCurrency("")
	require.NoError(t, err)
	require.Equal(t, "", currency)
}

// TestNormalizePlanCurrency_WhitespaceKeepsEmpty supplies only spaces
// as currency text. It checks successful normalization to an empty
// result, keeping a blank plan currency unspecified after trimming.
func TestNormalizePlanCurrency_WhitespaceKeepsEmpty(t *testing.T) {
	currency, err := NormalizePlanCurrency("   ")
	require.NoError(t, err)
	require.Equal(t, "", currency)
}

// TestNormalizePlanCurrency_LowercaseNormalized supplies lowercase NZD
// text. It checks successful normalization to uppercase NZD; the
// assertion protects text format and does not query a currency registry.
func TestNormalizePlanCurrency_LowercaseNormalized(t *testing.T) {
	currency, err := NormalizePlanCurrency("nzd")
	require.NoError(t, err)
	require.Equal(t, "NZD", currency)
}

// TestNormalizePlanCurrency_ValidUppercase supplies an uppercase
// three-letter USD code. It checks successful normalization and the
// unchanged uppercase result for input already using the accepted format.
func TestNormalizePlanCurrency_ValidUppercase(t *testing.T) {
	currency, err := NormalizePlanCurrency("USD")
	require.NoError(t, err)
	require.Equal(t, "USD", currency)
}

// TestNormalizePlanCurrency_TooShort supplies a two-letter code. It
// checks rejection and a currency diagnostic, protecting the required
// three-letter text format rather than assigning a default currency.
func TestNormalizePlanCurrency_TooShort(t *testing.T) {
	_, err := NormalizePlanCurrency("NZ")
	require.Error(t, err)
	require.Contains(t, err.Error(), "currency")
}

// TestNormalizePlanCurrency_TooLong supplies four currency letters. It
// checks rejection and the currency diagnostic when the normalized
// text exceeds the accepted three-letter format for a plan currency.
func TestNormalizePlanCurrency_TooLong(t *testing.T) {
	_, err := NormalizePlanCurrency("NZDD")
	require.Error(t, err)
	require.Contains(t, err.Error(), "currency")
}

// TestNormalizePlanCurrency_NonLetter supplies a three-character code
// containing a digit. It checks rejection and the currency diagnostic
// for nonletter content despite the otherwise accepted input length.
func TestNormalizePlanCurrency_NonLetter(t *testing.T) {
	_, err := NormalizePlanCurrency("N2D")
	require.Error(t, err)
	require.Contains(t, err.Error(), "currency")
}

// ptrStr returns a pointer to the supplied string for optional plan patches.
func ptrStr(s string) *string { return &s }

// ptrInt returns a pointer to the supplied integer for duration patches.
func ptrInt(i int) *int { return &i }

// ptrInt64 returns a pointer to the supplied group identity for patch tests.
func ptrInt64(i int64) *int64 { return &i }

// ptrFloat returns a pointer to the supplied sale price for patch tests.
func ptrFloat(f float64) *float64 { return &f }

// nullablePatchFloat creates a received numeric patch with its own value pointer.
func nullablePatchFloat(f float64) NullableFloat64Patch {
	return NullableFloat64Patch{Present: true, Value: &f}
}
