package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// visible_settings_participant.go normalizes visible payment sources and
// prepares supplied fields for combined settings updates.
// Alipay and WeChat source labels are normalized independently of their stored
// visibility flags. Source validation currently ignores the enabled argument,
// while administrator reads interpret exact true strings and normalize
// supported aliases for display.
//
// The visible participant prepares a fresh decoded value and retains writes by
// request key presence. It removes absent keys after normalization rather than
// reading prior settings, so partial requests keep their omitted fields
// outside persistence. A supplied null can retain a decoded zero value as a
// requested key. Standalone preparation mutates source strings in order and
// can leave the earlier normalization visible if a later source fails; the
// coordinator owns persistence of the returned values.

// VisibleMethodSettings retains supplied Alipay/WeChat source labels and
// visibility flags.
type VisibleMethodSettings struct {
	// saved Alipay source label
	PaymentVisibleMethodAlipaySource string `json:"payment_visible_method_alipay_source"`
	// saved WeChat source label
	PaymentVisibleMethodWxpaySource string `json:"payment_visible_method_wxpay_source"`
	// saved Alipay visibility flag
	PaymentVisibleMethodAlipayEnabled bool `json:"payment_visible_method_alipay_enabled"`
	// saved WeChat visibility flag
	PaymentVisibleMethodWxpayEnabled bool `json:"payment_visible_method_wxpay_enabled"`
}

// AdminReadSettings carries normalized saved Alipay/WeChat settings for
// administrator display.
type AdminReadSettings struct {
	PaymentVisibleMethodAlipayEnabled bool   // saved Alipay visibility flag
	PaymentVisibleMethodAlipaySource  string // saved Alipay source label
	PaymentVisibleMethodWxpayEnabled  bool   // saved WeChat visibility flag
	PaymentVisibleMethodWxpaySource   string // saved WeChat source label
}

// PrepareVisibleMethodSettings normalizes a nonnil value's source strings in
// place and returns all four setting values. Alipay normalization occurs
// first, so a later WeChat error can leave its earlier mutation; the source
// validator currently ignores enabled flags.
func PrepareVisibleMethodSettings(value *VisibleMethodSettings) (map[string]string, error) {
	var err error
	value.PaymentVisibleMethodAlipaySource, err = NormalizeVisibleMethodSettingSource("alipay", value.PaymentVisibleMethodAlipaySource, value.PaymentVisibleMethodAlipayEnabled)
	if err != nil {
		return nil, err
	}
	value.PaymentVisibleMethodWxpaySource, err = NormalizeVisibleMethodSettingSource("wxpay", value.PaymentVisibleMethodWxpaySource, value.PaymentVisibleMethodWxpayEnabled)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		SettingPaymentVisibleMethodAlipaySource:  value.PaymentVisibleMethodAlipaySource,
		SettingPaymentVisibleMethodWxpaySource:   value.PaymentVisibleMethodWxpaySource,
		SettingPaymentVisibleMethodAlipayEnabled: strconv.FormatBool(value.PaymentVisibleMethodAlipayEnabled),
		SettingPaymentVisibleMethodWxpayEnabled:  strconv.FormatBool(value.PaymentVisibleMethodWxpayEnabled),
	}, nil
}

// VisibleSettingsParticipant registers the four visible-method keys and
// prepares only those present in its input. It decodes a fresh value,
// normalizes both source fields, then removes each absent key from the
// returned values.
//
// The supplied prior-settings map is unused, so absence is preserved through
// key membership rather than merging old state into validation. A present null
// can decode to a zero value and remain a requested key. Empty input returns
// no change, and this participant returns no runtime Apply callback;
// persistence belongs to the combined settings coordinator.
func VisibleSettingsParticipant() settings.Participant {
	keys := []string{SettingPaymentVisibleMethodAlipaySource, SettingPaymentVisibleMethodWxpaySource, SettingPaymentVisibleMethodAlipayEnabled, SettingPaymentVisibleMethodWxpayEnabled}
	return settings.Participant{Module: "payment-visible-methods", Fields: keys, Keys: keys, Prepare: func(_ context.Context, input settings.Fields, _ map[string]string) (settings.PreparedChange, error) {
		if len(input) == 0 {
			return settings.PreparedChange{}, nil
		}
		raw, err := json.Marshal(input)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		var value VisibleMethodSettings
		if err = json.Unmarshal(raw, &value); err != nil {
			return settings.PreparedChange{}, err
		}
		values, err := PrepareVisibleMethodSettings(&value)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		// Normalization produced all four values from a fresh struct. Remove keys
		// absent from the request so omitted sources or flags do not become writes
		// of their zero values.
		for key := range values {
			if _, ok := input[key]; !ok {
				delete(values, key)
			}
		}
		return settings.PreparedChange{Values: values}, nil
	}}
}

// ReadAdminSettings returns a new display value parsed from saved string
// settings. Sources use alias normalization and invalid text becomes empty,
// while enabled flags are true only for the exact stored text true.
func ReadAdminSettings(settings map[string]string) *AdminReadSettings {
	result := &AdminReadSettings{}

	result.PaymentVisibleMethodAlipaySource = NormalizeVisibleMethodSource("alipay", settings[SettingPaymentVisibleMethodAlipaySource])
	result.PaymentVisibleMethodWxpaySource = NormalizeVisibleMethodSource("wxpay", settings[SettingPaymentVisibleMethodWxpaySource])
	result.PaymentVisibleMethodAlipayEnabled = settings[SettingPaymentVisibleMethodAlipayEnabled] == "true"
	result.PaymentVisibleMethodWxpayEnabled = settings[SettingPaymentVisibleMethodWxpayEnabled] == "true"
	return result
}

// NormalizeVisibleMethodSettingSource returns the accepted canonical source or
// empty for trimmed empty text. It rejects unsupported nonempty sources for
// the method; enabled is currently ignored, so disabling a flag does not
// bypass source validation.
func NormalizeVisibleMethodSettingSource(method, source string, enabled bool) (string, error) {
	_ = enabled
	source = strings.TrimSpace(source)
	if source == "" {
		return "", nil
	}

	normalized := NormalizeVisibleMethodSource(method, source)
	if normalized == "" {
		return "", infraerrors.BadRequest(
			"INVALID_PAYMENT_VISIBLE_METHOD_SOURCE",
			fmt.Sprintf("%s source must be one of the supported payment providers", method),
		)
	}
	return normalized, nil
}
