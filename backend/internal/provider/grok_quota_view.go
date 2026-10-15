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

// grok_quota_view.go builds Grok usage displays from billing, quota snapshots,
// and credential hints.
// Grok quota display combines official billing, rolling windows, and stored
// credential hints that can disagree in age or authority. Billing status
// initially supplies authentication or forbidden state; a successful active
// probe can clear billing's 403 when its accepted timestamp is equal or later.
// Other error and reauthorization signals retain their separate rules.
//
// Subscription tier prefers current access-token evidence, then credential,
// rolling snapshot, and billing hints through the platform resolver.
// Weekly/monthly percentages form progress windows while retaining local totals
// already attached to them. Typed quota snapshots can borrow nested pointers
// and maps, unlike JSON-decoded values; result cloning detaches those known
// mutable fields for shared probe consumers. Display construction itself can
// return borrowed rolling-window references, so callers copy before independent
// edits.

// GrokQuotaView supplies tier, time, and reauthorization rules for stored usage
// display.
type GrokQuotaView struct {
	FreeTokenLimit int64 // displayed free rolling token allowance
	// NeedsReauth, JWTSubscriptionTier, CanonicalPlan, and ParseTime are supplied
	// by the platform factory. Callers configure each reached callback; display
	// construction retains the provided free allowance and parsing rules.
	//
	// NeedsReauth is reached by BuildUsageInfo when GrokQuotaSnapshotFromExtra
	// returns a nonnil snapshot and no error. The missing or malformed snapshot
	// path returns before this credential-level check; billing status can already
	// mark renewal required.
	NeedsReauth         func(*Record) bool  // stored credential renewal predicate
	JWTSubscriptionTier func(string) string // current access-token tier parser
	// CanonicalPlan receives monthly limit cents, the selected signal, and the
	// rolling snapshot. A resolved name can replace display tier while an existing
	// raw label remains retained.
	// billing and snapshot tier resolver
	CanonicalPlan func(*float64, string, *upstreamusageview.QuotaSnapshot) string
	ParseTime     func(string) (time.Time, error) // billing period end parser
}

// CloneGrokQuotaProbeResult copies quota windows, their numeric pointers,
// headers, billing data, and local scalar totals into separate storage. Nil
// stays nil; shared-flight callers can independently edit these known mutable
// result fields.
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

// cloneGrokProbePointer allocates a shallow pointed-value copy, or returns nil
// for nil input. References contained inside T remain borrowed, so callers
// detach them separately before promising an independent compound result.
func cloneGrokProbePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

// BuildUsageInfo constructs Grok display data from supplied billing, rolling
// quota, and credential observations. Nil providers return an unknown-quota
// response, and malformed or missing stored sources contribute only the
// remaining accepted data. Nil or undecodable rolling snapshots return billing
// and tier data before the credential-level NeedsReauth callback is reached.
//
// Billing status and progress are applied first, with an equal-or-later
// successful active probe able to clear a billing 403. Snapshot status, header
// presence, spending-limit flags, and credential tier then fill their
// corresponding display decisions in order. The returned rolling-window and
// retry pointers may be borrowed from typed snapshot storage; callers use
// copying helpers before independent mutation. This builder interprets the
// supplied observations without freshness or identity validation of every
// source.
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

	// Billing decode failure omits that source while retained rolling quota
	// and credential hints can still build display data; it does not fail the
	// administrator's usage response.
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
		// A parseable billing time replaces the display construction time.
		// Malformed timestamp text leaves that initial time available while the
		// remaining billing fields still contribute their observations.
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
		// This condition targets quota_unknown alongside known billing progress.
		// Earlier initialization and billing status handling currently leave
		// that code unset here, so this retained clearing branch is dormant.
		if usage.ErrorCode == "quota_unknown" && (usage.SevenDay != nil || usage.ThirtyDay != nil) {
			usage.ErrorCode = ""
			if strings.Contains(strings.ToLower(usage.Error), "unknown until") ||
				strings.Contains(strings.ToLower(usage.Error), "no xai quota headers") {
				usage.Error = ""
			}
		}
	}

	// Unusable rolling data leaves official billing and credential hints
	// available. Only the absence of billing also creates the unknown-quota
	// response on this path.
	if err != nil || snapshot == nil {
		f.ApplyGrokCredentialUsageFallback(usage, provider, billing, nil)
		if billing == nil {
			usage.ErrorCode = "quota_unknown"
			usage.Error = "Grok quota is unknown until billing is probed or an upstream response includes xAI rate-limit headers"
		}
		return usage
	}

	// A snapshot can supply rolling quota alongside a newer billing time. Keep
	// the newer display timestamp when snapshot parsing fails or its update is
	// older, while still returning the available rolling fields below.
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
	// Accepted active evidence supersedes billing access rejection at its
	// observation time. Clear the forbidden markers with their diagnostic and
	// status before later snapshot rules select remaining display errors.
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
	// Credential reauthorization can remain required after successful upstream
	// observation. Retain any existing billing or quota diagnostic while marking
	// renewal, then use spending_limit when no other error was selected.
	if f.NeedsReauth(provider) {
		usage.NeedsReauth = true
		if usage.ErrorCode == "" {
			usage.ErrorCode = "spending_limit"
		}
	}
	f.ApplyGrokCredentialUsageFallback(usage, provider, billing, snapshot)
	// Credential fallback can restore stored forbidden entitlement after the
	// active status clears billing. Remove that stale label when the successful
	// snapshot supplied no replacement entitlement.
	if activeProbeClearsForbidden && strings.TrimSpace(snapshot.EntitlementStatus) == "" &&
		strings.EqualFold(strings.TrimSpace(usage.GrokEntitlementStatus), "forbidden") {
		usage.GrokEntitlementStatus = ""
	}
	return usage
}

