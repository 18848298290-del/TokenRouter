package creative

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
)

// runtime_settings.go reads creative feature settings and parses model
// allowlists and worker counts.
// The creative feature switch defaults to enabled when its stored value cannot
// be read. Model admission uses a stricter rule: missing, malformed, or
// invalid model settings become an empty allowlist, leaving the directory
// closed until valid entries are available.

// RuntimeSettingsStore reads persisted Creative Studio settings by key.
type RuntimeSettingsStore interface {
	// GetValue reads one persisted string or returns the storage lookup error.
	GetValue(context.Context, string) (string, error)
}

// RuntimeSettings reads settings on demand and applies defaults for missing or
// invalid values.
type RuntimeSettings struct {
	settingRepo RuntimeSettingsStore // persisted creative settings reader
	// notFound identifies lookup absence for GetCreativeModelSettings, which
	// suppresses that warning while returning the empty model allowlist.
	notFound error // missing setting error sentinel
}

// NewRuntimeSettings binds on-demand reads and a missing-key error sentinel.
// The sentinel controls warning suppression for model settings; unavailable
// repositories use the feature and model defaults implemented by the readers.
func NewRuntimeSettings(repo RuntimeSettingsStore, notFound error) *RuntimeSettings {
	return &RuntimeSettings{settingRepo: repo, notFound: notFound}
}

// ParseCreativeModelSettings returns a normalized allowlist from stored JSON.
// Blank input yields an empty list, and decoding or validation failure logs a
// warning before returning the same closed-admission result.
func ParseCreativeModelSettings(raw string) []CreativeModelSetting {
	if strings.TrimSpace(raw) == "" {
		return []CreativeModelSetting{}
	}
	var input []CreativeModelSetting
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		// Invalid stored allowlists close model admission, so decoding
		// failures cannot
		// expose every provider model as though administrators had
		// configured permission.
		slog.Warn("invalid persisted creative model settings", "error", err)
		return []CreativeModelSetting{}
	}
	normalized, err := NormalizeCreativeModelSettings(input)
	if err != nil {
		// Structurally decoded entries still need valid groups and
		// operations; reject
		// the complete list so a partly valid stored setting cannot
		// widen admission.
		slog.Warn("invalid persisted creative model settings", "error", err)
		return []CreativeModelSetting{}
	}
	return normalized
}

// IsCreativeEnabled reads the runtime switch and disables it only for the
// exact false string. A nil receiver, missing repository, or any read failure
// returns the enabled default.
func (s *RuntimeSettings) IsCreativeEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyCreativeEnabled)
	if err != nil {
		// Feature reads use the enabled default on storage failure,
		// while the separate
		// allowlist reader still closes discovery when its model
		// settings cannot load.
		return true
	}
	return value != "false"
}

// GetCreativeModelSettings reads and normalizes the current creative model
// allowlist. Missing storage or a read error returns an empty list; read
// errors matching notFound suppress the warning.
func (s *RuntimeSettings) GetCreativeModelSettings(ctx context.Context) []CreativeModelSetting {
	if s == nil || s.settingRepo == nil {
		return []CreativeModelSetting{}
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyCreativeModelSettings)
	if err != nil {
		if !errors.Is(err, s.notFound) {
			slog.Warn("failed to read creative model settings", "error", err)
		}
		// A failed model-settings read closes admission for this
		// request; missing keys
		// suppress warnings, while other failures remain visible in the
		// settings log.
		return []CreativeModelSetting{}
	}
	return ParseCreativeModelSettings(raw)
}

// ParseCreativeWorkerCount returns a positive decimal worker count after
// trimming whitespace. Invalid, zero and negative values select
// DefaultCreativeWorkerCount, which supplies the pool target used by runtime
// startup and administration settings reads.
func ParseCreativeWorkerCount(value string) int {
	count, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || count <= 0 {
		return DefaultCreativeWorkerCount
	}
	return count
}
