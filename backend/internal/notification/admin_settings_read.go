package notification

import (
	"strconv"
)

// Administrator SMTP reads need connection parameters together with a separate
// password-present flag for masked settings responses. Stored ports are parsed
// as decimal integers, with a failed parse selecting the submission-port default
// used by the mailer.

// AdminReadSettings contains SMTP connection values and password presence for administrator responses.
type AdminReadSettings struct {
	SMTPFrom     string // configured sender address
	SMTPFromName string // configured sender name
	SMTPHost     string // configured server host
	// SMTPPassword and SMTPPasswordConfigured describe the same stored secret.
	// The presence flag tests exact nonemptiness. Composite settings retain the
	// raw secret internally, while administrator responses expose the flag.
	SMTPPassword           string // stored authentication secret
	SMTPPasswordConfigured bool   // stored secret presence
	// SMTPPort is the parsed stored integer; malformed or absent text selects
	// 587, while successfully parsed values are returned without a range check.
	SMTPPort     int
	SMTPUseTLS   bool   // configured TLS switch
	SMTPUsername string // configured authentication account
}

// ReadAdminSettings returns SMTP values from one stored settings map for administrator editing.
// String fields retain their stored spelling, TLS requires the literal true,
// and an invalid decimal port selects 587. Password presence uses exact
// nonemptiness and does not trim the stored secret.
func ReadAdminSettings(settings map[string]string) *AdminReadSettings {
	result := &AdminReadSettings{}
	result.SMTPHost = settings[SettingKeySMTPHost]
	result.SMTPUsername = settings[SettingKeySMTPUsername]
	result.SMTPFrom = settings[SettingKeySMTPFrom]
	result.SMTPFromName = settings[SettingKeySMTPFromName]
	result.SMTPUseTLS = settings[SettingKeySMTPUseTLS] == "true"
	result.SMTPPasswordConfigured = settings[SettingKeySMTPPassword] != ""
	if port, err := strconv.Atoi(settings[SettingKeySMTPPort]); err == nil {
		result.SMTPPort = port
	} else {
		// Failed stored port text uses the submission default so administrator
		// reads expose the same default port as a mailer constructed from those values.
		result.SMTPPort = 587
	}
	result.SMTPPassword = settings[SettingKeySMTPPassword]
	return result
}
