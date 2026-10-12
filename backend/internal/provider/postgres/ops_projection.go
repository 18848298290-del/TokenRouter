package postgres

import (
	"context"
	"time"

	dbprovider "github.com/TokenFlux/TokenRouter/ent/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// ListSchedulableProviderLoads loads IDs, concurrency, and load factors for Ops queue-depth sampling.
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
