package provider

import (
	"context"
	"log/slog"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// execution_credentials.go acquires tokens and writes credential updates back
// to execution targets.
// Token acquisition and credential persistence can update the request
// execution target before a storage operation succeeds. The adapters copy
// those credentials back onto the original target so later request preparation
// sees token-source backfills and the provider module's selected update path.
//
// Whole-record persistence assigns credentials to the loaded target before
// invoking storage, retaining its other configuration fields. A store with a
// credential-specific update method uses that narrower write. The provider
// persistence result reports mutation separately from failure, so changed
// credentials are copied back even when the write returns an error. Callers
// synchronize access to the execution target.

// providerCredentialsUpdater replaces credentials through a field-specific
// provider storage operation.
type providerCredentialsUpdater interface {
	// UpdateCredentials replaces the selected provider's persisted credential map.
	UpdateCredentials(context.Context, int64, map[string]any) error
}

// executionCredentialStore adapts whole-record writes to the original request
// execution target.
type executionCredentialStore struct {
	source ExecutionProviderStore // whole-record persistence destination
	// original is updated before storage is invoked, so a failed persistence call
	// retains the credential assignment on the caller's request execution target.
	original *ExecutionProvider
}

// executionCredentialFields adds field-specific credential writes to the
// execution store adapter.
type executionCredentialFields struct {
	// whole-record compatibility adapter
	executionCredentialStore
	// credential-field persistence destination
	updater providerCredentialsUpdater
}

// ExecutionTokenSource retrieves access tokens using provider-module records
// supplied by execution.
type ExecutionTokenSource interface {
	// GetAccessToken retrieves an access token using the supplied provider record.
	// Token-source backfills may update its credentials, which ExecutionToken
	// transfers to the request target after the call, including error returns.
	GetAccessToken(context.Context, *provider.Record) (string, error)
}

// Update assigns the supplied credential map to the original execution target,
// then writes that target through source. A persistence error leaves the
// in-memory assignment visible; callers synchronize access to the shared target.
func (s executionCredentialStore) Update(ctx context.Context, value *provider.Record) error {
	// The core supplies a credential-only record. Update the original target first
	// so whole-record storage receives its other provider fields as well.
	s.original.Record.Credentials = value.Credentials
	return s.source.Update(ctx, s.original)
}

// UpdateCredentials assigns credentials to the original target and invokes the
// field-specific updater for id. The assignment remains visible even if
// storage fails, matching PersistCredentials' report of an in-memory change.
func (s executionCredentialFields) UpdateCredentials(ctx context.Context, id int64, credentials map[string]any) error {
	// The field writer receives the same credential map as the request target, so
	// subsequent request reads observe the attempted update even if storage fails.
	s.original.Record.Credentials = credentials
	return s.updater.UpdateCredentials(ctx, id, credentials)
}

// PersistExecutionCredentials applies the provider module's credential-copying
// and shadow rules to an execution target. Nil storage or targets are ignored;
// changed credentials remain on the target even if persistence fails, and
// callers synchronize target access.
func PersistExecutionCredentials(ctx context.Context, repo ExecutionProviderStore, value *ExecutionProvider, credentials map[string]any) error {
	if repo == nil || value == nil {
		return nil
	}
	var store provider.CredentialUpdateStore = executionCredentialStore{source: repo, original: value}
	// Prefer credential-field persistence so refreshing authentication cannot
	// overwrite health or configuration fields from the request's older record.
	if updater, ok := repo.(providerCredentialsUpdater); ok {
		store = executionCredentialFields{executionCredentialStore: executionCredentialStore{source: repo, original: value}, updater: updater}
	}
	view := &provider.Record{ID: value.Record.ID, Platform: value.Record.Platform, Type: value.Record.Type, ParentProviderID: value.Record.ParentProviderID, QuotaDimension: value.Record.QuotaDimension}
	changed, err := provider.PersistCredentials(ctx, store, view, credentials, slog.Warn)
	// PersistCredentials reports in-memory mutation separately from its error, so
	// copy changed credentials even when the storage operation has failed.
	if changed {
		value.Record.Credentials = view.Credentials
	}
	return err
}

// ExecutionToken obtains a token from source using a copied provider record
// and transfers its credentials back to value. That transfer also occurs on
// errors, so credential backfills performed during acquisition remain visible
// to later request preparation.
func ExecutionToken(ctx context.Context, source ExecutionTokenSource, value *ExecutionProvider) (string, error) {
	record := ExecutionRecord(value)
	token, err := source.GetAccessToken(ctx, record)
	if value != nil && record != nil {
		value.Record.Credentials = record.Credentials
	}
	return token, err
}
