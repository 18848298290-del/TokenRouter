package composite

import "github.com/TokenFlux/TokenRouter/internal/billing"

// BillingAdminSettings extracts billing settings from the snapshot.
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

// ApplyBillingAdminSettings copies billing settings into the snapshot.
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

// ApplyBillingAdminReadSettings copies balance display, notification, grant, and exchange-rate settings into
// the snapshot.
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
