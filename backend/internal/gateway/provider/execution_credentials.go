package provider

import (
	"context"
	"log/slog"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

type providerCredentialsUpdater interface {
	UpdateCredentials(context.Context, int64, map[string]any) error
}

type executionCredentialStore struct {
	source   ExecutionProviderStore
	original *ExecutionProvider
}

type executionCredentialFields struct {
	executionCredentialStore
	updater providerCredentialsUpdater
}

// ExecutionTokenSource reads provider access tokens for execution.
type ExecutionTokenSource interface {
	GetAccessToken(context.Context, *provider.Record) (string, error)
}

func (s executionCredentialStore) Update(ctx context.Context, value *provider.Record) error {
	s.original.Record.Credentials = value.Credentials
	return s.source.Update(ctx, s.original)
}

func (s executionCredentialFields) UpdateCredentials(ctx context.Context, id int64, credentials map[string]any) error {
	s.original.Record.Credentials = credentials
	return s.updater.UpdateCredentials(ctx, id, credentials)
}

// PersistExecutionCredentials delegates credential persistence to the provider module.
func PersistExecutionCredentials(ctx context.Context, repo ExecutionProviderStore, value *ExecutionProvider, credentials map[string]any) error {
	if repo == nil || value == nil {
		return nil
	}
	var store provider.CredentialUpdateStore = executionCredentialStore{source: repo, original: value}
	if updater, ok := repo.(providerCredentialsUpdater); ok {
		store = executionCredentialFields{executionCredentialStore: executionCredentialStore{source: repo, original: value}, updater: updater}
	}
	view := &provider.Record{ID: value.Record.ID, Platform: value.Record.Platform, Type: value.Record.Type, ParentProviderID: value.Record.ParentProviderID, QuotaDimension: value.Record.QuotaDimension}
	changed, err := provider.PersistCredentials(ctx, store, view, credentials, slog.Warn)
	if changed {
		value.Record.Credentials = view.Credentials
	}
	return err
}

// ExecutionToken reads a token and copies Gemini or Antigravity project backfills into the execution target.
func ExecutionToken(ctx context.Context, source ExecutionTokenSource, value *ExecutionProvider) (string, error) {
	record := ExecutionRecord(value)
	token, err := source.GetAccessToken(ctx, record)
	if value != nil && record != nil {
		value.Record.Credentials = record.Credentials
	}
	return token, err
}
