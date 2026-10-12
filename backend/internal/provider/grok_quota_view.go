package provider

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider/usageview"
	upstreamusageview "github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

type GrokQuotaView struct {
	FreeTokenLimit      int64
	NeedsReauth         func(*Record) bool
	JWTSubscriptionTier func(string) string
	CanonicalPlan       func(*float64, string, *upstreamusageview.QuotaSnapshot) string
	ParseTime           func(string) (time.Time, error)
}

// CloneGrokQuotaProbeResult copies nested quota, billing, header, and local usage data for independent callers.
func CloneGrokQuotaProbeResult(value *GrokQuotaProbeResult) *GrokQuotaProbeResult {
	if value == nil {
		return nil
	}
	out := *value
	out.Billing = usageview.CloneBillingSummary(value.Billing)
	out.LocalUsage24h = cloneGrokProbePointer(value.LocalUsage24h)
	out.LocalUsage7d = cloneGrokProbePointer(value.LocalUsage7d)
	out.LocalUsageMonthly = cloneGrokProbePointer(value.LocalUsageMonthly)
	if value.Snapshot != nil {
		out.Snapshot = cloneGrokProbePointer(value.Snapshot)
		out.Snapshot.Requests = usageview.CloneQuotaWindow(value.Snapshot.Requests)
		out.Snapshot.Tokens = usageview.CloneQuotaWindow(value.Snapshot.Tokens)
		out.Snapshot.RetryAfterSeconds = cloneGrokProbePointer(value.Snapshot.RetryAfterSeconds)
		out.Snapshot.Headers = maps.Clone(value.Snapshot.Headers)
	}
	return &out
}

// cloneGrokProbePointer copies a pointed-to value; callers use it for scalar-only data.
func cloneGrokProbePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func (f GrokQuotaView) BuildUsageInfo(provider *Record) *UsageInfo {
	now := time.Now()
	usage := &UsageInfo{
		Source:             "passive",
		UpdatedAt:          &now,
		GrokFreeTokenLimit: f.FreeTokenLimit,
	}
	if provider == nil {
		usage.ErrorCode = "quota_unknown"
		usage.Error = "Grok quota is unknown until billing is probed or an upstream response includes xAI rate-limit headers"
		return usage
	}

	billing, _ := ParseGrokBillingSnapshot(provider.Extra)
	snapshot, err := GrokQuotaSnapshotFromExtra(provider.Extra)
	activeProbeClearsForbidden := NewerSuccessfulGrokActiveProbeClearsBillingForbidden(billing, snapshot)
	if billing != nil {
		usage.GrokBilling = billing
		f.ApplyGrokBillingProgressWindows(usage, billing, now)
		if billing.Plan != "" {
			usage.SubscriptionTier = billing.Plan
			usage.SubscriptionTierRaw = billing.Plan
		}
		if parsedAt, parseErr := time.Parse(time.RFC3339, billing.UpdatedAt); parseErr == nil {
			usage.UpdatedAt = &parsedAt
		}
		if billing.FetchedAt != "" {
			usage.GrokLastQuotaProbeAt = billing.FetchedAt
		}
		usage.GrokQuotaSnapshotState = "billing_observed"
		usage.GrokLastStatusCode = billing.StatusCode
		switch billing.StatusCode {
		case 401:
			usage.NeedsReauth = true
			usage.ErrorCode = "unauthenticated"
		case 403:
			usage.IsForbidden = true
			usage.ForbiddenType = "forbidden"
			usage.ErrorCode = "forbidden"
		case 429:
			usage.ErrorCode = "rate_limited"
		}
		// Official weekly or monthly progress resolves a missing-header unknown state.
		if usage.ErrorCode == "quota_unknown" && (usage.SevenDay != nil || usage.ThirtyDay != nil) {
			usage.ErrorCode = ""
			if strings.Contains(strings.ToLower(usage.Error), "unknown until") ||
				strings.Contains(strings.ToLower(usage.Error), "no xai quota headers") {
				usage.Error = ""
			}
		}
	}

	if err != nil || snapshot == nil {
		f.ApplyGrokCredentialUsageFallback(usage, provider, billing, nil)
		if billing == nil {
			usage.ErrorCode = "quota_unknown"
			usage.Error = "Grok quota is unknown until billing is probed or an upstream response includes xAI rate-limit headers"
		}
		return usage
	}

	if parsedAt, parseErr := time.Parse(time.RFC3339, snapshot.UpdatedAt); parseErr == nil {
		if billing == nil || usage.UpdatedAt == nil || parsedAt.After(*usage.UpdatedAt) {
			usage.UpdatedAt = &parsedAt
		}
	}
	usage.GrokRequestQuota = snapshot.Requests
	usage.GrokTokenQuota = snapshot.Tokens
	usage.GrokRetryAfterSeconds = snapshot.RetryAfterSeconds
	if usage.SubscriptionTier == "" {
		usage.SubscriptionTier = snapshot.SubscriptionTier
		usage.SubscriptionTierRaw = snapshot.SubscriptionTier
	}
	if usage.GrokEntitlementStatus == "" {
		usage.GrokEntitlementStatus = snapshot.EntitlementStatus
	}
	if usage.GrokLastQuotaProbeAt == "" {
		usage.GrokLastQuotaProbeAt = snapshot.LastProbeAt
	}
	usage.GrokLastHeadersSeenAt = snapshot.LastHeadersSeenAt
	if activeProbeClearsForbidden {
		usage.IsForbidden = false
		usage.ForbiddenType = ""
		usage.ErrorCode = ""
		usage.GrokLastQuotaProbeAt = snapshot.LastProbeAt
		usage.GrokLastStatusCode = snapshot.StatusCode
	} else if snapshot.StatusCode >= 400 || usage.GrokLastStatusCode == 0 {
		usage.GrokLastStatusCode = snapshot.StatusCode
	}
	if snapshot.HasObservedHeaders() {
		if usage.GrokQuotaSnapshotState == "" {
			usage.GrokQuotaSnapshotState = "observed"
		}
	} else if billing == nil {
		usage.GrokQuotaSnapshotState = "no_headers"
		usage.ErrorCode = "quota_unknown"
		usage.Error = "No xAI quota headers observed on the latest Grok probe"
	}

	if usage.ErrorCode == "" {
		switch snapshot.StatusCode {
		case 401:
			usage.NeedsReauth = true
			usage.ErrorCode = "unauthenticated"
		case 403:
			usage.IsForbidden = true
			usage.ForbiddenType = "forbidden"
			usage.ErrorCode = "forbidden"
			if usage.GrokEntitlementStatus == "" {
				usage.GrokEntitlementStatus = "forbidden"
			}
		case 429:
			usage.ErrorCode = "rate_limited"
		}
	}
	if f.NeedsReauth(provider) {
		usage.NeedsReauth = true
		if usage.ErrorCode == "" {
			usage.ErrorCode = "spending_limit"
		}
	}
	f.ApplyGrokCredentialUsageFallback(usage, provider, billing, snapshot)
	if activeProbeClearsForbidden && strings.TrimSpace(snapshot.EntitlementStatus) == "" &&
		strings.EqualFold(strings.TrimSpace(usage.GrokEntitlementStatus), "forbidden") {
		usage.GrokEntitlementStatus = ""
	}
	return usage
}

