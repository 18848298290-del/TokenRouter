package provider

import (
	"context"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider/usageview"
)

const GrokUsageBillingExtraKey = "grok_billing_snapshot"

type GrokUsageProbe struct {
	Billing                                        *usageview.BillingSummary
	LocalUsage24h, LocalUsage7d, LocalUsageMonthly *WindowStats
}

// GrokUsageOptions supplies upstream observations and parsing; the service controls freshness and local
// statistics.
type GrokUsageOptions struct {
	Available      func() bool
	StatsAvailable func() bool
	Probe          func(context.Context, int64) (*GrokUsageProbe, error)
	Build          func(*Record) *UsageInfo
	Enrich         func(*UsageInfo, *Record)
}

func (s *OAuthUsageService) GetGrokUsage(ctx context.Context, provider *Record, force bool) (*UsageInfo, error) {
	if s.options.Grok.Available == nil || !s.options.Grok.Available() {
		now := s.options.Now()
		return &UsageInfo{UpdatedAt: &now}, nil
	}
	var billingProbeResult *GrokUsageProbe
	if provider != nil && provider.IsGrokOAuth() && s.options.Grok.Probe != nil && (force || GrokBillingSnapshotNeedsRefresh(provider, s.options.Now())) && s.ShouldProbeGrokBilling(provider.ID, s.options.Now(), force) {
		result, err := s.options.Grok.Probe(ctx, provider.ID)
		if err == nil && result != nil && result.Billing != nil {
			result = cloneGrokUsageProbe(result)
			billingProbeResult = result
			provider.Extra = MergeUsageExtra(provider.Extra, map[string]any{GrokUsageBillingExtraKey: usageview.CloneBillingSummary(result.Billing)})
		} else if err != nil && force {
			return nil, err
		}
	}
	usage := CloneUsageInfo(s.options.Grok.Build(provider))
	if usage.GrokQuotaSnapshotState == "" {
		if usage.ErrorCode == "quota_unknown" {
			usage.GrokQuotaSnapshotState = "unknown_until_first_response"
		} else {
			usage.GrokQuotaSnapshotState = "observed"
		}
	}

	if provider != nil {
		if s.options.Grok.StatsAvailable != nil && s.options.Grok.StatsAvailable() {
			if stats, err := s.stats.usageLogRepo.GetProviderTodayStats(ctx, provider.ID); err == nil && stats != nil {
				usage.GrokLocalUsage = normalizedLocalWindowStats(stats)
			}
		}
		if billingProbeResult != nil {
			usage.GrokLocalUsage24h = billingProbeResult.LocalUsage24h
			usage.GrokLocalUsage7d = billingProbeResult.LocalUsage7d
			usage.GrokLocalUsageMonthly = billingProbeResult.LocalUsageMonthly
		} else if s.options.Grok.StatsAvailable != nil && s.options.Grok.StatsAvailable() {
			usage.GrokLocalUsage24h, usage.GrokLocalUsage7d, usage.GrokLocalUsageMonthly = GrokLocalUsageForQuota(
				ctx, s.stats.usageLogRepo, provider.ID, usage.GrokBilling, s.options.Now().UTC(), s.options.Warn,
			)
		}
		// Attach matching local statistics to the upstream weekly and monthly progress windows.
		if usage.SevenDay != nil && usage.GrokLocalUsage7d != nil {
			usage.SevenDay.WindowStats = usage.GrokLocalUsage7d
		}
		if usage.ThirtyDay != nil && usage.GrokLocalUsageMonthly != nil {
			usage.ThirtyDay.WindowStats = usage.GrokLocalUsageMonthly
		}
	}

	s.options.Grok.Enrich(usage, provider)
	return usage, nil
}

// GrokLocalUsageForQuota chooses billing windows when upstream quota is known, or rolling free-tier usage
// otherwise.
func GrokLocalUsageForQuota(
	ctx context.Context,
	repo LocalUsageStats,
	providerID int64,
	billing *usageview.BillingSummary,
	now time.Time,
	warn func(string, ...any),
) (*WindowStats, *WindowStats, *WindowStats) {
	if GrokBillingHasAuthoritativeQuota(billing) {
		weekly, monthly := GrokLocalUsageForBilling(ctx, repo, providerID, billing, now, warn)
		return nil, weekly, monthly
	}
	return GrokLocalUsage24h(ctx, repo, providerID, now, warn), nil, nil
}

