package usageview

import (
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// BillingSummary and QuotaWindow describe billing and quota windows returned by upstream usage queries.
type (
	BillingSummary = usageview.BillingSummary
	QuotaWindow    = usageview.QuotaWindow
)

// CloneQuotaWindow copies a quota window.
func CloneQuotaWindow(value *QuotaWindow) *QuotaWindow { return usageview.CloneQuotaWindow(value) }

// CloneBillingSummary copies a billing summary.
func CloneBillingSummary(value *BillingSummary) *BillingSummary {
	return usageview.CloneBillingSummary(value)
}
