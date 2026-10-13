package catalogue_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Requestable catalogue calls read the current in-memory provider rows each
// time. These scenarios change saved candidates between queries and combine
// passthrough with configured model scopes, while the list wrapper exposes the
// resolved IDs for caller-facing assertions.

// requestableListFixture exposes resolved model IDs for caller-facing catalogue scenarios.
type requestableListFixture struct {
	*routing.RequestableCatalogue // configured catalogue query service
}

// TestRequestableCatalogueReadsCurrentProviders replaces an in-memory provider's
// whitelist between two catalogue queries. Each query must issue one group read,
// and the second result must contain the newly supplied model.
func TestRequestableCatalogueReadsCurrentProviders(t *testing.T) {
	groupID := int64(9)
	record := provider.Record{ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"model_whitelist": []string{"first"}}}
	repo := &modelsListProviderRepoStub{byGroup: map[int64][]provider.Record{groupID: {record}}}
	catalogue := newCatalogueFixture(repo, nil, nil)
	first := catalogue.ResolveRequestableModels(context.Background(), &groupID, "")
	require.Equal(t, []string{"first"}, routing.RequestableModelIDs(first.Models))
	require.Equal(t, int64(1), repo.listByGroupCalls.Load())
	record.Credentials["model_whitelist"] = []string{"second"}
	repo.byGroup[groupID] = []provider.Record{record}
	second := catalogue.ResolveRequestableModels(context.Background(), &groupID, "")
	require.Equal(t, []string{"second"}, routing.RequestableModelIDs(second.Models))
	require.Equal(t, int64(2), repo.listByGroupCalls.Load())
}

// TestGetAvailableModels_ErrorAndGlobalListBranches combines a failed global
// provider read with a successful mixed-platform pool. Failures must yield empty
// IDs, and the successful query must read the pool once.
func TestGetAvailableModels_ErrorAndGlobalListBranches(t *testing.T) {
	errRepo := &modelsListProviderRepoStub{
		err: errors.New("db error"),
	}
	svcErr := newRequestableListFixture(errRepo)
	require.Empty(t, svcErr.Available(context.Background(), nil, ""))

	okRepo := &modelsListProviderRepoStub{
		all: []provider.Record{
			{
				ID:       1,
				Platform: capability.PlatformAnthropic,
				Credentials: map[string]any{
					"model_whitelist": []string{"claude-3-5-sonnet"},
					"model_mapping": map[string]any{
						"claude-3-5-sonnet": "claude-3-5-sonnet",
					},
				},
			},
			{
				ID:       2,
				Platform: capability.PlatformGemini,
				Credentials: map[string]any{
					"model_whitelist": []string{"gemini-2.5-pro"},
					"model_mapping": map[string]any{
						"gemini-2.5-pro": "gemini-2.5-pro",
					},
				},
			},
		},
	}
	svcOK := newRequestableListFixture(okRepo)
	models := svcOK.Available(context.Background(), nil, "")
	require.Equal(t, []string{"claude-3-5-sonnet", "gemini-2.5-pro"}, models)
	require.Equal(t, int64(1), okRepo.listAllCalls.Load())
}

// TestGetAvailableModelsPassthroughPreservesExplicitScope combines an OpenAI
// passthrough provider with another configured provider in one group. The catalogue
// must expose both aliases and their whitelisted targets as the expected ID set.
func TestGetAvailableModelsPassthroughPreservesExplicitScope(t *testing.T) {
	groupID := int64(10)
	first := provider.Record{ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{
		"model_mapping": map[string]any{"custom-alias": "custom-upstream"}, "model_whitelist": []string{"custom-upstream"},
	}, Extra: map[string]any{"openai_passthrough": true}}
	second := provider.Record{ID: 2, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{
		"model_mapping": map[string]any{"other-alias": "other-upstream"}, "model_whitelist": []string{"other-upstream"},
	}}
	repo := &modelsListProviderRepoStub{byGroup: map[int64][]provider.Record{groupID: {first, second}}}
	models := newRequestableListFixture(repo).Available(context.Background(), &groupID, capability.PlatformOpenAI)
	require.ElementsMatch(t, []string{"custom-alias", "custom-upstream", "other-alias", "other-upstream"}, models)
}

// TestGetAvailableModels_GlobalListPreservesMappedModelsWithOpenAIPassthrough
// combines an unconfigured passthrough provider with a mapped Anthropic provider
// in one group. The mapped model must remain visible, and the tested fabricated
// OpenAI and unknown candidates must be absent.
func TestGetAvailableModels_GlobalListPreservesMappedModelsWithOpenAIPassthrough(t *testing.T) {
	groupID := int64(11)
	repo := &modelsListProviderRepoStub{
		byGroup: map[int64][]provider.Record{
			groupID: {
				{
					ID:       1,
					Platform: capability.PlatformOpenAI,
					Extra:    map[string]any{"openai_passthrough": true},
				},
				{
					ID:          2,
					Platform:    capability.PlatformAnthropic,
					Credentials: map[string]any{"model_mapping": map[string]any{"claude-mapped": "claude-mapped"}},
				},
			},
		},
	}
	svc := newRequestableListFixture(repo)

	models := svc.Available(context.Background(), &groupID, "")
	require.Contains(t, models, "claude-mapped")
	require.NotContains(t, models, "gpt-5.6-sol", "目录只使用已配置的型号")
	require.NotContains(t, models, "unknown-model")
}

// newRequestableListFixture assembles a catalogue ID wrapper over the supplied
// provider query fixture. Pricing and group policies are absent, so catalogue
// eligibility comes from the supplied provider rows.
func newRequestableListFixture(rows catalogueRows) requestableListFixture {
	return requestableListFixture{newCatalogueFixture(rows, nil, nil).RequestableCatalogue}
}

// Available returns model IDs resolved for the selected group and platform.
// A nil group ID selects the fixture's global provider query, and provider read
// failures become an empty ID slice.
func (f requestableListFixture) Available(ctx context.Context, groupID *int64, platform string) []string {
	return routing.RequestableModelIDs(f.ResolveRequestableModels(ctx, groupID, platform).Models)
}
