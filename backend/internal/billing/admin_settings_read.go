package billing

import (
	"context"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	settingvalues "github.com/TokenFlux/TokenRouter/internal/settings"
)

// Stored billing settings can omit display labels or contain invalid numeric
// text. Administrator reads construct a typed snapshot with currency labels,
// signup grants and alert switches, selecting defaults separately for each
// setting.

// AdminReadSettings contains balance display, default entitlement, and expiry notification settings.
type AdminReadSettings struct {
	BalanceIconSVG                  string                       // configured balance icon markup
	BalanceLowNotifyEnabled         bool                         // global low-balance alert switch
	BalanceLowNotifyRechargeURL     string                       // balance alert recharge link
	BalanceLowNotifyThreshold       float64                      // default balance alert amount
	BalanceUnitName                 string                       // displayed balance unit name
	BalanceUnitSymbol               string                       // displayed balance unit symbol
	DefaultBalance                  float64                      // initial user balance amount
	DefaultSubscriptions            []DefaultSubscriptionSetting // signup subscription plan selections
	ReasoningPointRMBUnitPrice      float64                      // RMB per reasoning point
	SubscriptionExpiryNotifyEnabled bool                         // subscription expiry reminder switch
	USDExchangeRate                 float64                      // RMB per USD
}

// ReadAdminSettings parses a stored settings snapshot into an administrator
// view. Invalid default-balance text invokes the required defaultBalance
// callback; empty unit labels select USD and $, invalid nonnegative rates
// remain zero, and expiry reminders stay enabled unless the switch is
// recognized as false.
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
		// A missing or malformed balance setting uses the configuration callback so
		// administrator reads show the same initial grant used by signup.
		result.DefaultBalance = defaultBalance()
	}
	// Malformed or negative conversion settings leave both numeric fields at
	// zero; administrator reads expose these values for correction before their
	// marketplace use.
	if price, err := strconv.ParseFloat(settings[SettingKeyReasoningPointRMBUnitPrice], 64); err == nil && price >= 0 {
		result.ReasoningPointRMBUnitPrice = price
	}
	if rate, err := strconv.ParseFloat(settings[SettingKeyUSDExchangeRate], 64); err == nil && rate >= 0 {
		result.USDExchangeRate = rate
	}
	result.DefaultSubscriptions = ParseDefaultSubscriptions(settings[SettingKeyDefaultSubscriptions])
	result.BalanceLowNotifyEnabled = settings[SettingKeyBalanceLowNotifyEnabled] == "true"
	// An invalid or negative saved alert threshold leaves the zero value, which
	// the notification service treats as a disabled threshold.
	if v, err := strconv.ParseFloat(settings[SettingKeyBalanceLowNotifyThreshold], 64); err == nil && v >= 0 {
		result.BalanceLowNotifyThreshold = v
	}
	result.BalanceLowNotifyRechargeURL = settings[SettingKeyBalanceLowNotifyRechargeURL]
	result.SubscriptionExpiryNotifyEnabled = !settingvalues.IsExplicitFalse(settings[SettingKeySubscriptionExpiryNotifyEnabled])
	return result
}

// ReadBalanceUnitName reads the plain and localized balance unit using the
// context language, then trims the selected text. The store must be callable;
// read errors are discarded while companion text can still be selected.
// Missing usable text or an empty trimmed label yields USD.
func ReadBalanceUnitName(ctx context.Context, store interface {
	// GetValue returns setting text to the localized balance-label reader. The
	// locale reader discards errors while keeping returned text for its language
	// and default selection.
	GetValue(context.Context, string) (string, error)
},
) string {
	value := locale.ReadSettingText(ctx, store, SettingKeyBalanceUnitName, "USD")
	value = strings.TrimSpace(value)
	if value == "" {
		// Whitespace-only labels become empty after trimming; use the administrator
		// display default so user balance text still names a unit.
		return "USD"
	}
	return value
}
