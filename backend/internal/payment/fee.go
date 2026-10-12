package payment

import (
	"github.com/shopspring/decimal"
)

// Checkout fees are calculated from the currency-rounded base amount,
// so a percentage is applied to the amount the channel can actually
// accept. A positive fixed fee rounds to the accepted precision, while
// a positive percentage rate produces a charge rounded away from zero. Nonpositive
// fee settings add no corresponding fee, although the breakdown retains
// the supplied percentage value.
//
// The combined fee is added to the base before the charge is formatted
// for provider submission. Decimal arithmetic carries the rounding
// choices through that sequence, then the breakdown exposes float
// values for persisted order snapshots. Callers validate finite inputs
// and business amount rules before calculation; the formatting wrapper
// uses the channel currency precision again when producing its string.

// FeeConfig holds the fixed amount and percentage used by checkout fee calculation.
type FeeConfig struct {
	// FixedFee adds a currency amount when positive. Nonpositive values
	// contribute zero, while a positive value is rounded at the selected
	// currency precision before combining fees.
	FixedFee float64 `json:"fixed_fee"`
	// FeeRate supplies a percentage when positive. Nonpositive values add
	// no percentage charge, and positive rates use the rounded base amount
	// with rounding away from zero at the currency precision.
	FeeRate float64 `json:"fee_rate"`
}

// FeeBreakdown retains the rounded checkout base, individual fees, and channel charge.
type FeeBreakdown struct {
	// BaseAmount, FixedFee, FeeRateAmount, FeeAmount, and PayAmount use the
	// selected currency. FeeAmount combines the two fee components, and
	// PayAmount adds that sum to BaseAmount; FeeRate retains the supplied
	// percentage independently of its calculated contribution.
	BaseAmount    float64 `json:"base_amount"`     // currency-rounded base amount
	FixedFee      float64 `json:"fee_fixed"`       // currency-rounded fixed fee
	FeeRate       float64 `json:"fee_rate"`        // supplied fee percentage
	FeeRateAmount float64 `json:"fee_rate_amount"` // percentage fee rounded away from zero
	FeeAmount     float64 `json:"fee_amount"`      // combined currency fees
	PayAmount     float64 `json:"pay_amount"`      // final channel charge
}

// CalculatePayAmountForCurrency returns the formatted channel charge
// using a percentage fee and no fixed fee. Pass finite numeric inputs;
// nonpositive rates add no fee, and currency helpers choose the output
// precision.
func CalculatePayAmountForCurrency(rechargeAmount float64, feeRate float64, currency string) string {
	return CalculatePayAmountWithFeeForCurrency(rechargeAmount, FeeConfig{FeeRate: feeRate}, currency).PayAmountStringForCurrency(currency)
}

// CalculatePayAmountWithFeeForCurrency returns the base, fee components,
// and final charge at currency precision. Callers provide finite inputs
// and perform business amount validation before this decimal arithmetic.
// Nonpositive fee settings contribute zero to their fee component.
//
// The base rounds before percentage calculation, and a positive fixed
// fee rounds independently. The percentage charge rounds away from zero,
// then both fees are combined and added to the base. Keeping those
// rounding steps distinct determines the actual amount sent to the
// channel and the breakdown stored with the order.
func CalculatePayAmountWithFeeForCurrency(baseAmount float64, cfg FeeConfig, currency string) FeeBreakdown {
	fractionDigits := int32(CurrencyMaxFractionDigits(currency))
	// Round the base first so percentage fees use the same amount precision
	// as the channel charge rather than a finer intermediate input.
	amount := decimal.NewFromFloat(baseAmount).Round(fractionDigits)
	fixedFee := decimal.Zero
	if cfg.FixedFee > 0 {
		// The fixed component uses nearest currency rounding independently of
		// the percentage component, whose rounding away from zero can produce a
		// different fractional adjustment.
		fixedFee = decimal.NewFromFloat(cfg.FixedFee).Round(fractionDigits)
	}

	rateAmount := decimal.Zero
	if cfg.FeeRate > 0 {
		rate := decimal.NewFromFloat(cfg.FeeRate)
		// Interpret the configured rate as a percentage and round its charge
		// away from zero after applying it to the rounded base, including small
		// fractional fees.
		rateAmount = amount.Mul(rate).Div(decimal.NewFromInt(100)).RoundUp(fractionDigits)
	}

	// FeeAmount and PayAmount are finalized before conversion to the
	// breakdown's float values, keeping both returned totals based on
	// the same currency-rounded decimal components.
	feeAmount := fixedFee.Add(rateAmount).Round(fractionDigits)
	payAmount := amount.Add(feeAmount).Round(fractionDigits)
	return FeeBreakdown{
		BaseAmount:    decimalToFloat(amount),
		FixedFee:      decimalToFloat(fixedFee),
		FeeRate:       cfg.FeeRate,
		FeeRateAmount: decimalToFloat(rateAmount),
		FeeAmount:     decimalToFloat(feeAmount),
		PayAmount:     decimalToFloat(payAmount),
	}
}

// PayAmountStringForCurrency formats the recorded channel charge at the
// currency's accepted fractional precision. Pass a finite PayAmount;
// this helper applies formatting precision to that field and does not
// recalculate the fee components.
func (b FeeBreakdown) PayAmountStringForCurrency(currency string) string {
	return decimal.NewFromFloat(b.PayAmount).StringFixed(int32(CurrencyMaxFractionDigits(currency)))
}

// decimalToFloat returns the decimal amount as a float for order values.
// The conversion discards its exactness flag, so callers retain currency
// rounding through the fee calculation and string formatter rather than
// assuming binary floats are exact decimals.
func decimalToFloat(v decimal.Decimal) float64 {
	out, _ := v.Float64()
	return out
}
