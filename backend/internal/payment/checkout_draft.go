package payment

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// checkout_draft.go builds order snapshots and validates pending-count and
// daily-purchase limits before persistence.
// Checkout stores the credited or plan Amount separately from gateway
// PayAmount and the principal used by its current purchase-limit check. Draft
// construction records buyer, provider, display, billing, and optional plan
// facts before the local order transaction allocates persistent identifiers.
//
// Plan snapshots retain the duration and quota terms that later fulfillment
// reads, and the returned order clone detaches the recognized mutable values
// before persistence. Historical daily-limit input uses paid gateway amounts
// for balance orders and plan amounts for subscriptions; the current principal
// stays in LimitAmount. Pending-order validation applies its configured or
// default count ceiling, while timeout normalization supplies the expiration
// duration for the store.

// BuildCheckoutDraft returns the order snapshots and persistence limits
// prepared for checkout. Supply the buyer and configuration; amount is the
// entitlement or plan amount, limitAmount is the current purchase principal,
// and fee supplies the gateway payment breakdown.
//
// Provider identity and display locale/subject are recorded together with
// optional billing details. A plan contributes its supplied display fields,
// price currency, converted day duration, and quota pointers. An unlabeled price
// records USD when enabled CNY conversion applies. The final Order.Clone
// detaches its recognized mutable snapshot shapes and quota
// pointers before the value is returned. Nonpositive configured timeout
// selects ConfigDefaultOrderTimeoutMin.
func BuildCheckoutDraft(req CreateOrderRequest, user *Buyer, plan *SubscriptionPlan, cfg *PaymentConfig, amount, limitAmount float64, fee FeeBreakdown, sel *InstanceSelection) CheckoutDraft {
	order := Order{
		UserID:           req.UserID,
		UserEmail:        user.Email,
		UserName:         user.Username,
		UserNotes:        NilIfEmpty(user.Notes),
		Amount:           amount,
		PayAmount:        fee.PayAmount,
		FeeRate:          fee.FeeRate,
		FeeFixed:         fee.FixedFee,
		FeeRateAmount:    fee.FeeRateAmount,
		FeeAmount:        fee.FeeAmount,
		PaymentType:      req.PaymentType,
		OrderType:        req.OrderType,
		ClientIP:         req.ClientIP,
		SrcHost:          req.SrcHost,
		SrcURL:           NilIfEmpty(req.SrcURL),
		ProviderSnapshot: BuildPaymentOrderProviderSnapshot(sel, req),
		BillingSnapshot:  BillingInfoSnapshot(req.BillingInfo),
	}
	if order.ProviderSnapshot == nil {
		order.ProviderSnapshot = map[string]any{}
	}
	order.ProviderSnapshot["display_locale"] = locale.Negotiate(req.Locale, locale.Default())
	order.ProviderSnapshot["display_subject"] = PaymentSubject(plan, limitAmount, cfg, sel, req.Locale)
	if sel != nil {
		order.ProviderInstanceID = NilIfEmpty(strings.TrimSpace(sel.InstanceID))
		order.ProviderKey = NilIfEmpty(strings.TrimSpace(sel.ProviderKey))
	}
	// Record the sale terms used for this order before later plan edits. The day
	// conversion fixes duration for fulfillment, and Clone subsequently detaches
	// the quota pointers.
	if plan != nil {
		id := plan.ID
		order.PlanID = &id
		order.PlanSnapshot = billing.SubscriptionPlanSnapshot{
			Locale:          locale.Negotiate(req.Locale, locale.Default()),
			ProductName:     plan.ProductName,
			Name:            plan.Name,
			Price:           plan.Price,
			Currency:        plan.Currency,
			ValidityDays:    billing.ComputeValidityDays(plan.ValidityDays, plan.ValidityUnit),
			DailyLimitUSD:   plan.DailyLimitUSD,
			WeeklyLimitUSD:  plan.WeeklyLimitUSD,
			MonthlyLimitUSD: plan.MonthlyLimitUSD,
		}
		if strings.TrimSpace(order.PlanSnapshot.Currency) == "" &&
			NormalizeSubscriptionUSDToCNYRate(cfg.SubscriptionUSDToCNYRate) > 0 &&
			PaymentOrderCurrency(&order) == "CNY" {
			// Enabled CNY conversion interprets an unlabeled subscription price as USD.
			// Record that unit now so later displays do not depend on current settings.
			order.PlanSnapshot.Currency = "USD"
		}
	}
	timeout := cfg.OrderTimeoutMin
	if timeout <= 0 {
		timeout = ConfigDefaultOrderTimeoutMin
	}
	// Clone recognized mutable order data before publishing the draft so plan
	// quota pointers and assembled snapshot maps are detached from their input
	// objects.
	return CheckoutDraft{
		Order:          *order.Clone(),
		MaxPending:     cfg.MaxPendingOrders,
		DailyLimit:     cfg.DailyLimit,
		LimitAmount:    limitAmount,
		TimeoutMinutes: timeout,
	}
}

// ValidateCheckoutPending rejects a pending/provider-processing count at or
// above the effective ceiling. A nonpositive ceiling selects
// ConfigDefaultMaxPendingOrders, and the error metadata reports that effective
// maximum to the checkout caller.
func ValidateCheckoutPending(count, max int) error {
	if max <= 0 {
		max = ConfigDefaultMaxPendingOrders
	}
	if count >= max {
		return apperror.TooManyRequests("TOO_MANY_PENDING", "too_many_pending").WithMetadata(map[string]string{"max": strconv.Itoa(max)})
	}
	return nil
}

// ValidateCheckoutDaily compares the supplied purchase amount with prior paid
// purchase values and the ceiling. Supply nonnil order entries; CreateCheckout
// invokes this check when its daily ceiling is positive.
//
// Balance history contributes PayAmount, including fees, while other order
// types contribute Amount. The current amount comes from
// CheckoutDraft.LimitAmount, so balance credit multipliers and subscription
// gateway conversion do not replace that current principal here. The
// comparison uses the unrounded floating total; exceeding the ceiling returns
// remaining allowance clamped at zero and formatted to two decimals.
func ValidateCheckoutDaily(orders []*Order, amount, limit float64) error {
	var used float64
	// Prior balance purchases use their paid gateway amount, while subscriptions
	// use their recorded plan amount. Compare that mixed historical basis with
	// the current draft principal.
	for _, o := range orders {
		if o.OrderType == OrderTypeBalance {
			used += o.PayAmount
			continue
		}
		used += o.Amount
	}
	if used+amount > limit {
		// Round the displayed remainder; compare the unrounded accumulated amount.
		return apperror.TooManyRequests("DAILY_LIMIT_EXCEEDED", "daily_limit_exceeded").WithMetadata(map[string]string{"remaining": fmt.Sprintf("%.2f", math.Max(0, limit-used))})
	}
	return nil
}

// RefundStringValue returns the pointed-to string, or empty text for nil.
func RefundStringValue(v *string) string { return refundStringValue(v) }
