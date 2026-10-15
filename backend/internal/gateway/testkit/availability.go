package testkit

import (
	"context"

	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// availability.go converts execution-provider fixture results into provider
// records for availability diagnostics. The adapter uses the production record
// conversion for each returned candidate, retaining the source query filters
// and treating a nil candidate collection as absent results for the diagnostic
// caller.

// AvailabilityStore converts execution-store fixtures into provider records
// for diagnostics.
type AvailabilityStore struct {
	Source gatewayadapter.ExecutionProviderStore // execution-provider fixture query source
}

// ListModelAvailabilityCandidates delegates the group and platform filters and
// converts successful execution candidates through ExecutionRecord. Source
// errors return immediately; a nil source collection remains nil in the
// diagnostic result.
func (s AvailabilityStore) ListModelAvailabilityCandidates(ctx context.Context, group *int64, platforms []string, grouped bool) ([]provider.Record, error) {
	values, err := s.Source.ListModelAvailabilityCandidates(ctx, group, platforms, grouped)
	if err != nil {
		return nil, err
	}
	if values == nil {
		return nil, nil
	}
	out := make([]provider.Record, len(values))
	for i := range values {
		out[i] = *gatewayadapter.ExecutionRecord(&values[i])
	}
	return out, nil
}
