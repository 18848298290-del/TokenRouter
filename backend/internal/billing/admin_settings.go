package billing

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// These keys store display and default-entitlement settings separately from user funds.
const (
	SettingKeyBalanceIconSVG                  = "balance_icon_svg"
	SettingKeyBalanceUnitName                 = "balance_unit_name"
	SettingKeyBalanceUnitSymbol               = "balance_unit_symbol"
	SettingKeyDefaultBalance                  = "default_balance"
	SettingKeyDefaultSubscriptions            = "default_subscriptions"
	SettingKeyReasoningPointRMBUnitPrice      = "reasoning_point_rmb_unit_price"
	SettingKeySubscriptionExpiryNotifyEnabled = "subscription_expiry_notify_enabled"
	SettingKeyUSDExchangeRate                 = "usd_exchange_rate"
)

// AdminSettings contains balance display, default entitlement, and notification settings.
type AdminSettings struct {
	BalanceIconSVG                  string                       `json:"balance_icon_svg"`
	BalanceLowNotifyEnabled         bool                         `json:"balance_low_notify_enabled"`
	BalanceLowNotifyRechargeURL     string                       `json:"balance_low_notify_recharge_url"`
	BalanceLowNotifyThreshold       float64                      `json:"balance_low_notify_threshold"`
	BalanceUnitName                 string                       `json:"balance_unit_name"`
	BalanceUnitSymbol               string                       `json:"balance_unit_symbol"`
	DefaultBalance                  float64                      `json:"default_balance"`
	DefaultSubscriptions            []DefaultSubscriptionSetting `json:"default_subscriptions"`
	ReasoningPointRMBUnitPrice      float64                      `json:"reasoning_point_rmb_unit_price"`
	SubscriptionExpiryNotifyEnabled bool                         `json:"subscription_expiry_notify_enabled"`
	USDExchangeRate                 float64                      `json:"usd_exchange_rate"`
}

// PrepareAdminSettings normalizes settings in place and encodes amounts with eight decimal places. The caller
// validates plan references.
func PrepareAdminSettings(settings *AdminSettings) (map[string]string, error) {
	updates := map[string]string{}
	settings.BalanceUnitName = strings.TrimSpace(settings.BalanceUnitName)
	settings.BalanceUnitSymbol = strings.TrimSpace(settings.BalanceUnitSymbol)
	settings.BalanceIconSVG = strings.TrimSpace(settings.BalanceIconSVG)
	if settings.BalanceUnitName == "" {
		settings.BalanceUnitName = "USD"
	}
	if settings.BalanceUnitSymbol == "" {
		settings.BalanceUnitSymbol = "$"
	}
	if settings.ReasoningPointRMBUnitPrice < 0 {
		settings.ReasoningPointRMBUnitPrice = 0
	}
	if settings.USDExchangeRate < 0 {
		settings.USDExchangeRate = 0
	}
	updates[SettingKeyDefaultBalance] = strconv.FormatFloat(settings.DefaultBalance, 'f', 8, 64)
	defaultSubsJSON, err := json.Marshal(settings.DefaultSubscriptions)
	if err != nil {
		return nil, fmt.Errorf("marshal default subscriptions: %w", err)
	}
	updates[SettingKeyDefaultSubscriptions] = string(defaultSubsJSON)
	updates[SettingKeyBalanceUnitName] = settings.BalanceUnitName
	updates[SettingKeyBalanceUnitSymbol] = settings.BalanceUnitSymbol
	updates[SettingKeyBalanceIconSVG] = settings.BalanceIconSVG
	updates[SettingKeyReasoningPointRMBUnitPrice] = strconv.FormatFloat(settings.ReasoningPointRMBUnitPrice, 'f', 8, 64)
	updates[SettingKeyUSDExchangeRate] = strconv.FormatFloat(settings.USDExchangeRate, 'f', 8, 64)
	updates[SettingKeyBalanceLowNotifyEnabled] = strconv.FormatBool(settings.BalanceLowNotifyEnabled)
	updates[SettingKeyBalanceLowNotifyThreshold] = strconv.FormatFloat(settings.BalanceLowNotifyThreshold, 'f', 8, 64)
	updates[SettingKeyBalanceLowNotifyRechargeURL] = settings.BalanceLowNotifyRechargeURL
	updates[SettingKeySubscriptionExpiryNotifyEnabled] = strconv.FormatBool(settings.SubscriptionExpiryNotifyEnabled)
	return updates, nil
}
