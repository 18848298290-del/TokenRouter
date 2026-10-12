package postgres

import (
	"context"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// UpdateConfiguration uses updateProvider's transaction for configuration changes and the outbox write.
func (r *ProviderStore) UpdateConfiguration(ctx context.Context, value *acctcore.Record, change acctcore.ConfigurationChange) error {
	return r.updateProvider(ctx, value, &change)
}

func (r *ProviderStore) lockConfigurationRecord(ctx context.Context, client *dbent.Client, id int64) (*acctcore.Record, error) {
	rows, err := client.QueryContext(ctx, "SELECT id FROM providers WHERE id=$1 AND deleted_at IS NULL FOR NO KEY UPDATE", id)
	if err != nil {
		return nil, err
	}
	found := rows.Next()
	scanErr := rows.Err()
	if found {
		var locked int64
		scanErr = rows.Scan(&locked)
	}
	closeErr := rows.Close()
	if scanErr != nil {
		return nil, scanErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if !found {
		return nil, acctcore.ErrProviderNotFound
	}
	entity, err := client.Provider.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return r.recordFromEntity(entity), nil
}