// GrokLocalUsage24h queries local usage starting 24 hours before now.
func GrokLocalUsage24h(ctx context.Context, repo LocalUsageStats, providerID int64, now time.Time, warn func(string, ...any)) *WindowStats {
	if repo == nil || providerID <= 0 {
		return nil
	}
	start := now.UTC().Add(-OAuthUsageGrokFreeQuotaWindow)
	stats, err := repo.GetProviderWindowStats(ctx, providerID, start)
	if err != nil {
		warn("grok_rolling_24h_usage_query_failed", "provider_id", providerID, "window_start", start, "error", err)
		return nil
	}
	return normalizedLocalWindowStats(stats)
}

func GrokLocalUsageForBilling(
	ctx context.Context,
	repo LocalUsageStats,
	providerID int64,
	billing *usageview.BillingSummary,
	now time.Time,
	warn func(string, ...any),
) (*WindowStats, *WindowStats) {
	var weekly *WindowStats
	var monthly *WindowStats
	if repo == nil || providerID <= 0 {
		return weekly, monthly
	}
	if start, ok := CurrentGrokBillingWindow(billing, true, now); ok {
		if stats, err := repo.GetProviderWindowStats(ctx, providerID, start); err == nil {
			weekly = normalizedLocalWindowStats(stats)
		} else {
			warn("grok_window_usage_query_failed", "provider_id", providerID, "window_start", start, "error", err)
		}
	}
	if start, ok := CurrentGrokBillingWindow(billing, false, now); ok {
		if stats, err := repo.GetProviderWindowStats(ctx, providerID, start); err == nil {
			monthly = normalizedLocalWindowStats(stats)
		} else {
			warn("grok_monthly_usage_query_failed", "provider_id", providerID, "window_start", start, "error", err)
		}
	}
	return weekly, monthly
}

func CurrentGrokBillingWindow(billing *usageview.BillingSummary, weekly bool, now time.Time) (time.Time, bool) {
	if billing == nil {
		return time.Time{}, false
	}
	startRaw, endRaw := billing.BillingPeriodStart, billing.BillingPeriodEnd
	if weekly {
		if billing.PeriodType != "weekly" {
			return time.Time{}, false
		}
		startRaw, endRaw = billing.PeriodStart, billing.PeriodEnd
	}
	start, startErr := ParseUsageTime(strings.TrimSpace(startRaw))
	end, endErr := ParseUsageTime(strings.TrimSpace(endRaw))
	if startErr != nil || endErr != nil || now.Before(start) || !now.Before(end) {
		return time.Time{}, false
	}
	return start, true
}

func GrokBillingSnapshotNeedsRefresh(provider *Record, now time.Time) bool {
	if provider == nil {
		return false
	}
	billing, err := ParseGrokBillingSnapshot(provider.Extra)
	if err != nil || billing == nil || billing.Partial || len(billing.FailedWindows) > 0 {
		return true
	}
	stamp := strings.TrimSpace(billing.UpdatedAt)
	if stamp == "" {
		stamp = strings.TrimSpace(billing.FetchedAt)
	}
	updatedAt, err := ParseUsageTime(stamp)
	return err != nil || now.Sub(updatedAt) >= OAuthUsageOpenAIProbeCacheTTL
}

func (s *OAuthUsageService) ShouldProbeGrokBilling(providerID int64, now time.Time, force bool) bool {
	if force || s == nil || s.cache == nil || providerID <= 0 {
		return true
	}
	if cached, ok := s.cache.LoadGrokProbe(providerID); ok {
		if ts, ok := cached.(time.Time); ok && now.Sub(ts) < OAuthUsageGrokProbeRetryTTL {
			return false
		}
	}
	s.cache.StoreGrokProbe(providerID, now)
	return true
}

// cloneGrokUsageProbe copies a shared probe result, retaining nil statistics and billing fields.
func cloneGrokUsageProbe(value *GrokUsageProbe) *GrokUsageProbe {
	if value == nil {
		return nil
	}
	out := *value
	out.Billing = usageview.CloneBillingSummary(value.Billing)
	out.LocalUsage24h = clonePointer(value.LocalUsage24h)
	out.LocalUsage7d = clonePointer(value.LocalUsage7d)
	out.LocalUsageMonthly = clonePointer(value.LocalUsageMonthly)
	return &out
}
