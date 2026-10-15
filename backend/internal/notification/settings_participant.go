package notification

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// settings_participant.go prepares administrator SMTP settings and maps
// submitted fields to persisted setting keys.
// SMTP administrator updates separate connection normalization from preparing
// the submitted storage keys. A blank next host can retain the previous
// connection and sender configuration, while a nonempty password alone selects
// whether the prepared map includes a secret write.
//
// The settings participant decodes only its owned request fields and filters
// prepared keys back to those explicitly submitted. smtp_from_email maps to
// stored smtp_from through corresponding field/key positions. This participant
// returns a values map for the common persistence batch and performs no
// network tests, settings writes, or runtime publication during preparation.

// AdminSMTPSettings contains connection, authentication, sender, and TLS inputs for administrator updates.
type AdminSMTPSettings struct {
	// SMTPHost controls the normalizer's connection-retention rule: blank next
	// host with a nonempty current host reuses the current connection and sender fields.
	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`     // configured TCP port
	SMTPUsername string `json:"smtp_username"` // configured authentication account
	// SMTPPassword is trimmed by NormalizeAdminSMTPSettings. PrepareSMTPSettings
	// omits literal empty strings; callers apply normalization when whitespace
	// should also retain the saved secret, because the participant skips that step.
	SMTPPassword string `json:"smtp_password"`
	SMTPFrom     string `json:"smtp_from_email"` // configured sender address
	SMTPFromName string `json:"smtp_from_name"`  // configured sender name
	SMTPUseTLS   bool   `json:"smtp_use_tls"`    // configured TLS switch
}

// NormalizeAdminSMTPSettings returns trimmed next values with port and host-retention rules applied.
// Nonpositive next port initially selects 587. A blank next host with a
// nonempty current host then copies current host, port, account, sender, sender
// name, and TLS mode over those next fields.
//
// Password is handled independently: it retains the trimmed next input during
// host retention rather than copying current's secret. PrepareSMTPSettings later
// omits a blank password write, letting the storage batch retain its saved
// secret while still accepting a supplied replacement password with an unchanged host.
func NormalizeAdminSMTPSettings(current, next AdminSMTPSettings) AdminSMTPSettings {
	next.SMTPHost = strings.TrimSpace(next.SMTPHost)
	next.SMTPUsername = strings.TrimSpace(next.SMTPUsername)
	next.SMTPPassword = strings.TrimSpace(next.SMTPPassword)
	next.SMTPFrom = strings.TrimSpace(next.SMTPFrom)
	next.SMTPFromName = strings.TrimSpace(next.SMTPFromName)
	if next.SMTPPort <= 0 {
		next.SMTPPort = 587
	}
	if next.SMTPHost == "" && current.SMTPHost != "" {
		// Host omission selects the complete saved connection/sender configuration.
		// Copy that related set together, leaving the separately prepared password input available for secret replacement.
		next.SMTPHost = current.SMTPHost
		next.SMTPPort = current.SMTPPort
		next.SMTPUsername = current.SMTPUsername
		next.SMTPFrom = current.SMTPFrom
		next.SMTPFromName = current.SMTPFromName
		next.SMTPUseTLS = current.SMTPUseTLS
	}
	return next
}

// PrepareSMTPSettings builds stored SMTP values and includes the password only when nonempty.
// It serializes the values as supplied, including port and TLS, and maps the
// administrator sender-email field to smtp_from. Callers apply any desired
// normalization before requesting this map; this function performs no storage writes.
func PrepareSMTPSettings(value AdminSMTPSettings) map[string]string {
	result := map[string]string{
		SettingKeySMTPHost: value.SMTPHost, SettingKeySMTPPort: strconv.Itoa(value.SMTPPort),
		SettingKeySMTPUsername: value.SMTPUsername, SettingKeySMTPFrom: value.SMTPFrom,
		SettingKeySMTPFromName: value.SMTPFromName, SettingKeySMTPUseTLS: strconv.FormatBool(value.SMTPUseTLS),
	}
	if value.SMTPPassword != "" {
		// Empty secret input represents retention during administrator updates.
		// Include a password key only when a replacement value is available to the eventual storage batch.
		result[SettingKeySMTPPassword] = value.SMTPPassword
	}
	return result
}

// SMTPSettingsParticipant prepares values for the SMTP fields present in the request.
// Empty input returns no change, and decode failures return before any values
// are prepared. Connection normalization remains the caller's choice; this
// participant serializes supplied values and never reads the current map.
//
// Request fields and storage keys stay in matching positions so smtp_from_email
// becomes smtp_from. Filtering after decoding removes zero-valued fields that
// were omitted, allowing the common settings batch to retain those saved values.
// A literal empty password is also omitted by PrepareSMTPSettings.
func SMTPSettingsParticipant() settings.Participant {
	fields := []string{"smtp_host", "smtp_port", "smtp_username", "smtp_password", "smtp_from_email", "smtp_from_name", "smtp_use_tls"}
	keys := []string{SettingKeySMTPHost, SettingKeySMTPPort, SettingKeySMTPUsername, SettingKeySMTPPassword, SettingKeySMTPFrom, SettingKeySMTPFromName, SettingKeySMTPUseTLS}
	return settings.Participant{Module: "notification", Fields: fields, Keys: keys, Prepare: func(_ context.Context, input settings.Fields, _ map[string]string) (settings.PreparedChange, error) {
		if len(input) == 0 {
			return settings.PreparedChange{}, nil
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		var value AdminSMTPSettings
		if err = json.Unmarshal(encoded, &value); err != nil {
			return settings.PreparedChange{}, err
		}
		values := PrepareSMTPSettings(value)
		for i, field := range fields {
			// Field and storage-key positions also cover the smtp_from_email alias.
			// Filter absent inputs after serialization so decoded zero values cannot overwrite an unrelated saved setting.
			if _, ok := input[field]; !ok {
				delete(values, keys[i])
			}
		}
		return settings.PreparedChange{Values: values}, nil
	}}
}
