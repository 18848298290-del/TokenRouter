package provider

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/provider/usageview"
)

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

// MergeUsageExtra clones JSON containers and applies updates; with no updates it returns extra unchanged.
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

func GrokBillingHasAuthoritativeQuota(billing *usageview.BillingSummary) bool {
	if billing == nil {
		return false
	}
	return billing.UsagePercent != nil ||
		billing.UsedPercent != nil ||
		(billing.MonthlyLimitCents != nil && *billing.MonthlyLimitCents > 0) ||
		strings.TrimSpace(billing.Plan) != ""
}
