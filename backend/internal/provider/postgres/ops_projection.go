package postgres

import (
	"context"
	"time"

	dbprovider "github.com/TokenFlux/TokenRouter/ent/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// ops_projection.go reads schedulable provider capacities for Ops queue
// sampling.
// Ops queue sampling uses eligible provider IDs and their effective capacity
// denominator, selected from concurrency and the optional load factor. The
// shared scheduling query applies persistent eligibility and current pause
// windows; selected concurrency and load-factor fields then use the
// provider's own capacity rule for the returned observations.

// ListSchedulableProviderLoads returns eligible provider IDs and the capacity
// denominator used by Ops queue-depth sampling. The shared query evaluates
// scheduling windows at the current wall clock; Record.EffectiveLoadFactor
// chooses a positive load factor before concurrency.
func (r *ProviderStore) ListSchedulableProviderLoads(ctx context.Context) ([]providercore.LoadObservation, error) {
	providers, err := r.SchedulableProvidersQuery(time.Now()).
		Select(
			dbprovider.FieldID,
			dbprovider.FieldConcurrency,
			dbprovider.FieldLoadFactor,
		).
		All(ctx)
	if err != nil {
		return nil, err
	}

	loads := make([]providercore.LoadObservation, 0, len(providers))
	for _, provider := range providers {
		projection := providercore.Record{
			ID:          provider.ID,
			Concurrency: provider.Concurrency,
			LoadFactor:  provider.LoadFactor,
		}
		loads = append(loads, providercore.LoadObservation{
			ID:             provider.ID,
			MaxConcurrency: projection.EffectiveLoadFactor(),
		})
	}
	return loads, nil
}
