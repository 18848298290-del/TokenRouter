package audit

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Audit cleanup interprets a zero retention period as indefinite storage. Settings
// reads and administrator views share the same parser so missing or malformed
// values choose the default period, while negative stored integers normalize to
// the same indefinite-retention value used by the worker.

// Audit retention settings select how many days the cleanup worker keeps records,
// and identify the settings entry read for each scheduled cleanup run.
const (
	DefaultRetentionDays            = 180                        // default record lifetime in days
	SettingKeyAuditLogRetentionDays = "audit_log_retention_days" // persisted retention setting key
)

// RetentionSettingsStore supplies stored text for the audit retention parser.
type RetentionSettingsStore interface {
	// GetValue reads the requested setting using the supplied operation context.
	// Read errors cause GetAuditLogRetentionDays to select the default retention period for cleanup.
	GetValue(context.Context, string) (string, error)
}

// RetentionSettings reads and normalizes the period used by audit cleanup.
type RetentionSettings struct {
	settingRepo RetentionSettingsStore /* stored retention text reader */
}

// AdminReadSettings exposes the audit retention period in days.
type AdminReadSettings struct {
	// AuditLogRetentionDays is the parsed retention period. Zero tells the cleanup
	// worker to keep existing audit records until an administrator clears them.
	AuditLogRetentionDays int
}

// NewRetentionSettings creates an audit-retention reader using the supplied store.
func NewRetentionSettings(repo RetentionSettingsStore) *RetentionSettings {
	return &RetentionSettings{settingRepo: repo}
}

// GetAuditLogRetentionDays returns the configured record lifetime in days, using
// the default when reading fails. A zero result disables scheduled deletion,
// including when the persisted value contains a negative integer.
func (s *RetentionSettings) GetAuditLogRetentionDays(ctx context.Context) int {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAuditLogRetentionDays)
	if err != nil {
		// A settings outage selects DefaultRetentionDays for this run; the worker
		// still receives a usable period rather than a read error.
		return DefaultRetentionDays
	}
	return ParseRetentionDays(value)
}

// ParseRetentionDays trims stored text and returns its retention period in days.
// Empty or invalid integers select DefaultRetentionDays. Negative values return
// zero, the cleanup worker's value for retaining records indefinitely.
func ParseRetentionDays(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultRetentionDays
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		// Invalid persisted text uses the same default as a missing setting, so the
		// administrator view and cleanup worker agree on the effective record lifetime.
		return DefaultRetentionDays
	}
	if n < 0 {
		return 0
	}
	return n
}

// ReadAdminSettings returns the administrator view of the supplied audit settings.
// A missing retention key is parsed as empty text and therefore displays the
// default record lifetime used by scheduled cleanup.
func ReadAdminSettings(settings map[string]string) *AdminReadSettings {
	result := &AdminReadSettings{}
	result.AuditLogRetentionDays = ParseRetentionDays(settings[SettingKeyAuditLogRetentionDays])

	return result
}

// PrepareRetentionDays encodes the supplied day count as decimal setting text.
func PrepareRetentionDays(days int) string { return strconv.Itoa(days) }

// SettingsParticipant prepares decimal retention text from the audit JSON field.
// An absent field produces no change; JSON null prepares "0", the setting for
// indefinite retention. Syntax, type, and integer-range errors stop preparation.
// Accepted integers are encoded as decimal text; ParseRetentionDays later
// interprets stored negative values as indefinite retention.
func SettingsParticipant() settings.Participant {
	return settings.Participant{Module: "audit", Fields: []string{"audit_log_retention_days"}, Keys: []string{SettingKeyAuditLogRetentionDays}, Prepare: func(_ context.Context, input settings.Fields, _ map[string]string) (settings.PreparedChange, error) {
		raw, ok := input["audit_log_retention_days"]
		if !ok {
			return settings.PreparedChange{}, nil
		}
		// JSON null leaves a primitive integer unchanged. Its initial
		// zero therefore prepares indefinite retention through the
		// decimal encoder also used for numeric day counts.
		var days int
		if err := json.Unmarshal(raw, &days); err != nil {
			return settings.PreparedChange{}, err
		}
		return settings.PreparedChange{Values: map[string]string{SettingKeyAuditLogRetentionDays: PrepareRetentionDays(days)}}, nil
	}}
}
