package notification

import (
	"strconv"
)

// AdminReadSettings contains SMTP parameters and password configuration status.
type AdminReadSettings struct {
	SMTPFrom               string
	SMTPFromName           string
	SMTPHost               string
	SMTPPassword           string
	SMTPPasswordConfigured bool
	SMTPPort               int
	SMTPUseTLS             bool
	SMTPUsername           string
}

// ReadAdminSettings parses SMTP parameters and password status from stored settings.
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
		result.SMTPPort = 587
	}
	result.SMTPPassword = settings[SettingKeySMTPPassword]
	return result
}
