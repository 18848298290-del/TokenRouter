package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/identity/contact"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// admin_settings.go converts provider notification and scheduling settings
// between typed administrator inputs and stored strings.
// Notification recipients retain their delivery flags through the contact
// encoder, while scheduling thresholds are validated before a supplied map is
// serialized. Reads expose default thresholds when stored threshold JSON cannot
// be decoded.
//
// The settings participant receives a partial field map. Decoding that map into
// AdminSettings introduces zero values for omitted fields, so preparation
// filters the generated values back to the original key set. This keeps a
// threshold-only write from disabling notifications or replacing recipient
// addresses. Storage applies the prepared values after this module finishes
// validation.

const (
	// Notification keys store the enablement switch and recipient records consumed
	// by provider quota notification settings and administrator reads.
	SettingKeyProviderQuotaNotifyEnabled = "provider_quota_notify_enabled"
	SettingKeyProviderQuotaNotifyEmails  = "provider_quota_notify_emails"
)

// AdminSettings contains typed provider notification and scheduling values for
// settings preparation.
type AdminSettings struct {
	ProviderQuotaNotifyEnabled bool            `json:"provider_quota_notify_enabled"` // quota notification enablement
	ProviderQuotaNotifyEmails  []contact.Entry `json:"provider_quota_notify_emails"`  // notification recipients and flags
	// ProviderSchedulingThresholds selects supplied platform percentages. Nil omits
	// the stored threshold key; an empty map writes the default platform values.
	ProviderSchedulingThresholds map[string]int `json:"provider_scheduling_thresholds"`
}

// AdminReadSettings carries decoded provider settings displayed by the
// administration page.
type AdminReadSettings struct {
	ProviderQuotaNotifyEmails    []contact.Entry // decoded recipients and flags
	ProviderQuotaNotifyEnabled   bool            // stored quota notification switch
	ProviderSchedulingThresholds map[string]int  // platform scheduling usage percentages
}

// PrepareAdminSettings encodes recipients and notification enablement for
// settings storage. A nil threshold map omits that key; a supplied map must
// contain supported platforms and percentages from one through one hundred.
func PrepareAdminSettings(value AdminSettings) (map[string]string, error) {
	values := map[string]string{SettingKeyProviderQuotaNotifyEnabled: strconv.FormatBool(value.ProviderQuotaNotifyEnabled), SettingKeyProviderQuotaNotifyEmails: contact.MarshalNotifyEmails(value.ProviderQuotaNotifyEmails)}
	if value.ProviderSchedulingThresholds != nil {
		normalized, err := ValidateAndNormalizeProviderSchedulingThresholds(value.ProviderSchedulingThresholds)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(normalized)
		if err != nil {
			return nil, fmt.Errorf("marshal provider scheduling thresholds: %w", err)
		}
		values[SettingKeyProviderSchedulingThresholds] = string(raw)
	}
	return values, nil
}

// ReadAdminSettings decodes stored notification and scheduling values for
// administration. Enablement requires the exact string true; empty recipients
// become an allocated slice, and undecodable threshold JSON retains the
// platform defaults.
func ReadAdminSettings(settings map[string]string) *AdminReadSettings {
	result := &AdminReadSettings{}

	result.ProviderQuotaNotifyEnabled = settings[SettingKeyProviderQuotaNotifyEnabled] == "true"
	if raw := strings.TrimSpace(settings[SettingKeyProviderQuotaNotifyEmails]); raw != "" {
		result.ProviderQuotaNotifyEmails = contact.ParseNotifyEmails(raw)
	}
	if result.ProviderQuotaNotifyEmails == nil {
		result.ProviderQuotaNotifyEmails = []contact.Entry{}
	}
	result.ProviderSchedulingThresholds = DefaultProviderSchedulingThresholds()
	if raw := strings.TrimSpace(settings[SettingKeyProviderSchedulingThresholds]); raw != "" {
		// Malformed stored JSON leaves the defaults available to the
		// administrator; recording the parse error keeps that recovery choice
		// visible during diagnosis.
		if thresholds, err := ParseProviderSchedulingThresholdsSetting(raw); err != nil {
			slog.Warn("[Setting] parseSettings: unmarshal provider_scheduling_thresholds failed", "error", err)
		} else {
			result.ProviderSchedulingThresholds = thresholds
		}
	}
	return result
}

// SettingsParticipant returns the provider module's partial-settings
// preparation callback. The callback accepts the module's three keys, decodes
// their typed values, and validates supplied scheduling thresholds before
// returning storage strings.
//
// Typed decoding assigns zero values to absent notification fields. Filtering
// the prepared map using the original input keys retains the distinction
// between omission and an explicit value. An empty input returns an empty
// prepared change, and a null threshold map produces no threshold write.
func SettingsParticipant() settings.Participant {
	keys := []string{SettingKeyProviderQuotaNotifyEnabled, SettingKeyProviderQuotaNotifyEmails, SettingKeyProviderSchedulingThresholds}
	return settings.Participant{Module: "provider", Fields: keys, Keys: keys, Prepare: func(_ context.Context, input settings.Fields, _ map[string]string) (settings.PreparedChange, error) {
		if len(input) == 0 {
			return settings.PreparedChange{}, nil
		}
		raw, err := json.Marshal(input)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		var value AdminSettings
		if err = json.Unmarshal(raw, &value); err != nil {
			return settings.PreparedChange{}, err
		}
		values, err := PrepareAdminSettings(value)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		// Typed decoding supplies defaults for omitted fields. Restrict output
		// to the submitted keys so partial settings writes retain the other
		// stored values.
		for key := range values {
			if _, ok := input[key]; !ok {
				delete(values, key)
			}
		}
		return settings.PreparedChange{Values: values}, nil
	}}
}
