package provider

import "context"

type CredentialUpdateStore interface {
	Update(context.Context, *Record) error
}
type CredentialFieldsUpdater interface {
	UpdateCredentials(context.Context, int64, map[string]any) error
}

// PersistCredentials skips shadows, mutates the input record, then persists credentials; its bool reports input mutation.
// Refresh callers use conditional writes to compare exchange identity; ordinary configuration callers can use this field-specific writer.
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
	if updater, ok := store.(CredentialFieldsUpdater); ok {
		return true, updater.UpdateCredentials(ctx, value.ID, value.Credentials)
	}
	return true, store.Update(ctx, value)
}
