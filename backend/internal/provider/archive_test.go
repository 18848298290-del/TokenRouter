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

// archiveProvidersFixture records provider selection and creation order for
// archive tests.
type archiveProvidersFixture struct {
	ArchiveProviders // unused archive provider methods
	// values is returned directly for explicit-ID export selection. The pointers
	// and their mutable containers remain shared with the fixture's caller.
	values []*Record
	// events points to the timeline also used by the proxy fixture and injected
	// callbacks. Each operation appends its phase before returning.
	events *[]string
	// created retains shallow copies of submitted CreateProviderInput values.
	// Their credential maps remain available for assertions after the import's
	// enrichment step.
	created []CreateProviderInput
}

// archiveProxiesFixture records proxy import before the archive's provider
// writes.
type archiveProxiesFixture struct {
	ArchiveProxies // unused archive proxy methods
	// events points to the shared archive timeline. Proxy import appends its phase
	// before returning the keys used by provider creation.
	events *[]string
}

// GetProvidersByIDs records provider selection in the shared event list and
// returns retained fixture pointers. The fixture performs no ID filtering or
// copying so export owns the isolation exercised by this test.
func (f *archiveProvidersFixture) GetProvidersByIDs(context.Context, []int64) ([]*Record, error) {
	*f.events = append(*f.events, "providers")
	return f.values, nil
}

// CreateProvider records every attempted input before applying its named
// failure scenario. It retains a shallow input copy, rejects the bad label, and
// otherwise returns a minimal created record.
func (f *archiveProvidersFixture) CreateProvider(_ context.Context, input *CreateProviderInput) (*Record, error) {
	*f.events = append(*f.events, "create")
	f.created = append(f.created, *input)
	if input.Name == "bad" {
		return nil, errors.New("fixture rejected")
	}
	return &Record{ID: 1, Name: input.Name, Platform: input.Platform, Type: input.Type}, nil
}

// ImportForProviderBinding records the proxy phase and returns the fixed
// known-key mapping. It reports one created proxy, letting Archive's
// partial-result test observe proxy work completed before provider entries.
func (f archiveProxiesFixture) ImportForProviderBinding(context.Context, []egress.TransferProxy) (map[string]int64, egress.ProxyImportResult, error) {
	*f.events = append(*f.events, "proxies")
	return map[string]int64{"known": 7}, egress.ProxyImportResult{ProxyCreated: 1}, nil
}

// TestArchiveExportPreservesOrderAndIndependentSecrets exports credentials
// after deferred proxy-option validation. The assertions protect validation
// order, the injected UTC timestamp, an allocated empty proxy slice, and
// isolation of the nested model mapping.
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

// TestArchiveImportRetainsPartialResults imports one accepted entry and one
// rejected entry after proxy handling. Counts retain partial success, decoded
// identity hints reach creation, and the request's credential map remains
// unchanged.
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

// TestArchiveRoundTripsImageModes carries independent endpoint capabilities
// through export and import. Editing an exported copy must not modify the
// source provider, and both capability values must reach the creation input.
func TestArchiveRoundTripsImageModes(t *testing.T) {
	events := []string{}
	credentials := map[string]any{"api_key": "fixture", ImagesGenerationsModeKey: "non_streaming", ImagesEditsModeKey: "streaming"}
	records := &archiveProvidersFixture{events: &events, values: []*Record{{ID: 1, Name: "images", Platform: PlatformOpenAI, Type: ProviderTypeAPIKey, Credentials: credentials}}}
	archive := NewArchive(records, archiveProxiesFixture{events: &events}, ArchiveOptions{Now: time.Now})
	payload, err := archive.Export(t.Context(), ArchiveExportQuery{IDs: []int64{1}, IncludeProxies: func() (bool, error) { return false, nil }})
	require.NoError(t, err)
	result, err := archive.Import(t.Context(), transfer.DataImportRequest{Data: payload})
	require.NoError(t, err)
	require.Equal(t, 1, result.ProviderCreated)
	require.Equal(t, "non_streaming", records.created[0].Credentials[ImagesGenerationsModeKey])
	require.Equal(t, "streaming", records.created[0].Credentials[ImagesEditsModeKey])
	payload.Providers[0].Credentials[ImagesGenerationsModeKey] = "both"
	require.Equal(t, "non_streaming", credentials[ImagesGenerationsModeKey])
}
