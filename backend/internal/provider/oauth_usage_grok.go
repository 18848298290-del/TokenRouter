package provider

import (
	"context"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider/usageview"
)

// oauth_usage_grok.go combines Grok billing probes, stored quota observations,
// and local usage windows.
// Grok quota display can use authoritative billing windows or rolling
// free-account local usage. Billing authority is decided from current summary
// fields, while freshness and retry timestamps choose whether an OAuth account
// is probed before its display is built.
//
// Probe and display results are copied before mutation, but the supplied
// provider Extra is updated locally with a copied billing observation. Forced
// probe errors propagate; ordinary probe errors fall back to stored data.
// Local statistics failures retain whichever independent billing windows were
// observed, and those totals attach to matching weekly/monthly progress. Force
// bypasses the Grok retry gate without recording a new timestamp; ordinary
// admission records before querying and retains that reservation even on
// failure. Callers synchronize mutable provider access and retain reached
// build, enrichment, and statistics dependencies.

// GrokUsageBillingExtraKey identifies the copied upstream billing observation
// stored locally in provider Extra for subsequent account display and
// freshness decisions.
const GrokUsageBillingExtraKey = "grok_billing_snapshot"

// GrokUsageProbe combines billing metadata with optional local totals from one
// account probe.
type GrokUsageProbe struct {
	// Billing, LocalUsage24h, LocalUsage7d, and LocalUsageMonthly may be shared
	// by a probe implementation. cloneGrokUsageProbe copies their mutable data
	// before the service assigns or edits display observations.
	Billing *usageview.BillingSummary // upstream billing account summary
	// LocalUsage24h: rolling daily totals; LocalUsage7d: weekly billing totals;
	// LocalUsageMonthly: monthly billing totals.
	LocalUsage24h, LocalUsage7d, LocalUsageMonthly *WindowStats
}

// GrokUsageOptions binds display availability, probe activity, construction,
// and account enrichment.
type GrokUsageOptions struct {
	// Available optionally qualifies the display service. Missing or false
	// availability returns a dated empty view before probe or statistics activity.
	Available func() bool
	// StatsAvailable optionally enables local queries when it returns true.
	// That branch requires a usable service statistics reader, while false
	// retains probe totals.
	StatsAvailable func() bool
	// Probe optionally queries eligible OAuth accounts and may return shared
	// results. Force propagates its errors; ordinary calls retain stored billing
	// display.
	Probe func(context.Context, int64) (*GrokUsageProbe, error)
	// Build is required after availability and returns nonnil display data.
	// The service copies that result before adding snapshot state or local totals.
	Build func(*Record) *UsageInfo
	// Enrich is required after display construction and mutates its copied view.
	// The borrowed provider remains stable through any account-specific
	// enrichment reads.
	Enrich func(*UsageInfo, *Record)
}

// GetGrokUsage builds copied usage data for a stable provider observation.
// Missing or false availability returns a dated empty view; reached Build must
// return nonnil data and Enrich is required.
//
// Eligible stale/forced OAuth records may probe billing, whose usable result
// is copied into provider Extra. Forced errors abort, ordinary errors keep
// stored data, and nil successful probe data contributes nothing. Statistics
// callbacks require a usable LocalUsageStatistics reader when enabled.
// Probe-provided local totals take precedence over separate billing-window
// reads, then weekly/monthly totals attach to their matching progress.
// Provider Extra mutation remains caller-owned while returned/probe display
// fields are independent copies.
func (s *OAuthUsageService) GetGrokUsage(ctx context.Context, provider *Record, force bool) (*UsageInfo, error) {
	if s.options.Grok.Available == nil || !s.options.Grok.Available() {
		now := s.options.Now()
		return &UsageInfo{UpdatedAt: &now}, nil
	}
	var billingProbeResult *GrokUsageProbe
	if provider != nil && provider.IsGrokOAuth() && s.options.Grok.Probe != nil && (force || GrokBillingSnapshotNeedsRefresh(provider, s.options.Now())) && s.ShouldProbeGrokBilling(provider.ID, s.options.Now(), force) {
		result, err := s.options.Grok.Probe(ctx, provider.ID)
		// Forced failure is caller-visible; ordinary failure continues from
		// stored billing data. Ordinary admission reserves its timestamp before
		// this result decision, and force skips that reservation.
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
			// Today totals are best effort and leave existing built display data
			// intact when lookup fails or returns nil. Other billing-window totals
			// can still be attached independently below.
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
		// Attach each local billing observation to its corresponding quota window.
		// Missing statistics leave progress unannotated so absent data is not shown
		// as a measured zero total.
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

// GrokLocalUsageForQuota returns billing-window totals when the summary meets
// GrokBillingHasAuthoritativeQuota, otherwise rolling daily totals. These
// paths are exclusive, and invalid inputs or individual read failures can
// leave their selected observations nil.
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

// GrokLocalUsage24h returns copied totals starting exactly twenty-four hours
// before UTC now. Nil repository or nonpositive row ID skips work; query
// failure requires Warn and returns nil, while successful nil totals normalize
// to zero.
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

// GrokLocalUsageForBilling independently queries valid weekly and monthly
// ranges for a stable billing summary. Missing reader or invalid row ID
// returns nil observations, and reached warning paths require warn.
//
// CurrentGrokBillingWindow supplies each valid origin only while now is within
// that range. Successful nil totals normalize to copied zeros, while one query
// error leaves its observation absent and allows the other window query to
// continue. Callers therefore retain partial billing statistics without
// treating an unavailable window as observed zero usage.
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

// CurrentGrokBillingWindow returns a valid start containing now within the
// parsed half-open range. Nil summary, invalid timestamps, or time outside the
// range reports false.
//
// Weekly selection requires the exact weekly PeriodType and uses
// PeriodStart/PeriodEnd. Monthly selection uses
// BillingPeriodStart/BillingPeriodEnd independently of that label. Both text
// endpoints are trimmed and parsed before comparison, so missing or malformed
// dates cannot supply a local statistics origin. This helper preserves the
// selected range identity while the caller chooses which quota window receives
// those totals.
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

// GrokBillingSnapshotNeedsRefresh reports missing, malformed, partial,
// failed-window, or ten-minute-old billing observations. Nil record returns
// false, while complete parsed data younger than that limit remains fresh.
//
// UpdatedAt has precedence over FetchedAt when nonblank, so invalid preferred
// text requests refresh instead of trying the fallback timestamp. Future
// timestamps remain fresh under elapsed-time comparison. This decision
// describes observation freshness; ShouldProbeGrokBilling separately reserves
// ordinary retry admission before upstream work begins.
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

// ShouldProbeGrokBilling admits a forced probe immediately without storing a
// reservation. Nil service/cache or nonpositive ID also admits directly.
//
// Ordinary calls reject a typed timestamp younger than one minute, otherwise
// store now before querying. Failed probes retain that timestamp, limiting
// later ordinary attempts. Load and Store are separate synchronized map
// operations, so simultaneous callers can both admit; the method is an
// observation gate rather than an atomic single-owner claim. Future timestamps
// can extend its rejection interval.
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

// cloneGrokUsageProbe copies billing metadata and all three scalar local
// totals. Nil fields stay nil, and callers keep the source stable during
// copying before independently editing the returned result.
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
