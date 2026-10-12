package audit

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// DefaultRetentionDays is the default audit-log retention period in days.
const (
	DefaultRetentionDays            = 180
	SettingKeyAuditLogRetentionDays = "audit_log_retention_days"
)

// RetentionSettingsStore reads audit retention settings.
type RetentionSettingsStore interface {
	GetValue(context.Context, string) (string, error)
}

// RetentionSettings parses the audit retention period.
type RetentionSettings struct{ settingRepo RetentionSettingsStore }

// AdminReadSettings exposes the audit retention period in days.
type AdminReadSettings struct{ AuditLogRetentionDays int }

// NewRetentionSettings creates a retention settings reader.
func NewRetentionSettings(repo RetentionSettingsStore) *RetentionSettings {
	return &RetentionSettings{settingRepo: repo}
}

// GetAuditLogRetentionDays returns the retention period; zero means indefinite retention.
func (s *RetentionSettings) GetAuditLogRetentionDays(ctx context.Context) int {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAuditLogRetentionDays)
	if err != nil {
		return DefaultRetentionDays
	}
	return ParseRetentionDays(value)
}

// ParseRetentionDays defaults empty or invalid values and treats negative values as indefinite retention.
func ParseRetentionDays(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultRetentionDays
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return DefaultRetentionDays
	}
	if n < 0 {
		return 0
	}
	return n
}

// ReadAdminSettings parses audit retention from the supplied settings.
func ReadAdminSettings(settings map[string]string) *AdminReadSettings {
	result := &AdminReadSettings{}
	result.AuditLogRetentionDays = ParseRetentionDays(settings[SettingKeyAuditLogRetentionDays])

	return result
}

// PrepareRetentionDays encodes the retention period as a decimal integer.
func PrepareRetentionDays(days int) string { return strconv.Itoa(days) }

// SettingsParticipant prepares audit retention setting changes.
func SettingsParticipant() settings.Participant {
	return settings.Participant{Module: "audit", Fields: []string{"audit_log_retention_days"}, Keys: []string{SettingKeyAuditLogRetentionDays}, Prepare: func(_ context.Context, input settings.Fields, _ map[string]string) (settings.PreparedChange, error) {
		raw, ok := input["audit_log_retention_days"]
		if !ok {
			return settings.PreparedChange{}, nil
		}
		var days int
		if err := json.Unmarshal(raw, &days); err != nil {
			return settings.PreparedChange{}, err
		}
		return settings.PreparedChange{Values: map[string]string{SettingKeyAuditLogRetentionDays: PrepareRetentionDays(days)}}, nil
	}}
}
