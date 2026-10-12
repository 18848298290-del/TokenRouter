package testkit

import (
	"context"

	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// AvailabilityStore converts execution-store fixtures into provider records for diagnostics.
type AvailabilityStore struct {
	Source gatewayadapter.ExecutionProviderStore
}

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
