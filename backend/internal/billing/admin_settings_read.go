package billing

import (
	"context"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	settingvalues "github.com/TokenFlux/TokenRouter/internal/settings"
)

// AdminReadSettings contains balance display, default entitlement, and expiry notification settings.
type AdminReadSettings struct {
	BalanceIconSVG                  string
	BalanceLowNotifyEnabled         bool
	BalanceLowNotifyRechargeURL     string
	BalanceLowNotifyThreshold       float64
	BalanceUnitName                 string
	BalanceUnitSymbol               string
	DefaultBalance                  float64
	DefaultSubscriptions            []DefaultSubscriptionSetting
	ReasoningPointRMBUnitPrice      float64
	SubscriptionExpiryNotifyEnabled bool
	USDExchangeRate                 float64
}

// ReadAdminSettings parses billing settings, using defaultBalance when the configured balance is invalid.
func ReadAdminSettings(settings map[string]string, defaultBalance func() float64) *AdminReadSettings {
	balanceUnitName := strings.TrimSpace(settings[SettingKeyBalanceUnitName])
	if balanceUnitName == "" {
		balanceUnitName = "USD"
	}
	balanceUnitSymbol := strings.TrimSpace(settings[SettingKeyBalanceUnitSymbol])
	if balanceUnitSymbol == "" {
		balanceUnitSymbol = "$"
	}
	result := &AdminReadSettings{}
	result.BalanceUnitName = balanceUnitName
	result.BalanceUnitSymbol = balanceUnitSymbol
	result.BalanceIconSVG = strings.TrimSpace(settings[SettingKeyBalanceIconSVG])
	if balance, err := strconv.ParseFloat(settings[SettingKeyDefaultBalance], 64); err == nil {
		result.DefaultBalance = balance
	} else {
		result.DefaultBalance = defaultBalance()
	}
	if price, err := strconv.ParseFloat(settings[SettingKeyReasoningPointRMBUnitPrice], 64); err == nil && price >= 0 {
		result.ReasoningPointRMBUnitPrice = price
	}
	if rate, err := strconv.ParseFloat(settings[SettingKeyUSDExchangeRate], 64); err == nil && rate >= 0 {
		result.USDExchangeRate = rate
	}
	result.DefaultSubscriptions = ParseDefaultSubscriptions(settings[SettingKeyDefaultSubscriptions])
	result.BalanceLowNotifyEnabled = settings[SettingKeyBalanceLowNotifyEnabled] == "true"
	if v, err := strconv.ParseFloat(settings[SettingKeyBalanceLowNotifyThreshold], 64); err == nil && v >= 0 {
		result.BalanceLowNotifyThreshold = v
	}
	result.BalanceLowNotifyRechargeURL = settings[SettingKeyBalanceLowNotifyRechargeURL]
	result.SubscriptionExpiryNotifyEnabled = !settingvalues.IsExplicitFalse(settings[SettingKeySubscriptionExpiryNotifyEnabled])
	return result
}

// ReadBalanceUnitName trims the configured balance unit and returns USD on read failure or an empty value.
func ReadBalanceUnitName(ctx context.Context, store interface {
	GetValue(context.Context, string) (string, error)
},
) string {
	value := locale.ReadSettingText(ctx, store, SettingKeyBalanceUnitName, "USD")
	value = strings.TrimSpace(value)
	if value == "" {
		return "USD"
	}
	return value
}