// NewerSuccessfulGrokActiveProbeClearsBillingForbidden reports whether an
// active_probe status 200 supersedes a billing status 403 at an equal or later
// parsed instant. Other status/source pairs and missing usable timestamps fail.
//
// Billing prefers UpdatedAt before FetchedAt, while the probe prefers
// LastProbeAt before UpdatedAt. Those priorities select the first parseable
// value, rather than the newest among every supplied field. Equality is
// accepted for observations stamped at second precision, so two phases with the
// same encoded second can clear the older forbidden display. This rule
// interprets timestamp evidence and does not independently prove within-second
// event order.
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

// FirstGrokObservationTime returns the first trimmed RFC3339 timestamp that
// parses successfully. Invalid earlier entries are skipped in supplied order,
// and wholly unusable input returns a zero time with false.
func FirstGrokObservationTime(values ...string) (time.Time, bool) {
	// Timestamp order expresses the caller's observation priority. Skip malformed
	// earlier text so the next accepted field can still establish comparison
	// time.
	for _, value := range values {
		parsedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
		if err == nil {
			return parsedAt, true
		}
	}
	return time.Time{}, false
}

// ApplyGrokCredentialUsageFallback fills missing entitlement text from stored
// credentials and then applies tier resolution. Nil display or provider skips;
// existing entitlement text retains precedence, while tier has its separate
// source-selection rules.
func (f GrokQuotaView) ApplyGrokCredentialUsageFallback(usage *UsageInfo, provider *Record, billing *upstreamusageview.BillingSummary, snapshot *upstreamusageview.QuotaSnapshot) {
	if usage == nil || provider == nil {
		return
	}
	if usage.GrokEntitlementStatus == "" {
		usage.GrokEntitlementStatus = strings.TrimSpace(provider.GetCredential("entitlement_status"))
	}
	f.ApplyGrokResolvedSubscriptionTier(usage, provider, billing, snapshot)
}

