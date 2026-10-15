package payment

import (
	"math"
	"strings"
)

// fulfillment_values.go resolves historical notification identities and
// derives purchased reasoning-point quantities for rebates.
// Payment recovery compares notifications with historical channel
// identity even when the registry's current payment-type binding changes.
// The order snapshot can supply that recorded provider key, while an
// explicit resolved instance key retains the delegate's highest
// precedence. Missing historical data uses the remaining order and
// registry compatibility choices.
//
// Affiliate rebates also need the purchased reasoning-point base,
// which differs from the channel charge. Balance orders use credited
// Amount, and subscription orders choose one valid recorded quota ceiling
// in monthly, weekly, daily order. Invoice metadata helpers separately
// prepare transition fields from notification strings; persistence and
// fulfillment eligibility remain with the transition callers.

// ExpectedNotificationProviderKeyForOrder returns the expected notification
// implementation from recorded order identity and an optional instance key.
// Nil order input returns the trimmed instance key.
//
// A nonempty snapshot key replaces the order column before delegation, so
// a changed registry binding cannot replace either recorded key. The delegate
// prefers a nonempty instance key, then the recorded key, registry binding,
// and payment type. A supplied instance key therefore determines the result
// even when the order records a different provider.
func ExpectedNotificationProviderKeyForOrder(registry *Registry, order *Order, instanceProviderKey string) string {
	if order == nil {
		return strings.TrimSpace(instanceProviderKey)
	}

	orderProviderKey := refundStringValue(order.ProviderKey)
	// A partial snapshot can contain only a version or other attributes. Keep
	// the order column until the snapshot supplies the provider identity itself.
	if snapshot := PsOrderProviderSnapshot(order); snapshot != nil && snapshot.ProviderKey != "" {
		orderProviderKey = snapshot.ProviderKey
	}

	return ExpectedNotificationProviderKey(registry, order.PaymentType, orderProviderKey, instanceProviderKey)
}

// IsRefundStatus reports whether s is a recognized refund lifecycle state.
func IsRefundStatus(s string) bool {
	switch s {
	case OrderStatusRefundRequested, OrderStatusRefunding, OrderStatusRefundPending, OrderStatusPartiallyRefunded, OrderStatusRefunded, OrderStatusRefundFailed:
		return true
	}
	return false
}

// OrderErrorMessage returns diagnostic text for a failure cause or an
// empty string for nil. The error's own message is retained for order
// transition and audit callers; this helper adds no wrapper or
// classification.
func OrderErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// OrderPurchasedReasoningPoints returns a positive finite purchase
// base for affiliate rebates and a flag indicating that it exists.
// Balance orders use credited Amount, so the external channel payment
// and fees do not determine that base.
//
// Subscription orders inspect recorded monthly, weekly, then daily
// ceilings and select the first positive finite value. Missing, zero,
// negative, or nonfinite ceilings are skipped; the periods are not
// summed. Nil orders, unsupported order types, and purchases without
// a usable base return zero with false.
func OrderPurchasedReasoningPoints(order *Order) (float64, bool) {
	if order == nil {
		return 0, false
	}

	var points float64
	switch order.OrderType {
	case OrderTypeBalance:
		points = order.Amount
	case OrderTypeSubscription:
		limits := []*float64{
			order.PlanSnapshot.MonthlyLimitUSD,
			order.PlanSnapshot.WeeklyLimitUSD,
			order.PlanSnapshot.DailyLimitUSD,
		}
		// Quota windows overlap, so the rebate base uses one recorded ceiling
		// in its stated priority order. Skip unusable ceilings and continue
		// looking for the next valid period.
		for _, limit := range limits {
			if limit == nil || *limit <= 0 || math.IsNaN(*limit) || math.IsInf(*limit, 0) {
				continue
			}
			return *limit, true
		}
		return 0, false
	default:
		return 0, false
	}

	if points <= 0 || math.IsNaN(points) || math.IsInf(points, 0) {
		return 0, false
	}
	return points, true
}

// FirstNonEmpty returns the first nonblank candidate after trimming
// whitespace, or empty when every candidate is blank. Candidate order
// sets the caller's preference, so the returned display text retains
// that selection after normalization.
func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// ApplyInvoiceMetadata fills a nonnil transition with trimmed invoice
// ID, hosted link, PDF link, and status from metadata. Missing keys
// replace those transition fields with empty strings; the repository's
// transition rules determine which values are persisted.
func ApplyInvoiceMetadata(change *OrderTransition, metadata map[string]string) {
	change.InvoiceID = strings.TrimSpace(metadata["invoice_id"])
	change.InvoiceURL = strings.TrimSpace(metadata["invoice_url"])
	change.InvoicePDF = strings.TrimSpace(metadata["invoice_pdf"])
	change.InvoiceStatus = strings.TrimSpace(metadata["invoice_status"])
}