func NewerSuccessfulGrokActiveProbeClearsBillingForbidden(billing *upstreamusageview.BillingSummary, snapshot *upstreamusageview.QuotaSnapshot) bool {
	if billing == nil || billing.StatusCode != 403 || snapshot == nil ||
		snapshot.StatusCode != 200 || strings.TrimSpace(snapshot.ObservationSource) != "active_probe" {
		return false
	}

	billingAt, billingOK := FirstGrokObservationTime(billing.UpdatedAt, billing.FetchedAt)
	probeAt, probeOK := FirstGrokObservationTime(snapshot.LastProbeAt, snapshot.UpdatedAt)
	// Snapshots have second precision, so a later active probe in the same refresh
	// may legitimately share the billing observation's timestamp.
	return billingOK && probeOK && !probeAt.Before(billingAt)
}

func FirstGrokObservationTime(values ...string) (time.Time, bool) {
	for _, value := range values {
		parsedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
		if err == nil {
			return parsedAt, true
		}
	}
	return time.Time{}, false
}

func (f GrokQuotaView) ApplyGrokCredentialUsageFallback(usage *UsageInfo, provider *Record, billing *upstreamusageview.BillingSummary, snapshot *upstreamusageview.QuotaSnapshot) {
	if usage == nil || provider == nil {
		return
	}
	if usage.GrokEntitlementStatus == "" {
		usage.GrokEntitlementStatus = strings.TrimSpace(provider.GetCredential("entitlement_status"))
	}
	f.ApplyGrokResolvedSubscriptionTier(usage, provider, billing, snapshot)
}

