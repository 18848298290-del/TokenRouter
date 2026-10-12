package dto

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"

	proxydto "github.com/TokenFlux/TokenRouter/internal/egress/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	routingdto "github.com/TokenFlux/TokenRouter/internal/routing/httpapi/dto"
)

func ProviderFromRecordShallow(a *provider.Record) *Provider {
	if a == nil {
		return nil
	}
	a = provider.CloneRecord(a)
	runtime := provider.RuntimeConfig{Extra: a.Extra, Concurrency: a.Concurrency, SessionWindowStart: a.SessionWindowStart, SessionWindowEnd: a.SessionWindowEnd}
	redactedCreds, credsStatus := RedactCredentials(a.Credentials)
	extra := redactProviderManagedExtra(a.Extra)
	var ollamaCloudUsage *provider.OllamaCloudUsageState
	if state := provider.OllamaCloudUsageStateFromProvider(a); state.Eligible {
		ollamaCloudUsage = state
	}
	out := &Provider{
		ID:                      a.ID,
		Name:                    a.Name,
		Notes:                   a.Notes,
		Platform:                a.Platform,
		Type:                    a.Type,
		Credentials:             redactedCreds,
		CredentialsStatus:       credsStatus,
		Extra:                   extra,
		OllamaCloudUsage:        ollamaCloudUsage,
		ProxyID:                 a.ProxyID,
		ProxyFallbackOriginID:   a.ProxyFallbackOriginID,
		ProxyFallbackOriginName: a.ProxyFallbackOriginName,
		Concurrency:             a.Concurrency,
		LoadFactor:              a.LoadFactor,
		Priority:                a.Priority,
		RateMultiplier:          a.BillingRateMultiplier(),
		Status:                  a.Status,
		ErrorMessage:            a.ErrorMessage,
		LastUsedAt:              a.LastUsedAt,
		ExpiresAt:               timeToUnixSeconds(a.ExpiresAt),
		AutoPauseOnExpired:      a.AutoPauseOnExpired,
		CreatedAt:               a.CreatedAt,
		UpdatedAt:               a.UpdatedAt,
		Schedulable:             a.Schedulable,
		RateLimitedAt:           a.RateLimitedAt,
		RateLimitResetAt:        a.RateLimitResetAt,
		OverloadUntil:           a.OverloadUntil,
		TempUnschedulableUntil:  a.TempUnschedulableUntil,
		TempUnschedulableReason: a.TempUnschedulableReason,
		QuotaAutoPaused:         a.QuotaAutoPaused,
		SessionWindowStart:      a.SessionWindowStart,
		SessionWindowEnd:        a.SessionWindowEnd,
		SessionWindowStatus:     a.SessionWindowStatus,
		GroupIDs:                a.GroupIDs,
		ParentProviderID:        a.ParentProviderID,
		QuotaDimension:          a.QuotaDimension,
	}

	// Expose Anthropic OAuth/Setup Token cost and session controls stored in Extra.
	if a.IsAnthropicOAuthOrSetupToken() {
		if limit := runtime.GetWindowCostLimit(); limit > 0 {
			out.WindowCostLimit = &limit
		}
		if reserve := runtime.GetWindowCostStickyReserve(); reserve > 0 {
			out.WindowCostStickyReserve = &reserve
		}
		if maxSessions := runtime.GetMaxSessions(); maxSessions > 0 {
			out.MaxSessions = &maxSessions
		}
		if idleTimeout := runtime.GetSessionIdleTimeoutMinutes(); idleTimeout > 0 {
			out.SessionIdleTimeoutMin = &idleTimeout
		}
		if rpm := runtime.GetBaseRPM(); rpm > 0 {
			out.BaseRPM = &rpm
			strategy := runtime.GetRPMStrategy()
			out.RPMStrategy = &strategy
			buffer := runtime.GetRPMStickyBuffer()
			out.RPMStickyBuffer = &buffer
		}

		if mode := runtime.GetUserMsgQueueMode(); mode != "" {
			out.UserMsgQueueMode = &mode
		}

		if a.IsSessionIDMaskingEnabled() {
			enabled := true
			out.EnableSessionIDMasking = &enabled
		}

		if a.IsCacheTTLOverrideEnabled() {
			enabled := true
			out.CacheTTLOverrideEnabled = &enabled
			target := a.GetCacheTTLOverrideTarget()
			out.CacheTTLOverrideTarget = &target
		}

		if a.IsCustomBaseURLEnabled() {
			enabled := true
			out.CustomBaseURLEnabled = &enabled
			if customURL := a.GetCustomBaseURL(); customURL != "" {
				out.CustomBaseURL = &customURL
			}
		}
	}

	// TLS fingerprint settings apply to Anthropic OAuth/Setup Token and OpenAI OAuth providers.
	if a.SupportsTLSFingerprint() {
		if a.IsTLSFingerprintEnabled() {
			enabled := true
			out.EnableTLSFingerprint = &enabled
		}
		if profileID := a.GetTLSFingerprintProfileID(); profileID != 0 {
			out.TLSFingerprintProfileID = &profileID
		}
		if routerID := a.GetTLSFingerprintRouterID(); routerID != 0 {
			out.TLSFingerprintRouterID = &routerID
		}
	}

	if a.IsOpenAIOAuth() {
		policy := a.GetOpenAIOAuthClientPolicy()
		out.OpenAIOAuthClientPolicy = &policy
	}

	// Expose quota limits for API key and Bedrock providers.
	if a.IsAPIKeyOrBedrock() {
		if limit := a.GetQuotaLimit(); limit > 0 {
			out.QuotaLimit = &limit
			used := a.GetQuotaUsed()
			out.QuotaUsed = &used
		}
		if limit := a.GetQuotaDailyLimit(); limit > 0 {
			out.QuotaDailyLimit = &limit
			used := a.GetQuotaDailyUsed()
			if a.IsDailyQuotaPeriodExpired() {
				used = 0
			}
			out.QuotaDailyUsed = &used
		}
		if limit := a.GetQuotaWeeklyLimit(); limit > 0 {
			out.QuotaWeeklyLimit = &limit
			used := a.GetQuotaWeeklyUsed()
			if a.IsWeeklyQuotaPeriodExpired() {
				used = 0
			}
			out.QuotaWeeklyUsed = &used
		}

		if mode := a.GetQuotaDailyResetMode(); mode == "fixed" {
			out.QuotaDailyResetMode = &mode
			hour := a.GetQuotaDailyResetHour()
			out.QuotaDailyResetHour = &hour
		}
		if mode := a.GetQuotaWeeklyResetMode(); mode == "fixed" {
			out.QuotaWeeklyResetMode = &mode
			day := a.GetQuotaWeeklyResetDay()
			out.QuotaWeeklyResetDay = &day
			hour := a.GetQuotaWeeklyResetHour()
			out.QuotaWeeklyResetHour = &hour
		}
		if a.GetQuotaDailyResetMode() == "fixed" || a.GetQuotaWeeklyResetMode() == "fixed" {
			tz := a.GetQuotaResetTimezone()
			out.QuotaResetTimezone = &tz
		}
		if a.Extra != nil {
			if v, ok := a.Extra["quota_daily_reset_at"].(string); ok && v != "" {
				out.QuotaDailyResetAt = &v
			}
			if v, ok := a.Extra["quota_weekly_reset_at"].(string); ok && v != "" {
				out.QuotaWeeklyResetAt = &v
			}
		}

		if enabled := a.GetQuotaNotifyDailyEnabled(); enabled {
			out.QuotaNotifyDailyEnabled = &enabled
		}
		if threshold := a.GetQuotaNotifyDailyThreshold(); threshold > 0 {
			out.QuotaNotifyDailyThreshold = &threshold
		}
		if enabled := a.GetQuotaNotifyWeeklyEnabled(); enabled {
			out.QuotaNotifyWeeklyEnabled = &enabled
		}
		if threshold := a.GetQuotaNotifyWeeklyThreshold(); threshold > 0 {
			out.QuotaNotifyWeeklyThreshold = &threshold
		}
		if enabled := a.GetQuotaNotifyTotalEnabled(); enabled {
			out.QuotaNotifyTotalEnabled = &enabled
		}
		if threshold := a.GetQuotaNotifyTotalThreshold(); threshold > 0 {
			out.QuotaNotifyTotalThreshold = &threshold
		}
	}

	return out
}

