package payment

import (
	"context"
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Composite settings use payment-prefixed HTTP names while payment validation
// accepts its own serialized request fields. Each mapping pair carries a
// present raw value whose text is not exactly null into that typed request, so pointer and collection
// omissions can retain existing settings.
//
// Preparation returns validated values and a provider refresh callback for the
// settings coordinator. UpdateSession owns the combined persistence step and
// invokes application callbacks after marking the update committed. A refresh
// failure therefore remains an application error for already stored
// configuration; this participant cannot claim a settings rollback. With no
// selected input it returns an empty change, and the declared field/key lists
// define which settings belong to payment.

// paymentCompositeFields pairs composite input names with serialized
// UpdatePaymentConfigRequest field names. input and target select each present
// nonnull raw value before typed validation, keeping omitted fields outside
// the prepared setting batch.
var paymentCompositeFields = []struct {
	// input: composite input field name; target: payment request JSON name
	input, target string
}{
	{"payment_enabled", "enabled"},
	{"payment_min_amount", "min_amount"},
	{"payment_max_amount", "max_amount"},
	{"payment_daily_limit", "daily_limit"},
	{"payment_order_timeout_minutes", "order_timeout_minutes"},
	{"payment_max_pending_orders", "max_pending_orders"},
	{"payment_enabled_types", "enabled_payment_types"},
	{"payment_balance_disabled", "balance_disabled"},
	{"payment_balance_recharge_multiplier", "balance_recharge_multiplier"},
	{"payment_subscription_usd_to_cny_rate", "subscription_usd_to_cny_rate"},
	{"payment_recharge_fee_rate", "recharge_fee_rate"},
	{"payment_method_fees", "method_fees"},
	{"payment_load_balance_strategy", "load_balance_strategy"},
	{"payment_product_name_prefix", "product_name_prefix"},
	{"payment_product_name_suffix", "product_name_suffix"},
	{"payment_help_image_url", "help_image_url"},
	{"payment_help_text", "help_text"},
	{"payment_cancel_rate_limit_enabled", "cancel_rate_limit_enabled"},
	{"payment_cancel_rate_limit_max", "cancel_rate_limit_max"},
	{"payment_cancel_rate_limit_window", "cancel_rate_limit_window"},
	{"payment_cancel_rate_limit_unit", "cancel_rate_limit_unit"},
	{"payment_cancel_rate_limit_window_mode", "cancel_rate_limit_window_mode"},
	{"payment_alipay_force_qrcode", "alipay_force_qrcode"},
	{"payment_alipay_mobile_precreate_deep_link", "alipay_mobile_precreate_deep_link"},
}

// SettingsParticipant registers payment-owned composite fields and prepares
// their supplied setting values. Its callback excludes exact raw null text,
// decodes the renamed JSON fields into UpdatePaymentConfigRequest, and
// delegates payment validation.
//
// No selected input yields an empty prepared change. Other accepted input
// returns Values plus the supplied refresh callback, leaving persistence and
// post-commit invocation to settings.UpdateSession. The session marks
// persistence committed before Apply, so refresh failure reports an already
// stored update; this participant performs no local rollback or retry of that
// application callback.
func SettingsParticipant(refresh func(context.Context) error) settings.Participant {
	fields := make([]string, len(paymentCompositeFields))
	for i, field := range paymentCompositeFields {
		fields[i] = field.input
	}
	return settings.Participant{Module: "payment", Fields: fields, Keys: []string{
		SettingPaymentEnabled,
		SettingMinRechargeAmount,
		SettingMaxRechargeAmount,
		SettingDailyRechargeLimit,
		SettingOrderTimeoutMinutes,
		SettingMaxPendingOrders,
		SettingEnabledPaymentTypes,
		SettingBalancePayDisabled,
		SettingBalanceRechargeMult,
		SettingSubscriptionUSDToCNYRate,
		SettingRechargeFeeRate,
		SettingPaymentMethodFees,
		SettingLoadBalanceStrategy,
		SettingProductNamePrefix,
		SettingProductNameSuffix,
		SettingHelpImageURL,
		SettingHelpText,
		SettingCancelRateLimitOn,
		SettingCancelRateLimitMax,
		SettingCancelWindowSize,
		SettingCancelWindowUnit,
		SettingCancelWindowMode,
		SettingAlipayForceQRCode,
		SettingAlipayMobilePrecreateDeepLink,
	}, Prepare: func(ctx context.Context, input settings.Fields, _ map[string]string) (settings.PreparedChange, error) {
		projected := map[string]json.RawMessage{}
		// Exclude absent payment inputs and exact raw null text while renaming their JSON keys.
		// The typed request then preserves pointer and nil-collection omission for
		// PreparePaymentConfig's partial update rules.
		for _, field := range paymentCompositeFields {
			if value, ok := input[field.input]; ok && string(value) != "null" {
				projected[field.target] = value
			}
		}
		if len(projected) == 0 {
			return settings.PreparedChange{}, nil
		}
		encoded, err := json.Marshal(projected)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		var request UpdatePaymentConfigRequest
		if err = json.Unmarshal(encoded, &request); err != nil {
			return settings.PreparedChange{}, err
		}
		values, err := PreparePaymentConfig(request)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		// Return refresh as deferred application alongside prepared values.
		// UpdateSession persists the combined batch before invoking callbacks, so a
		// refresh error leaves that stored configuration in place.
		return settings.PreparedChange{Values: values, Apply: refresh}, nil
	}}
}
