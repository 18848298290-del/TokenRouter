package provider

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/provider/usageview"
)

// Grok billing snapshots can enter Extra as typed summaries or serialized
// values. Parsing detaches direct summary references and decodes other values
// into fresh summary data, while preserving errors for the query caller's
// display and recovery handling.
//
// Probe selection distinguishes absent signals from supplied zero percentages.
// A populated percentage still describes usage at zero, while a zero monthly
// limit alone supplies no accepted quota signal. Extra merging also distinguishes
// an empty update from a populated patch: empty updates borrow the current map,
// and patches copy the container forms supported by CloneValues.

// ParseGrokBillingSnapshot decodes a retained billing summary from Extra using
// its stored Go representation. Absent values, nil interfaces, and typed-nil
// BillingSummary pointers return no summary; direct summaries are cloned before
// returning.
//
// Map and other representations are serialized and decoded into a fresh
// summary. A typed-nil map or another value encoded as null decodes to a nonnil
// zero summary, so that result differs from an absent key. JSON encoding and
// decoding errors are returned for the caller's recovery or display decision;
// every successful populated result has separate summary pointers and slices.
func ParseGrokBillingSnapshot(extra map[string]any) (*usageview.BillingSummary, error) {
	if extra == nil {
		return nil, nil
	}
	raw, ok := extra[GrokUsageBillingExtraKey]
	if !ok || raw == nil {
		return nil, nil
	}
	switch snapshot := raw.(type) {
	case *usageview.BillingSummary:
		return usageview.CloneBillingSummary(snapshot), nil
	case usageview.BillingSummary:
		return usageview.CloneBillingSummary(&snapshot), nil
	case map[string]any:
		data, err := json.Marshal(snapshot)
		if err != nil {
			return nil, err
		}
		var out usageview.BillingSummary
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		return &out, nil
	default:
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("marshal grok billing snapshot: %w", err)
		}
		var out usageview.BillingSummary
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
}

// MergeUsageExtra applies updates to copies of container types supported by
// CloneValues. Empty updates return Extra itself. A populated patch creates a
// new top-level map; unsupported values pass through unchanged, including
// pointers and mutable references inside structs. Copy shared values before
// modifying them independently.
func MergeUsageExtra(extra, updates map[string]any) map[string]any {
	if len(updates) == 0 {
		return extra
	}
	out := CloneValues(extra)
	if out == nil {
		out = make(map[string]any, len(updates))
	}
	maps.Copy(out, CloneValues(updates))
	return out
}

// GrokBillingHasAuthoritativeQuota reports whether billing supplies an accepted
// quota signal for probe selection. Nil summaries return false, and either
// populated percent pointer counts even when its value is zero.
//
// A positive MonthlyLimitCents or a nonblank plan also supplies a signal. Zero
// monthly limits and blank plans alone do not select billing as authoritative,
// while absent percentages leave those remaining signals to decide. The check
// identifies presence for the Grok quota caller; it does not validate
// percentage bounds or assert freshness of the stored observation.
func GrokBillingHasAuthoritativeQuota(billing *usageview.BillingSummary) bool {
	if billing == nil {
		return false
	}
	return billing.UsagePercent != nil ||
		billing.UsedPercent != nil ||
		(billing.MonthlyLimitCents != nil && *billing.MonthlyLimitCents > 0) ||
		strings.TrimSpace(billing.Plan) != ""
}
