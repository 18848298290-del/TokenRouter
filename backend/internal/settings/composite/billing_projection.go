package composite

import "github.com/TokenFlux/TokenRouter/internal/billing"

// billing_projection.go transfers billing administrator values between module
// settings and the combined snapshot.
// Billing display, default grants, and notification settings share one
// Snapshot during a combined administrator update. The billing value struct
// selects those fields for module preparation; successful normalization writes
// them back, while its default-subscription slice remains shared across these
// transfers.

// BillingAdminSettings returns billing display, grant, and notification values
// for module preparation. DefaultSubscriptions keeps the Snapshot's slice storage,
// so callers leave its entries stable while preparation or another consumer can
// read the same selection.
func (s *Snapshot) BillingAdminSettings() billing.AdminSettings {
	return billing.AdminSettings{
		BalanceIconSVG:                  s.BalanceIconSVG,
		BalanceLowNotifyEnabled:         s.BalanceLowNotifyEnabled,
		BalanceLowNotifyRechargeURL:     s.BalanceLowNotifyRechargeURL,
		BalanceLowNotifyThreshold:       s.BalanceLowNotifyThreshold,
		BalanceUnitName:                 s.BalanceUnitName,
		BalanceUnitSymbol:               s.BalanceUnitSymbol,
		DefaultBalance:                  s.DefaultBalance,
		DefaultSubscriptions:            s.DefaultSubscriptions,
		ReasoningPointRMBUnitPrice:      s.ReasoningPointRMBUnitPrice,
		SubscriptionExpiryNotifyEnabled: s.SubscriptionExpiryNotifyEnabled,
		USDExchangeRate:                 s.USDExchangeRate,
	}
}

// ApplyBillingAdminSettings writes normalized display, grant, and notification
// values back into the Snapshot. DefaultSubscriptions retains the supplied slice;
// its caller keeps that list stable through the later encoded batch preparation.
func (s *Snapshot) ApplyBillingAdminSettings(value billing.AdminSettings) {
	s.BalanceIconSVG = value.BalanceIconSVG
	s.BalanceLowNotifyEnabled = value.BalanceLowNotifyEnabled
	s.BalanceLowNotifyRechargeURL = value.BalanceLowNotifyRechargeURL
	s.BalanceLowNotifyThreshold = value.BalanceLowNotifyThreshold
	s.BalanceUnitName = value.BalanceUnitName
	s.BalanceUnitSymbol = value.BalanceUnitSymbol
	s.DefaultBalance = value.DefaultBalance
	s.DefaultSubscriptions = value.DefaultSubscriptions
	s.ReasoningPointRMBUnitPrice = value.ReasoningPointRMBUnitPrice
	s.SubscriptionExpiryNotifyEnabled = value.SubscriptionExpiryNotifyEnabled
	s.USDExchangeRate = value.USDExchangeRate
}

// ApplyBillingAdminReadSettings transfers the billing reader's selected balance
// labels, grants, and notification controls. A nonnil result is required, and its
// default-subscription slice remains shared with the Snapshot for later response
// and preparation consumers.
func (s *Snapshot) ApplyBillingAdminReadSettings(value *billing.AdminReadSettings) {
	s.BalanceIconSVG = value.BalanceIconSVG
	s.BalanceLowNotifyEnabled = value.BalanceLowNotifyEnabled
	s.BalanceLowNotifyRechargeURL = value.BalanceLowNotifyRechargeURL
	s.BalanceLowNotifyThreshold = value.BalanceLowNotifyThreshold
	s.BalanceUnitName = value.BalanceUnitName
	s.BalanceUnitSymbol = value.BalanceUnitSymbol
	s.DefaultBalance = value.DefaultBalance
	s.DefaultSubscriptions = value.DefaultSubscriptions
	s.ReasoningPointRMBUnitPrice = value.ReasoningPointRMBUnitPrice
	s.SubscriptionExpiryNotifyEnabled = value.SubscriptionExpiryNotifyEnabled
	s.USDExchangeRate = value.USDExchangeRate
}