func (f GrokQuotaView) ApplyGrokResolvedSubscriptionTier(usage *UsageInfo, provider *Record, billing *upstreamusageview.BillingSummary, snapshot *upstreamusageview.QuotaSnapshot) {
	if usage == nil || provider == nil {
		return
	}
	if jwtTier := f.JWTSubscriptionTier(provider.GetCredential("access_token")); jwtTier != "" {
		usage.SubscriptionTier = jwtTier
		usage.SubscriptionTierRaw = jwtTier
		return
	}
	signal := strings.TrimSpace(provider.GetCredential("subscription_tier"))
	if signal == "" && snapshot != nil {
		signal = strings.TrimSpace(snapshot.SubscriptionTier)
	}
	if signal == "" && billing != nil {
		signal = strings.TrimSpace(billing.Plan)
	}
	var limit *float64
	if billing != nil {
		limit = billing.MonthlyLimitCents
	}
	if plan := f.CanonicalPlan(limit, signal, snapshot); plan != "" {
		usage.SubscriptionTier = plan
		if usage.SubscriptionTierRaw == "" {
			usage.SubscriptionTierRaw = signal
			if strings.TrimSpace(signal) == "" {
				usage.SubscriptionTierRaw = plan
			}
		}
		return
	}
	if usage.SubscriptionTier == "" && signal != "" {
		usage.SubscriptionTier = signal
		usage.SubscriptionTierRaw = signal
	}
}

func GrokQuotaSnapshotFromExtra(extra map[string]any) (*upstreamusageview.QuotaSnapshot, error) {
	if extra == nil {
		return nil, nil
	}
	raw, ok := extra[GrokQuotaSnapshotExtraKey]
	if !ok || raw == nil {
		return nil, nil
	}
	switch snapshot := raw.(type) {
	case *upstreamusageview.QuotaSnapshot:
		return snapshot, nil
	case upstreamusageview.QuotaSnapshot:
		return &snapshot, nil
	case map[string]any:
		data, err := json.Marshal(snapshot)
		if err != nil {
			return nil, err
		}
		var out upstreamusageview.QuotaSnapshot
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		return &out, nil
	default:
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("marshal grok quota snapshot: %w", err)
		}
		var out upstreamusageview.QuotaSnapshot
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
}

// ApplyGrokBillingProgressWindows fills weekly (seven_day) and monthly (thirty_day)
// usage progress from the billing summary.
func (f GrokQuotaView) ApplyGrokBillingProgressWindows(usage *UsageInfo, billing *upstreamusageview.BillingSummary, now time.Time) {
	if usage == nil || billing == nil {
		return
	}
	if billing.UsagePercent != nil {
		seven := &UsageProgress{Utilization: *billing.UsagePercent}
		if end, err := f.ParseTime(strings.TrimSpace(billing.PeriodEnd)); err == nil {
			seven.ResetsAt = &end
			if sec := int(end.Sub(now).Seconds()); sec > 0 {
				seven.RemainingSeconds = sec
			}
		}
		if usage.SevenDay != nil {
			seven.WindowStats = usage.SevenDay.WindowStats
		}
		usage.SevenDay = seven
	}
	var monthlyUtil *float64
	if billing.UsedPercent != nil {
		monthlyUtil = billing.UsedPercent
	} else if billing.MonthlyLimitCents != nil && *billing.MonthlyLimitCents > 0 && billing.UsedCents != nil {
		v := (*billing.UsedCents / *billing.MonthlyLimitCents) * 100
		monthlyUtil = &v
	}
	if monthlyUtil != nil {
		thirty := &UsageProgress{Utilization: *monthlyUtil}
		endRaw := strings.TrimSpace(billing.BillingPeriodEnd)
		if endRaw == "" && billing.PeriodType == "monthly" {
			endRaw = strings.TrimSpace(billing.PeriodEnd)
		}
		if end, err := f.ParseTime(endRaw); err == nil {
			thirty.ResetsAt = &end
			if sec := int(end.Sub(now).Seconds()); sec > 0 {
				thirty.RemainingSeconds = sec
			}
		}
		if usage.ThirtyDay != nil {
			thirty.WindowStats = usage.ThirtyDay.WindowStats
		}
		usage.ThirtyDay = thirty
	}
}

// StampGrokQuotaPlan resolves the model and applies tier evidence from the previous provider snapshot.
func StampGrokQuotaPlan(record *Record, snapshot *upstreamusageview.QuotaSnapshot, model string, resolveModel func(string, ...string) string, applySignal func(*upstreamusageview.QuotaSnapshot, *upstreamusageview.QuotaSnapshot)) {
	if snapshot == nil {
		return
	}
	if strings.TrimSpace(snapshot.Model) == "" {
		if model = strings.TrimSpace(model); model != "" {
			snapshot.Model = resolveModel(model)
		}
	}
	var previous *upstreamusageview.QuotaSnapshot
	if record != nil {
		previous, _ = GrokQuotaSnapshotFromExtra(record.Extra)
	}
	applySignal(snapshot, previous)
}