func redactProviderManagedExtra(extra map[string]any) map[string]any {
	if extra == nil {
		return nil
	}
	redacted := make(map[string]any, len(extra))
	for key, value := range extra {
		switch key {
		case provider.OllamaCloudUsageSessionExtraKey,
			provider.OllamaCloudUsageAutoRefreshExtraKey,
			provider.OllamaCloudUsageSnapshotExtraKey:
			continue
		case provider.UpstreamUsageQueryExtraKey:
			// Historical Extra values may contain credentials inside the usage query configuration.
			// Return only the three allowed non-sensitive configuration fields to the browser.
			redacted[key] = redactUpstreamUsageQuery(value)
		default:
			redacted[key] = provider.CloneValues(map[string]any{key: value})[key]
		}
	}
	return redacted
}

func redactUpstreamUsageQuery(value any) map[string]any {
	object, ok := value.(map[string]any)
	if !ok {
		payload, err := json.Marshal(value)
		if err != nil {
			return map[string]any{}
		}
		if err := json.Unmarshal(payload, &object); err != nil || object == nil {
			return map[string]any{}
		}
	}
	result := make(map[string]any, 3)
	if enabled, ok := object["enabled"].(bool); ok {
		result["enabled"] = enabled
	}
	if adapter, ok := object["adapter"].(string); ok {
		// Restrict adapter names to registered values; arbitrary historical strings may contain credentials.
		if adapter == provider.UpstreamUsageAdapterSub2API || adapter == provider.UpstreamUsageAdapterNewAPI || adapter == provider.UpstreamUsageAdapterZivv {
			result["adapter"] = adapter
		}
	}
	if baseURL, ok := object["base_url"].(string); ok {
		if parsed, err := url.Parse(strings.TrimSpace(baseURL)); err == nil && parsed.Scheme != "" && parsed.Host != "" &&
			parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" &&
			(strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) {
			result["base_url"] = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		}
	}
	return result
}

