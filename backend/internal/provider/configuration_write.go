package provider

import (
	"context"
	"fmt"
	"maps"
	"time"
)

const (
	ConfigName ConfigurationFields = 1 << iota
	ConfigNotes
	ConfigPlatform
	ConfigType
	ConfigCredentials
	ConfigExtra
	ConfigProxyID
	ConfigConcurrency
	ConfigPriority
	ConfigRateMultiplier
	ConfigLoadFactor
	ConfigStatus
	ConfigErrorMessage
	ConfigSchedulable
	ConfigExpiresAt
	ConfigAutoPauseOnExpired
	ConfigParentProviderID
	ConfigQuotaDimension
)

// Billing owns usage counters and maintenance services own observations; configuration replacement retains their latest values.
var managedConfigurationExtraKeys = []string{
	"quota_used", "quota_daily_used", "quota_daily_start", "quota_weekly_used", "quota_weekly_start",
	"grok_billing_snapshot", "grok_usage_snapshot", "grok_observed_models",
	"qoder_quota_snapshot", "qoder_quota_updated_at",
	"ollama_cloud_usage_session", "ollama_cloud_usage_auto_refresh", "ollama_cloud_usage_snapshot",
	"cn_usage_monitor_snapshot", "model_rate_limits", "antigravity_quota_scopes", "antigravity_credits_overages",
}

// ConfigurationFields identifies writable configuration fields, excluding activity times, windows, and health observations.
type ConfigurationFields uint32

// ConfigurationChange selects fields to update; omitted secrets can be inherited from the latest locked record.
type ConfigurationChange struct {
	// ExtraPatch contains the raw maintenance patch, not a previously read snapshot.
	ExtraPatch          map[string]any     `json:"-"`
	CredentialPatch     map[string]any     `json:"-"`
	ExpectedCredentials *CredentialVersion `json:"-"`
	ProtocolExtra       map[string]any
	PreserveExtraKeys   []string
	ComputeResetAt      *time.Time
	NormalizeWindowAt   *time.Time

	Fields             ConfigurationFields
	PreserveSensitive  bool
	CredentialInput    map[string]any
	NormalizeProtocols bool
}

// ConfigurationWriter updates configuration using the caller's transaction connection when one is supplied.
type ConfigurationWriter interface {
	UpdateConfiguration(context.Context, *Record, ConfigurationChange) error
}

