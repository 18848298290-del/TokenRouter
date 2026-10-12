package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	maxProviderNameRunes                 = 100
	duplicateProviderOperationIDExtraKey = "duplicate_operation_id"
)

var duplicateProviderDiscardedExtraKeys = map[string]struct{}{
	// Each duplication operation owns its retry identifier.
	duplicateProviderOperationIDExtraKey: {},
	// External synchronization identifiers belong to one local provider.
	"crs_account_id": {},
	"crs_kind":       {},
	"crs_synced_at":  {},
	// Start local usage counters and derived quota windows empty.
	"quota_used":            {},
	"quota_daily_used":      {},
	"quota_weekly_used":     {},
	"quota_daily_start":     {},
	"quota_weekly_start":    {},
	"quota_daily_reset_at":  {},
	"quota_weekly_reset_at": {},
	// Generate a fresh provider-specific Codex fingerprint seed for the duplicate.
	CodexFingerprintSeedExtraKey: {},
	// Upstream observations, capability probes, and temporary scheduling state are not copied as configuration.
	"model_rate_limits":                      {},
	"session_window_utilization":             {},
	"passive_usage_7d_utilization":           {},
	"passive_usage_7d_reset":                 {},
	"passive_usage_7d_oi_utilization":        {},
	"passive_usage_7d_oi_reset":              {},
	"passive_usage_sampled_at":               {},
	"grok_usage_snapshot":                    {},
	"grok_billing_snapshot":                  {},
	"qoder_quota_snapshot":                   {},
	"qoder_quota_updated_at":                 {},
	CNUsageMonitorSnapshotExtraKey:           {},
	"antigravity_credits_overages":           {},
	"antigravity_force_token_refresh":        {},
	"antigravity_force_token_refresh_at":     {},
	"antigravity_force_token_refresh_reason": {},
	"drive_storage_limit":                    {},
	"drive_storage_usage":                    {},
	"drive_tier_updated_at":                  {},
	"codex_primary_used_percent":             {},
	"codex_primary_reset_after_seconds":      {},
	"codex_primary_window_minutes":           {},
	"codex_secondary_used_percent":           {},
	"codex_secondary_reset_after_seconds":    {},
	"codex_secondary_window_minutes":         {},
	"codex_primary_over_secondary_percent":   {},
	"codex_usage_updated_at":                 {},
	"codex_5h_used_percent":                  {},
	"codex_5h_reset_after_seconds":           {},
	"codex_5h_window_minutes":                {},
	"codex_5h_reset_at":                      {},
	"codex_7d_used_percent":                  {},
	"codex_7d_reset_after_seconds":           {},
	"codex_7d_window_minutes":                {},
	"codex_7d_reset_at":                      {},
	"upstream_billing_probe":                 {},
	"upstream_billing_probe_enabled":         {},
}

func duplicateProviderName(sourceName string) string {
	const suffix = " (Copy)"
	nameRunes := []rune(strings.TrimSpace(sourceName))
	maxBaseRunes := maxProviderNameRunes - len([]rune(suffix))
	if len(nameRunes) > maxBaseRunes {
		nameRunes = nameRunes[:maxBaseRunes]
	}
	return string(nameRunes) + suffix
}

func cloneProviderJSONMap(value map[string]any) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	cloned := make(map[string]any, len(value))
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

func duplicateProviderExtra(value map[string]any) (map[string]any, error) {
	cloned, err := cloneProviderJSONMap(value)
	if err != nil {
		return nil, err
	}
	for key := range duplicateProviderDiscardedExtraKeys {
		delete(cloned, key)
	}
	return cloned, nil
}

func canDuplicateProviderType(providerType string) bool {
	switch providerType {
	case ProviderTypeAPIKey, ProviderTypeUpstream, ProviderTypeBedrock, ProviderTypeServiceAccount:
		return true
	default:
		return false
	}
}

func duplicateProviderGroups(source *Record) ([]GroupMembership, []int64) {
	if len(source.ProviderGroups) > 0 {
		groups := make([]GroupMembership, 0, len(source.ProviderGroups))
		groupIDs := make([]int64, 0, len(source.ProviderGroups))
		for _, sourceGroup := range source.ProviderGroups {
			groups = append(groups, GroupMembership{GroupID: sourceGroup.GroupID})
			groupIDs = append(groupIDs, sourceGroup.GroupID)
		}
		return groups, groupIDs
	}

	groups := make([]GroupMembership, 0, len(source.GroupIDs))
	groupIDs := append([]int64(nil), source.GroupIDs...)
	for _, groupID := range groupIDs {
		groups = append(groups, GroupMembership{GroupID: groupID})
	}
	return groups, groupIDs
}