func ProviderFromRecord(a *provider.Record) *Provider {
	if a == nil {
		return nil
	}
	out := ProviderFromRecordShallow(a)
	out.Proxy = proxydto.ProxyFromEgress(a.Proxy)
	if len(a.ProviderGroups) > 0 {
		out.ProviderGroups = make([]ProviderGroup, 0, len(a.ProviderGroups))
		for i := range a.ProviderGroups {
			ag := a.ProviderGroups[i]
			out.ProviderGroups = append(out.ProviderGroups, *ProviderGroupFromRecord(&ag))
		}
	}
	if len(a.Groups) > 0 {
		out.Groups = make([]*Group, 0, len(a.Groups))
		for _, g := range a.Groups {
			out.Groups = append(out.Groups, routingdto.GroupFromRouting((*routing.Group)(g)))
		}
	}
	return out
}

func ProviderGroupFromRecord(ag *provider.GroupMembership) *ProviderGroup {
	if ag == nil {
		return nil
	}
	return &ProviderGroup{
		ProviderID: ag.ProviderID,
		GroupID:    ag.GroupID,
		CreatedAt:  ag.CreatedAt,
		Provider:   ProviderFromRecordShallow(ag.Provider),
		Group:      routingdto.GroupFromRouting((*routing.Group)(ag.Group)),
	}
}

func timeToUnixSeconds(value *time.Time) *int64 {
	if value == nil {
		return nil
	}
	ts := value.Unix()
	return &ts
}
