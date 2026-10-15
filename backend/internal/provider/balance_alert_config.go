package provider

import (
	"bytes"
	"encoding/json"
	"math"
	"net/mail"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// balance_alert_config.go validates administrator choices for periodic wallet
// balance emails. Thresholds compare the upstream primary remaining amount as
// reported, while the reported unit is presentation data. Missing configuration
// leaves monitoring disabled so existing providers do not begin sending mail.

// Balance alert settings share one Extra key across creation, editing, and
// polling; interval bounds keep automatic queries at least five minutes apart.
const UpstreamBalanceAlertExtraKey = "upstream_balance_alert"

// ErrBalanceAlertConfigInvalid identifies invalid monitor settings in administrator requests.
var ErrBalanceAlertConfigInvalid = apperror.BadRequest("UPSTREAM_BALANCE_ALERT_CONFIG_INVALID", "invalid upstream balance alert configuration")

// BalanceAlertConfig contains the independently configured reminder policy for one provider.
type BalanceAlertConfig struct {
	Enabled         bool    `json:"enabled"`          // periodic reminder switch
	Threshold       float64 `json:"threshold"`        // primary balance cutoff
	IntervalMinutes int     `json:"interval_minutes"` // minutes
	RecipientEmail  string  `json:"recipient_email"`  // designated reminder recipient
}

// BalanceAlertConfiguration reads and validates the provider's reminder settings.
// Missing settings use disabled defaults. Enabled settings require a wallet-capable
// query; disabling upstream queries suspends monitoring while retaining reminder choices.
func BalanceAlertConfiguration(p *Record) (BalanceAlertConfig, error) {
	c := BalanceAlertConfig{Threshold: 1000, IntervalMinutes: 60}
	if p == nil {
		return c, nil
	}
	raw, ok := p.Extra[UpstreamBalanceAlertExtraKey]
	if !ok {
		return c, nil
	}
	payload, err := json.Marshal(raw)
	if err != nil || raw == nil {
		return c, ErrBalanceAlertConfigInvalid
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return c, ErrBalanceAlertConfigInvalid
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return c, ErrBalanceAlertConfigInvalid
		}
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, ErrBalanceAlertConfigInvalid
	}
	c.RecipientEmail = strings.TrimSpace(c.RecipientEmail)
	if math.IsNaN(c.Threshold) || math.IsInf(c.Threshold, 0) || c.Threshold < 0 || c.IntervalMinutes < 5 || c.IntervalMinutes > 1440 {
		return c, ErrBalanceAlertConfigInvalid
	}
	if c.Enabled {
		address, err := mail.ParseAddress(c.RecipientEmail)
		if err != nil || address.Address != c.RecipientEmail || strings.ContainsAny(c.RecipientEmail, "\r\n") || !BalanceAlertSupported(p) {
			return c, ErrBalanceAlertConfigInvalid
		}
	}
	return c, nil
}

// BalanceAlertSupported reports whether the provider has a wallet-capable adapter.
// Query enablement and provider status are checked separately by the worker so
// temporarily disabling a provider can retain its validated reminder configuration.
func BalanceAlertSupported(p *Record) bool {
	if p == nil || p.Type != ProviderTypeAPIKey || p.IsCodingPlan() {
		return false
	}
	c, err := EffectiveUpstreamUsageConfig(p)
	if err != nil {
		return false
	}
	if p.IsCNProvider() {
		c.Adapter = CNUpstreamUsageAdapterName(p)
	}
	switch c.Adapter {
	case UpstreamUsageAdapterSub2API, UpstreamUsageAdapterNewAPI, UpstreamUsageAdapterZivv, UpstreamUsageAdapterKimiBalance, UpstreamUsageAdapterDeepseekBalance:
		return true
	default:
		return false
	}
}
