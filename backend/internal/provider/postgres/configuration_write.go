package postgres

import (
	"context"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// configuration_write.go delegates declared configuration updates and locks
// current provider records for merging.
// Provider edits declare the fields they intend to change so an old
// administrator read does not replace current credentials or health data.
// UpdateConfiguration delegates transaction handling to the shared mutation
// path, which reads the latest row under a PostgreSQL lock before merging
// those fields.

// UpdateConfiguration merges the declared configuration fields into the
// current locked provider record through updateProvider. A nil value succeeds
// immediately; the shared path owns its transaction or participates in the
// Ent transaction carried by ctx, including the outbox write.
func (r *ProviderStore) UpdateConfiguration(ctx context.Context, value *acctcore.Record, change acctcore.ConfigurationChange) error {
	return r.updateProvider(ctx, value, &change)
}

// lockConfigurationRecord locks a nondeleted provider before reading its
// complete Ent record. Callers supply the transaction client and retain its
// transaction until the merged write finishes; an absent row returns
// ErrProviderNotFound, and scan or explicit close errors stop the read.
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
	// Finish the lock query before the Ent row read reuses the transaction connection;
	// closing the result set retains the row lock until transaction completion.
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