// ApplyGrokResolvedSubscriptionTier mutates display tier using a current JWT
// tier before credential, snapshot, and billing hints. Nil inputs skip, and the
// first nonblank hint supplies the platform resolver alongside monthly limit
// and snapshot.
//
// A resolved plan replaces SubscriptionTier but preserves an already populated
// SubscriptionTierRaw. Empty raw text is filled from the selected hint or
// resolved name. If resolution yields nothing, a nonblank hint fills only an
// empty display tier. This keeps accepted normalized display text and the
// retained raw source label distinct when observations disagree.
func (f GrokQuotaView) ApplyGrokResolvedSubscriptionTier(usage *UsageInfo, provider *Record, billing *upstreamusageview.BillingSummary, snapshot *upstreamusageview.QuotaSnapshot) {
	if usage == nil || provider == nil {
		return
	}
	if jwtTier := f.JWTSubscriptionTier(provider.GetCredential("access_token")); jwtTier != "" {
		usage.SubscriptionTier = jwtTier
		usage.SubscriptionTierRaw = jwtTier
		return
	}
	// Stored credential text supplies the first hint when JWT parsing yields
	// none. Fill missing hints from snapshot and billing so partial observations
	// still reach the platform's plan resolver.
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

// GrokQuotaSnapshotFromExtra returns saved rolling quota using
// representation-specific ownership. Missing keys and nil values return no
// snapshot; a stored pointer is returned directly, while a stored value
// receives a shallow struct copy.
//
// Those typed results can still share windows, retry pointers, and headers. Map
// or other JSON-encodable forms decode into new storage and return conversion
// errors. A typed-nil map encoded as null can produce a nonnil zero snapshot,
// unlike absence; callers interpret observed fields and copy borrowed typed
// results before mutation. This decoder adds no timestamp or quota-value
// validation.
func GrokQuotaSnapshotFromExtra(extra map[string]any) (*upstreamusageview.QuotaSnapshot, error) {
	if extra == nil {
		return nil, nil
	}
	raw, ok := extra[GrokQuotaSnapshotExtraKey]
	if !ok || raw == nil {
		return nil, nil
	}
	// Typed snapshots reuse their window storage; JSON-shaped observations need
	// decoding into the current schema. Select the representation before
	// conversion so typed callers retain the documented borrowing rules.
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

// ApplyGrokBillingProgressWindows fills weekly and monthly display progress
// from billing while retaining each existing window's local totals. Nil inputs
// skip. Weekly uses supplied UsagePercent; monthly prefers UsedPercent before
// deriving used cents divided by a positive limit.
//
// Each usable period end supplies reset and positive whole-second countdown,
// while failed parsing leaves utilization available. Monthly period selection
// prefers BillingPeriodEnd and uses PeriodEnd only for monthly summaries when
// that field is empty. Percentages are retained or calculated without display
// clamping, so callers can show over-limit usage as supplied.
func (f GrokQuotaView) ApplyGrokBillingProgressWindows(usage *UsageInfo, billing *upstreamusageview.BillingSummary, now time.Time) {
	if usage == nil || billing == nil {
		return
	}
	if billing.UsagePercent != nil {
		seven := &UsageProgress{Utilization: *billing.UsagePercent}
		// An unusable weekly period end cannot supply reset timing. Retain its
		// percentage so partial billing still produces meaningful utilization.
		if end, err := f.ParseTime(strings.TrimSpace(billing.PeriodEnd)); err == nil {
			seven.ResetsAt = &end
			// Countdown remains zero for expired or subsecond remaining time.
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
		// Convert spending ratio to the percentage expected by UsageProgress.
		v := (*billing.UsedCents / *billing.MonthlyLimitCents) * 100
		monthlyUtil = &v
	}
	if monthlyUtil != nil {
		thirty := &UsageProgress{Utilization: *monthlyUtil}
		endRaw := strings.TrimSpace(billing.BillingPeriodEnd)
		// Primary dates can describe a monthly-only summary when its dedicated
		// billing date is absent. A weekly primary end belongs to a different
		// quota window and cannot supply this monthly countdown.
		if endRaw == "" && billing.PeriodType == "monthly" {
			endRaw = strings.TrimSpace(billing.PeriodEnd)
		}
		// Monthly utilization stays available when period-end parsing fails;
		// omit reset/countdown fields instead of discarding an otherwise usable
		// spending observation.
		if end, err := f.ParseTime(endRaw); err == nil {
			thirty.ResetsAt = &end
			// Countdown remains zero for expired or subsecond remaining time.
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

// StampGrokQuotaPlan fills an absent snapshot model from a nonblank supplied
// model and passes current/previous snapshots to applySignal. Nil snapshots
// skip; previous decode errors supply nil so new observation stamping can
// continue.
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
		// A malformed earlier snapshot contributes no plan signal. Keep stamping
		// the current observation so bad retained metadata cannot prevent its
		// accepted model/tier update.
		previous, _ = GrokQuotaSnapshotFromExtra(record.Extra)
	}
	applySignal(snapshot, previous)
}