// ApplyConfigurationChange takes selected fields from desired and retains other values from the latest locked record.
func ApplyConfigurationChange(current, desired *Record, change ConfigurationChange) (*Record, error) {
	if change.ExpectedCredentials != nil && !MatchesCredentialVersion(current, *change.ExpectedCredentials) {
		return nil, ErrRefreshProviderStateChanged
	}
	out := CloneRecord(current)
	if change.Fields&ConfigName != 0 {
		out.Name = desired.Name
	}
	if change.Fields&ConfigNotes != 0 {
		out.Notes = desired.Notes
	}
	if change.Fields&ConfigPlatform != 0 {
		out.Platform = desired.Platform
	}
	if change.Fields&ConfigType != 0 {
		out.Type = desired.Type
	}
	if change.Fields&ConfigCredentials != 0 {
		out.Credentials = desired.Credentials
		if change.CredentialPatch != nil {
			out.Credentials = MergeCredentials(current.Credentials, CloneValues(change.CredentialPatch))
		}
	}
	if change.Fields&ConfigExtra != 0 {
		out.Extra = desired.Extra
		if change.ExtraPatch != nil {
			out.Extra = CloneValues(current.Extra)
			if out.Extra == nil {
				out.Extra = map[string]any{}
			}
			maps.Copy(out.Extra, CloneValues(change.ExtraPatch))
		}
	}
	if change.Fields&ConfigProxyID != 0 {
		out.ProxyID = desired.ProxyID
	}
	if change.Fields&ConfigConcurrency != 0 {
		out.Concurrency = desired.Concurrency
	}
	if change.Fields&ConfigPriority != 0 {
		out.Priority = desired.Priority
	}
	if change.Fields&ConfigRateMultiplier != 0 {
		out.RateMultiplier = desired.RateMultiplier
	}
	if change.Fields&ConfigLoadFactor != 0 {
		out.LoadFactor = desired.LoadFactor
	}
	if change.Fields&ConfigStatus != 0 {
		out.Status = desired.Status
	}
	if change.Fields&ConfigErrorMessage != 0 {
		out.ErrorMessage = desired.ErrorMessage
	}
	if change.Fields&ConfigSchedulable != 0 {
		out.Schedulable = desired.Schedulable
	}
	if change.Fields&ConfigExpiresAt != 0 {
		out.ExpiresAt = desired.ExpiresAt
	}
	if change.Fields&ConfigAutoPauseOnExpired != 0 {
		out.AutoPauseOnExpired = desired.AutoPauseOnExpired
	}
	if change.Fields&ConfigParentProviderID != 0 {
		out.ParentProviderID = desired.ParentProviderID
	}
	if change.Fields&ConfigQuotaDimension != 0 {
		out.QuotaDimension = desired.QuotaDimension
	}
	out.GroupIDs = desired.GroupIDs
	out.Credentials = CloneValues(out.Credentials)
	if change.Fields&ConfigCredentials != 0 && change.PreserveSensitive && out.Credentials == nil {
		out.Credentials = map[string]any{}
	}
	out.Extra = CloneValues(out.Extra)
	if change.Fields&ConfigCredentials != 0 && change.PreserveSensitive {
		// The desired values have passed platform validation; inherit omitted secrets from the locked record.
		for _, key := range SensitiveCredentialKeys {
			if _, provided := change.CredentialInput[key]; provided {
				continue
			}
			delete(out.Credentials, key)
			if v, ok := current.Credentials[key]; ok {
				out.Credentials[key] = CloneValues(map[string]any{key: v})[key]
			}
		}
		// Remove legacy SSO and password fields after inheriting secrets.
		out.Credentials = SanitizeStoredCredentials(out.Platform, out.Credentials)
	}
	if change.Fields&ConfigExtra != 0 {
		if out.Extra == nil {
			out.Extra = map[string]any{}
		}
		for _, key := range managedConfigurationExtraKeys {
			delete(out.Extra, key)
			if v, ok := current.Extra[key]; ok {
				out.Extra[key] = CloneValues(map[string]any{key: v})[key]
			}
		}
		for _, key := range change.PreserveExtraKeys {
			delete(out.Extra, key)
			if v, ok := current.Extra[key]; ok {
				out.Extra[key] = CloneValues(map[string]any{key: v})[key]
			}
		}
		if change.ComputeResetAt != nil {
			ComputeQuotaResetAt(out.Extra, *change.ComputeResetAt, current.LoadLocation)
		}
		if change.NormalizeWindowAt != nil {
			NormalizeFixedQuotaWindows(out.Extra, *change.NormalizeWindowAt, current.LoadLocation)
		}
		// Apply the overages toggle's rate-limit cleanup to the latest Extra state.
		if current.Platform == PlatformAntigravity && current.IsOveragesEnabled() != out.IsOveragesEnabled() {
			delete(out.Extra, "antigravity_credits_overages")
			if out.IsOveragesEnabled() {
				delete(out.Extra, "model_rate_limits")
			} else if limits, ok := out.Extra["model_rate_limits"].(map[string]any); ok {
				delete(limits, "AICredits")
			}
		}
	}
	if change.NormalizeProtocols {
		ApplyLegacyProtocolPatch(out, change.CredentialInput, change.ProtocolExtra)
		if err := NormalizeCNProviderCredentials(out, false); err != nil {
			return nil, err
		}
		if err := NormalizeOpenAIAPIKeyConfiguration(out); err != nil {
			return nil, err
		}
		if err := NormalizeProviderProtocols(out); err != nil {
			return nil, err
		}
		if seed, ok := CodexFingerprintSeed(current.Extra); ok {
			if out.Extra == nil {
				out.Extra = map[string]any{}
			}
			out.Extra[CodexFingerprintSeedExtraKey] = seed
		} else if seed, ok := CodexFingerprintSeed(desired.Extra); ok {
			if out.Extra == nil {
				out.Extra = map[string]any{}
			}
			out.Extra[CodexFingerprintSeedExtraKey] = seed
		}
	}
	if CNUsageMonitorIdentityFingerprint(current) != CNUsageMonitorIdentityFingerprint(out) {
		delete(out.Extra, CNUsageMonitorSnapshotExtraKey)
	}
	return CloneRecord(out), nil
}

// WriteConfiguration uses ConfigurationWriter when supported, otherwise it falls back to whole-record Update.
func WriteConfiguration(ctx context.Context, store interface {
	Update(context.Context, *Record) error
}, value *Record, change ConfigurationChange,
) error {
	if writer, ok := store.(ConfigurationWriter); ok {
		return writer.UpdateConfiguration(ctx, value, change)
	}
	if change.ExpectedCredentials != nil || change.CredentialPatch != nil || change.ExtraPatch != nil {
		return fmt.Errorf("%w: conditional configuration writer is not configured", ErrRefreshCredentialPersist)
	}
	return store.Update(ctx, value)
}

// CRSSyncConfiguration selects the fields owned by CRS synchronization.
func CRSSyncConfiguration(proxyProvided bool) ConfigurationChange {
	change := ConfigurationChange{Fields: ConfigName | ConfigPlatform | ConfigType | ConfigCredentials | ConfigExtra | ConfigConcurrency | ConfigPriority | ConfigStatus | ConfigSchedulable}
	if proxyProvided {
		change.Fields |= ConfigProxyID
	}
	return change
}