func duplicateProviderOperationID(sourceID int64, actorScope, operationKey string) string {
	operationKey = strings.TrimSpace(operationKey)
	if operationKey == "" {
		return ""
	}
	actorScope = strings.TrimSpace(actorScope)
	if actorScope == "" {
		actorScope = "admin:0"
	}
	payload := "admin.providers.duplicate\x00" + actorScope + "\x00" + strconv.FormatInt(sourceID, 10) + "\x00" + operationKey
	digest := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", digest)
}

func (s *Admin) findDuplicateByOperationID(ctx context.Context, operationID string) (*Record, error) {
	if operationID == "" {
		return nil, nil
	}
	providers, err := s.providerRepo.FindByExtraField(ctx, duplicateProviderOperationIDExtraKey, operationID)
	if err != nil {
		return nil, fmt.Errorf("find duplicate provider operation: %w", err)
	}
	if len(providers) == 0 {
		return nil, nil
	}
	provider := providers[0]
	return &provider, nil
}

// RecoverDuplicateProvider looks up a committed duplicate without creating one.
// The idempotency coordinator uses it when response persistence is uncertain.
func (s *Admin) RecoverDuplicateProvider(ctx context.Context, id int64, actorScope, operationKey string) (*Record, error) {
	return s.findDuplicateByOperationID(ctx, duplicateProviderOperationID(id, actorScope, operationKey))
}

func cloneProviderValuePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// DuplicateProvider creates a paused provider from source configuration with fresh runtime state.
// Credentials and Extra are copied deeply before normalization.
// Linked shadows must use CreateShadow and are rejected here.
func (s *Admin) DuplicateProvider(ctx context.Context, id int64, actorScope, operationKey string) (*Record, error) {
	operationID := duplicateProviderOperationID(id, actorScope, operationKey)
	existing, err := s.RecoverDuplicateProvider(ctx, id, actorScope, operationKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	source, err := s.providerRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if source.IsCredentialShadow() {
		return nil, infraerrors.BadRequest(
			"PROVIDER_DUPLICATE_SHADOW_UNSUPPORTED",
			"linked credential shadow providers cannot be duplicated; duplicate the parent provider instead",
		)
	}
	if !canDuplicateProviderType(source.Type) {
		return nil, infraerrors.BadRequest(
			"PROVIDER_DUPLICATE_CREDENTIAL_TYPE_UNSUPPORTED",
			"providers with rotating or unsupported credential types cannot be duplicated",
		)
	}

	credentials, err := cloneProviderJSONMap(source.Credentials)
	if err != nil {
		return nil, fmt.Errorf("clone provider credentials: %w", err)
	}
	extra, err := duplicateProviderExtra(source.Extra)
	if err != nil {
		return nil, fmt.Errorf("clone provider extra configuration: %w", err)
	}
	if operationID != "" {
		if extra == nil {
			extra = make(map[string]any, 1)
		}
		extra[duplicateProviderOperationIDExtraKey] = operationID
	}

	var expiresAt *int64
	if source.ExpiresAt != nil {
		unix := source.ExpiresAt.Unix()
		expiresAt = &unix
	}
	autoPauseOnExpired := source.AutoPauseOnExpired
	groups, groupIDs := duplicateProviderGroups(source)
	proxyID := source.ProxyID
	if source.ProxyFallbackOriginID != nil {
		// Proxy failover is temporary; copy the originally configured proxy.
		proxyID = source.ProxyFallbackOriginID
	}
	input := &CreateProviderInput{
		Name:               duplicateProviderName(source.Name),
		Notes:              cloneProviderValuePointer(source.Notes),
		Platform:           source.Platform,
		Type:               source.Type,
		Credentials:        credentials,
		Extra:              extra,
		ProxyID:            cloneProviderValuePointer(proxyID),
		Concurrency:        source.Concurrency,
		Priority:           source.Priority,
		RateMultiplier:     cloneProviderValuePointer(source.RateMultiplier),
		LoadFactor:         cloneProviderValuePointer(source.LoadFactor),
		GroupIDs:           groupIDs,
		ExpiresAt:          expiresAt,
		AutoPauseOnExpired: &autoPauseOnExpired,
	}
	if err := egress.NormalizeHeaderOverrideCredentials(input.Credentials); err != nil {
		return nil, err
	}
	duplicate, err := BuildProviderForCreate(input, input.Extra, s.options.Creation)
	if err != nil {
		return nil, err
	}
	// An administrator must review copied credentials before enabling shared production traffic.
	duplicate.Schedulable = false
	if s.options.Duplicates == nil {
		return nil, errors.New("provider duplicate repository is not configured")
	}
	if err := s.options.Duplicates.CreateWithProviderGroups(ctx, duplicate, groups); err != nil {
		return nil, fmt.Errorf("create duplicate provider: %w", err)
	}
	for i := range groups {
		groups[i].ProviderID = duplicate.ID
	}
	duplicate.ProviderGroups = groups
	duplicate.GroupIDs = groupIDs
	return duplicate, nil
}
