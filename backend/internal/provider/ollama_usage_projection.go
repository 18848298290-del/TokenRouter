package provider

import (
	"encoding/json"
	"strings"
)

// Ollama administrator state is read from managed session, refresh, and
// snapshot keys in provider Extra. Eligibility controls whether those values
// are exposed, while decoded snapshots accept recognized statuses and
// encryption-key availability is supplied by the service.

// OllamaCloudUsageStateFromProvider builds administrator state from the
// supplied record's Extra fields. Nil returns an empty state; ineligible
// records retain their ID but skip managed fields. Encryption-key configuration
// is added separately by EnrichState.
func OllamaCloudUsageStateFromProvider(provider *Record) *OllamaCloudUsageState {
	state := &OllamaCloudUsageState{}
	if provider == nil {
		return state
	}
	state.ProviderID = provider.ID
	state.Eligible = IsOllamaCloudUsageProvider(provider)
	if !state.Eligible {
		return state
	}
	state.Configured = OllamaCloudUsageConfigured(provider)
	state.AutoRefreshEnabled = state.Configured && OllamaCloudUsageAutoRefreshEnabled(provider)
	state.Snapshot = DecodeOllamaCloudUsageSnapshot(provider.Extra)
	return state
}

// OllamaCloudUsageConfigured reports whether Extra contains a nonblank
// encrypted-session string. Nil records, absent keys, other value types, and
// whitespace-only strings return false; decryption and cookie validity are
// checked during refresh.
func OllamaCloudUsageConfigured(provider *Record) bool {
	if provider == nil || provider.Extra == nil {
		return false
	}
	value, ok := provider.Extra[OllamaCloudUsageSessionExtraKey].(string)
	return ok && strings.TrimSpace(value) != ""
}

// OllamaCloudUsageAutoRefreshEnabled reads a true boolean from the stored
// auto-refresh key. Nil or another value type returns false; callers separately
// require provider eligibility, a configured session, and enabled global
// settings for periodic work.
func OllamaCloudUsageAutoRefreshEnabled(provider *Record) bool {
	if provider == nil || provider.Extra == nil {
		return false
	}
	enabled, ok := provider.Extra[OllamaCloudUsageAutoRefreshExtraKey].(bool)
	return ok && enabled
}

// DecodeOllamaCloudUsageSnapshot returns a fresh JSON-decoded snapshot with one
// of the three recognized statuses. Missing, unencodable, malformed, or
// unknown-status values return nil; timestamps and provider identity are not
// validated by this decoder.
func DecodeOllamaCloudUsageSnapshot(extra map[string]any) *OllamaCloudUsageSnapshot {
	if extra == nil {
		return nil
	}
	value, ok := extra[OllamaCloudUsageSnapshotExtraKey]
	if !ok || value == nil {
		return nil
	}
	// Decode through JSON so supported nested snapshot values receive fresh
	// storage; serialization and shape failures are treated as absent
	// observations.
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var snapshot OllamaCloudUsageSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil
	}
	if snapshot.Status != OllamaCloudUsageStatusOK && snapshot.Status != OllamaCloudUsageStatusUnauthorized && snapshot.Status != OllamaCloudUsageStatusFailed {
		return nil
	}
	return &snapshot
}
