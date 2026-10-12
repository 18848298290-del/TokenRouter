package payment

import (
	"math"

	"github.com/shopspring/decimal"
)

const (
	DefaultBalanceRechargeMultiplier = 1.0
	ProviderAmountTolerance          = 0.01
)

func NormalizeBalanceRechargeMultiplier(multiplier float64) float64 {
	if math.IsNaN(multiplier) || math.IsInf(multiplier, 0) || multiplier <= 0 {
		return DefaultBalanceRechargeMultiplier
	}
	return multiplier
}

// NormalizeSubscriptionUSDToCNYRate maps invalid rates to zero, disabling conversion.
// Zero is valid: subscriptions charge the plan price directly when conversion is disabled.
func NormalizeSubscriptionUSDToCNYRate(rate float64) float64 {
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
		return 0
	}
	return rate
}

func CalculateCreditedBalance(paymentAmount, multiplier float64) float64 {
	return decimal.NewFromFloat(paymentAmount).
		Mul(decimal.NewFromFloat(NormalizeBalanceRechargeMultiplier(multiplier))).
		Round(2).
		InexactFloat64()
}

func CalculateGatewayRefundAmount(orderAmount, payAmount, refundAmount float64, currency string) float64 {
	if orderAmount <= 0 || payAmount <= 0 || refundAmount <= 0 {
		return 0
	}
	fractionDigits := int32(CurrencyMaxFractionDigits(currency))
	if math.Abs(refundAmount-orderAmount) <= PaymentAmountToleranceForCurrency(currency) {
		return decimal.NewFromFloat(payAmount).Round(fractionDigits).InexactFloat64()
	}
	return decimal.NewFromFloat(payAmount).
		Mul(decimal.NewFromFloat(refundAmount)).
		Div(decimal.NewFromFloat(orderAmount)).
		Round(fractionDigits).
		InexactFloat64()
}

func PaymentAmountToleranceForCurrency(currency string) float64 {
	minorUnit := CurrencyMinorUnit(currency)
	if minorUnit <= 2 {
		return ProviderAmountTolerance
	}
	return math.Pow10(-minorUnit) / 2
}
