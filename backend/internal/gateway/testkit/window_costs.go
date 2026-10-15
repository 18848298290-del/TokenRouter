package testkit

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// window_costs.go converts usage fixture statistics into billing window costs
// while retaining optional batch reads. Production billing checks the adapter's
// method set to decide whether it can obtain several provider observations
// together.
//
// The wrapper retains the optional batch interface when present and otherwise
// exposes individual reads. Converting results keeps nil collections and
// individual missing entries visible to the billing cache's absence handling,
// including when an error accompanies returned data. Nonnil results receive new
// billing map or struct storage, with StandardCost copied from each usage
// observation. A nil source returns a nil interface so unavailable fixture
// storage stays distinguishable from an empty observation.

// windowCosts converts one usage-provider window observation into billing cost
// statistics.
type windowCosts struct {
	source usage.UsageLogRepository // retained usage window fixture reader
}

// windowCostBatch identifies usage fixtures that can read several provider
// windows together.
type windowCostBatch interface {
	// GetProviderWindowStatsBatch returns provider observations from the supplied
	// window start, retaining missing entries and an accompanying read error for
	// the billing adapter to pass through.
	GetProviderWindowStatsBatch(context.Context, []int64, time.Time) (map[int64]*usage.ProviderStats, error)
}

// windowCostsBatch adds retained batch reads to the single-window fixture
// adapter.
type windowCostsBatch struct {
	windowCosts                 // converted individual provider window reads
	batch       windowCostBatch // retained batch window fixture capability
}

// WindowCosts adapts usage window reads while retaining an optional batch
// capability for billing type assertions. A nil source returns nil, and nonnil
// wrappers retain their fixture interfaces for later reads.
//
// The source's batch method is detected when the adapter is built. A matching
// source returns windowCostsBatch with both read forms; another source returns
// the single-window adapter. Conversion later preserves missing observations
// and accompanying errors, allowing the billing cache to choose its own
// absence or retry handling instead of treating adaptation as a successful
// zero-cost observation.
func WindowCosts(source usage.UsageLogRepository) billing.WindowCostSource {
	if source == nil {
		return nil
	}
	base := windowCosts{source}
	if batch, ok := source.(windowCostBatch); ok {
		return windowCostsBatch{windowCosts: base, batch: batch}
	}
	return base
}

// GetWindow copies StandardCost from the source usage observation into billing
// window statistics. A nil source observation remains nil, and any
// accompanying error is returned with the converted result.
func (s windowCosts) GetWindow(ctx context.Context, id int64, start time.Time) (*billing.WindowCostStats, error) {
	value, err := s.source.GetProviderWindowStats(ctx, id, start)
	if value == nil {
		return nil, err
	}
	return &billing.WindowCostStats{StandardCost: value.StandardCost}, err
}

// GetWindows converts a batch observation into newly allocated billing map and
// value storage. Nil collections and individual nil entries remain nil, and
// source errors stay paired with the returned observations.
func (s windowCostsBatch) GetWindows(ctx context.Context, ids []int64, start time.Time) (map[int64]*billing.WindowCostStats, error) {
	values, err := s.batch.GetProviderWindowStatsBatch(ctx, ids, start)
	if values == nil {
		return nil, err
	}
	out := make(map[int64]*billing.WindowCostStats, len(values))
	for id, value := range values {
		if value == nil {
			out[id] = nil
		} else {
			out[id] = &billing.WindowCostStats{StandardCost: value.StandardCost}
		}
	}
	return out, err
}
