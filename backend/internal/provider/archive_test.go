package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/provider/transfer"
)

type archiveProvidersFixture struct {
	ArchiveProviders
	values  []*Record
	events  *[]string
	created []CreateProviderInput
}

type archiveProxiesFixture struct {
	ArchiveProxies
	events *[]string
}

func (f *archiveProvidersFixture) GetProvidersByIDs(context.Context, []int64) ([]*Record, error) {
	*f.events = append(*f.events, "providers")
	return f.values, nil
}

func (f *archiveProvidersFixture) CreateProvider(_ context.Context, input *CreateProviderInput) (*Record, error) {
	*f.events = append(*f.events, "create")
	f.created = append(f.created, *input)
	if input.Name == "bad" {
		return nil, errors.New("fixture rejected")
	}
	return &Record{ID: 1, Name: input.Name, Platform: input.Platform, Type: input.Type}, nil
}

func (f archiveProxiesFixture) ImportForProviderBinding(context.Context, []egress.TransferProxy) (map[string]int64, egress.ProxyImportResult, error) {
	*f.events = append(*f.events, "proxies")
	return map[string]int64{"known": 7}, egress.ProxyImportResult{ProxyCreated: 1}, nil
}

// TestArchiveExportPreservesOrderAndIndependentSecrets checks deferred include_proxies validation and independent exported credentials.
func TestArchiveExportPreservesOrderAndIndependentSecrets(t *testing.T) {
	events := []string{}
	mapping := map[string]any{"alias": "upstream"}
	records := &archiveProvidersFixture{events: &events, values: []*Record{{ID: 1, Credentials: map[string]any{"api_key": "fixture-secret", "model_mapping": mapping}}}}
	archive := NewArchive(records, nil, ArchiveOptions{Now: func() time.Time { return time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC) }})
	sentinel := errors.New("invalid include_proxies fixture")
	_, err := archive.Export(context.Background(), ArchiveExportQuery{IDs: []int64{1}, IncludeProxies: func() (bool, error) { events = append(events, "include"); return true, sentinel }})
	require.ErrorIs(t, err, sentinel)
	require.Equal(t, []string{"providers", "include"}, events)
	payload, err := archive.Export(context.Background(), ArchiveExportQuery{IDs: []int64{1}, IncludeProxies: func() (bool, error) { return false, nil }})
	require.NoError(t, err)
	require.Equal(t, "2026-09-13T00:00:00Z", payload.ExportedAt)
	require.Empty(t, payload.Proxies)
	require.NotNil(t, payload.Proxies)
	require.NoError(t, transfer.ValidateHeader(payload))
	require.Equal(t, "fixture-secret", payload.Providers[0].Credentials["api_key"])
	copied, ok := payload.Providers[0].Credentials["model_mapping"].(map[string]any)
	require.True(t, ok)
	copied["alias"] = "changed"
	require.Equal(t, "upstream", mapping["alias"])
}

// TestArchiveImportRetainsPartialResults checks partial import success and identity hints applied to copied credentials.
func TestArchiveImportRetainsPartialResults(t *testing.T) {
	events := []string{}
	records := &archiveProvidersFixture{events: &events}
	options := ArchiveOptions{DecodeIDToken: func(string) (*ArchiveIdentityHints, error) {
		events = append(events, "decode")
		return &ArchiveIdentityHints{Email: "decoded@example.test"}, nil
	}}
	archive := NewArchive(records, archiveProxiesFixture{events: &events}, options)
	input := transfer.DataImportRequest{Data: transfer.DataPayload{Proxies: []transfer.DataProxy{}, Providers: []transfer.DataProvider{{Name: "good", Platform: PlatformOpenAI, Type: ProviderTypeOAuth, Credentials: map[string]any{"id_token": "fixture"}}, {Name: "bad", Platform: PlatformOpenAI, Type: ProviderTypeAPIKey, Credentials: map[string]any{"api_key": "fixture"}}}}}
	result, err := archive.Import(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, 1, result.ProxyCreated)
	require.Equal(t, 1, result.ProviderCreated)
	require.Equal(t, 1, result.ProviderFailed)
	require.Equal(t, []string{"proxies", "decode", "create", "create"}, events)
	require.Equal(t, map[string]any{"id_token": "fixture"}, input.Data.Providers[0].Credentials)
	require.Equal(t, "decoded@example.test", records.created[0].Credentials["email"])
}
