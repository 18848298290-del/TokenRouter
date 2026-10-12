package usageview

import (
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// Grok billing and rolling quota observations use the upstream usage display
// types throughout the provider module. These aliases keep the JSON
// representation shared with upstream decoding, while copying delegates to
// the same helpers so consumers can mutate returned windows or summaries
// independently.

// BillingSummary and QuotaWindow describe billing and quota windows returned
// by upstream usage queries.
type (
	// BillingSummary aliases the shared normalized Grok billing observation type.
	BillingSummary = usageview.BillingSummary
	// QuotaWindow aliases the shared nullable Grok rolling quota window type.
	QuotaWindow = usageview.QuotaWindow
)

// CloneQuotaWindow copies a quota observation and each of its pointed-to
// values using the shared upstream helper. Nil input returns nil, so callers
// can handle an absent window without constructing a placeholder or sharing
// mutable counters.
func CloneQuotaWindow(value *QuotaWindow) *QuotaWindow { return usageview.CloneQuotaWindow(value) }

// CloneBillingSummary copies the billing observation, its slices, and scalar
// pointers through the upstream helper. Nil remains nil and nil/empty slice
// distinctions are retained; each product's usage-percent pointer is also
// copied for independent edits.
func CloneBillingSummary(value *BillingSummary) *BillingSummary {
	return usageview.CloneBillingSummary(value)
}
