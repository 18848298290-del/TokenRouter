package payment

import (
	"github.com/shopspring/decimal"
)

// FeeConfig contains the fees applied to a payment method.
type FeeConfig struct {
	FixedFee float64 `json:"fixed_fee"`
	FeeRate  float64 `json:"fee_rate"`
}

// FeeBreakdown contains the base amount, fees, and total charge for an order.
type FeeBreakdown struct {
	BaseAmount    float64 `json:"base_amount"`
	FixedFee      float64 `json:"fee_fixed"`
	FeeRate       float64 `json:"fee_rate"`
	FeeRateAmount float64 `json:"fee_rate_amount"`
	FeeAmount     float64 `json:"fee_amount"`
	PayAmount     float64 `json:"pay_amount"`
}

// CalculatePayAmountForCurrency returns the charge including the percentage fee at currency precision.
func CalculatePayAmountForCurrency(rechargeAmount float64, feeRate float64, currency string) string {
	return CalculatePayAmountWithFeeForCurrency(rechargeAmount, FeeConfig{FeeRate: feeRate}, currency).PayAmountStringForCurrency(currency)
}

// CalculatePayAmountWithFeeForCurrency computes fixed and percentage fees at currency precision.
// Percentage fees round up; the base amount and fixed fee round to the nearest minor unit.
func CalculatePayAmountWithFeeForCurrency(baseAmount float64, cfg FeeConfig, currency string) FeeBreakdown {
	fractionDigits := int32(CurrencyMaxFractionDigits(currency))
	amount := decimal.NewFromFloat(baseAmount).Round(fractionDigits)
	fixedFee := decimal.Zero
	if cfg.FixedFee > 0 {
		fixedFee = decimal.NewFromFloat(cfg.FixedFee).Round(fractionDigits)
	}

	rateAmount := decimal.Zero
	if cfg.FeeRate > 0 {
		rate := decimal.NewFromFloat(cfg.FeeRate)
		rateAmount = amount.Mul(rate).Div(decimal.NewFromInt(100)).RoundUp(fractionDigits)
	}

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

// PayAmountStringForCurrency formats the gateway charge at currency precision.
func (b FeeBreakdown) PayAmountStringForCurrency(currency string) string {
	return decimal.NewFromFloat(b.PayAmount).StringFixed(int32(CurrencyMaxFractionDigits(currency)))
}

func decimalToFloat(v decimal.Decimal) float64 {
	out, _ := v.Float64()
	return out
}
