package provider

import "context"

// credential_persistence.go persists ordinary credential replacements through
// field-specific or whole-record storage.
// Ordinary credential writes can use a field-specific store or a whole-record
// store, while Spark shadows keep credentials with their parent.
// PersistCredentials replaces the caller record before storage and reports that
// mutation even when the write fails; refresh comparison is supplied separately
// by CredentialRefreshWriter.

// CredentialUpdateStore persists a caller-supplied provider record for ordinary
// configuration updates.
type CredentialUpdateStore interface {
	// Update saves the supplied record for ordinary configuration persistence.
	// PersistCredentials calls this whole-record path when the store has no
	// CredentialFieldsUpdater implementation; the store controls how the supplied
	// context affects its I/O.
	Update(context.Context, *Record) error
}

// CredentialFieldsUpdater supplies the preferred field-specific path for
// ordinary credential persistence.
type CredentialFieldsUpdater interface {
	// UpdateCredentials replaces credentials for the selected provider using the
	// supplied context. PersistCredentials passes its newly copied credential map
	// and retains the mutation if this operation fails.
	UpdateCredentials(context.Context, int64, map[string]any) error
}

// PersistCredentials replaces value.Credentials and writes it through the store,
// preferring CredentialFieldsUpdater when implemented. A nil store, nil record,
// or credential shadow returns false without a write.
//
// The true result reports that the input record was changed, including when
// persistence returns an error. Nil credentials become an empty object, and
// CloneValues detaches its supported containers while retaining unsupported
// values. Callers synchronize access to value and keep borrowed values stable;
// refresh exchanges use CredentialRefreshWriter for identity comparisons.
func PersistCredentials(ctx context.Context, store CredentialUpdateStore, value *Record, credentials map[string]any, warn func(string, ...any)) (bool, error) {
	if store == nil || value == nil {
		return false, nil
	}
	if value.IsCredentialShadow() {
		if warn != nil {
			warn("skip persisting credentials to spark shadow provider", "provider_id", value.ID, "parent_id", *value.ParentProviderID)
		}
		return false, nil
	}
	value.Credentials = CloneValues(credentials)
	if value.Credentials == nil {
		value.Credentials = map[string]any{}
	}
	// A field-specific update avoids writing stale health or configuration fields
	// from the caller record; the whole-record store remains the compatibility
	// path.
	if updater, ok := store.(CredentialFieldsUpdater); ok {
		return true, updater.UpdateCredentials(ctx, value.ID, value.Credentials)
	}
	return true, store.Update(ctx, value)
}
