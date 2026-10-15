package billing

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// admin_settings.go prepares balance display, signup funding, and notification
// settings for persistence.
// Balance display settings name the unit shown to users, while default funds
// and subscriptions determine signup grants. Preparation trims display text
// and encodes numeric values for the settings store; the settings coordinator
// validates referenced plans before persistence.

// Billing setting keys identify display labels, signup grants, conversion
// rates and reminder switches stored by the administrator settings
// participant.
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
	BalanceIconSVG              string  `json:"balance_icon_svg"`                // configured balance icon markup
	BalanceLowNotifyEnabled     bool    `json:"balance_low_notify_enabled"`      // global low-balance alert switch
	BalanceLowNotifyRechargeURL string  `json:"balance_low_notify_recharge_url"` // balance alert recharge link
	BalanceLowNotifyThreshold   float64 `json:"balance_low_notify_threshold"`    // default balance alert amount
	BalanceUnitName             string  `json:"balance_unit_name"`               // displayed balance unit name
	BalanceUnitSymbol           string  `json:"balance_unit_symbol"`             // displayed balance unit symbol
	DefaultBalance              float64 `json:"default_balance"`                 // initial user balance amount
	// DefaultSubscriptions supplies plan selections to signup grants.
	// PrepareAdminSettings serializes this slice as supplied; plan existence and
	// duplicate checks belong to the caller.
	DefaultSubscriptions            []DefaultSubscriptionSetting `json:"default_subscriptions"`              // signup subscription plan selections
	ReasoningPointRMBUnitPrice      float64                      `json:"reasoning_point_rmb_unit_price"`     // RMB per reasoning point
	SubscriptionExpiryNotifyEnabled bool                         `json:"subscription_expiry_notify_enabled"` // subscription expiry reminder switch
	USDExchangeRate                 float64                      `json:"usd_exchange_rate"`                  // RMB per USD
}

// PrepareAdminSettings normalizes the supplied non-nil settings in place and
// returns values for persistence. Empty unit text selects USD and $, negative
// conversion rates become zero, and the caller validates default subscription
// plan references. Amounts are encoded to eight decimal places; notification
// text is retained as supplied.
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
	// Eight decimal places match the persisted settings amount format and keep
	// administrator reads independent of shorter default float formatting.
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
