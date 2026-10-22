package app

import (
	"context"
	"errors"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// provider_usage_statistics.go adapts local usage totals and optional batch
// reads for provider quota displays. Provider cost and user charges remain
// separate fields in the converted totals. Paired-window and batch capabilities
// are preserved when the usage source supports them, with missing pair support
// returned as an error so provider-owned querying can fall back to individual
// windows.

// providerLocalStats adapts local usage records for provider quota display
// totals.
type providerLocalStats struct {
	source usage.UsageLogRepository // local usage totals
}

// providerLocalStatsPairSource supplies one combined read for two provider
// windows.
type providerLocalStatsPairSource interface {
	// GetProviderWindowStatsPair reads separate totals for the two supplied provider window starts.
	GetProviderWindowStatsPair(context.Context, int64, time.Time, time.Time) (*usage.ProviderStats, *usage.ProviderStats, error)
}

// providerLocalStatsBatchSource supplies one window read for multiple provider
// identities.
type providerLocalStatsBatchSource interface {
	// GetProviderWindowStatsBatch reads provider totals from one shared window start.
	GetProviderWindowStatsBatch(context.Context, []int64, time.Time) (map[int64]*usage.ProviderStats, error)
}

// providerLocalStatsBatch adds supported batch reads beside the individual
// usage adapter.
type providerLocalStatsBatch struct {
	// providerLocalStats and batch point to the same underlying usage source.
	// Construction exposes batch only after its capability assertion while retaining individual and pair reads.
	providerLocalStats                               // individual and paired reads
	batch              providerLocalStatsBatchSource // supported multi-provider reads
}

// GetProviderWindowStatsPair adapts the source's two-window query when
// supported. Otherwise it returns an unsupported-query error, letting the
// provider caller perform individual-window recovery; available results retain
// the source error after conversion.
func (r providerLocalStats) GetProviderWindowStatsPair(ctx context.Context, id int64, first, second time.Time) (*provider.WindowStats, *provider.WindowStats, error) {
	reader, ok := r.source.(providerLocalStatsPairSource)
	// The display caller owns separate-window fallback. Return a concrete error
	// when the source lacks combined querying so it can choose that recovery path.
	if !ok {
		return nil, nil, errors.New("usage reader does not support dual-window queries")
	}
	a, b, err := reader.GetProviderWindowStatsPair(ctx, id, first, second)
	return localWindowStats(a), localWindowStats(b), err
}

// localWindowStats converts scalar request, token, and cost totals, or returns
// nil for absence. Cost, standard cost, and user charge keep separate fields
// for the provider display consumer.
func localWindowStats(v *usage.ProviderStats) *provider.WindowStats {
	if v == nil {
		return nil
	}
	return &provider.WindowStats{Requests: v.Requests, Tokens: v.Tokens, Cost: v.Cost, StandardCost: v.StandardCost, UserCost: v.UserCost}
}

// GetProviderWindowStats narrows local usage for one provider and supplied
// window start.
func (r providerLocalStats) GetProviderWindowStats(ctx context.Context, id int64, start time.Time) (*provider.WindowStats, error) {
	v, err := r.source.GetProviderWindowStats(ctx, id, start)
	return localWindowStats(v), err
}

// GetProviderTodayStats narrows the source's current-day totals for provider
// display.
func (r providerLocalStats) GetProviderTodayStats(ctx context.Context, id int64) (*provider.WindowStats, error) {
	v, err := r.source.GetProviderTodayStats(ctx, id)
	return localWindowStats(v), err
}

// GetProviderWindowStatsBatch narrows supported multi-provider results using
// the supplied start. Nil source maps stay nil, and nil individual totals
// remain nil entries beside the original query error.
func (r providerLocalStatsBatch) GetProviderWindowStatsBatch(ctx context.Context, ids []int64, start time.Time) (map[int64]*provider.WindowStats, error) {
	values, err := r.batch.GetProviderWindowStatsBatch(ctx, ids, start)
	if values == nil {
		return nil, err
	}
	out := make(map[int64]*provider.WindowStats, len(values))
	for id, v := range values {
		out[id] = localWindowStats(v)
	}
	return out, err
}

// newProviderLocalUsageStats exposes batch capability when the source
// implements it. The source is retained for later reads, and the base adapter
// always exposes pair lookup so it can report unsupported combined querying to
// the provider caller.
func newProviderLocalUsageStats(source usage.UsageLogRepository) provider.LocalUsageStats {
	reader := providerLocalStats{source}
	if batch, ok := source.(providerLocalStatsBatchSource); ok {
		return providerLocalStatsBatch{reader, batch}
	}
	return reader
}
