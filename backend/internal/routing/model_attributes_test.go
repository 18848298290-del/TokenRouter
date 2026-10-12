package routing

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog/testkit"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// attributeRepoFixture counts attribute queries and supplies profiles by group.
type attributeRepoFixture struct {
	config      *ModelAttributeConfig
	groups      map[int64]*ModelAttributeConfig
	batchCalls  int
	singleCalls int
	requested   []int64
	err         error
}

func (r *attributeRepoFixture) List(context.Context) ([]ModelAttributeConfig, error) {
	return []ModelAttributeConfig{*r.config}, nil
}

func (r *attributeRepoFixture) Get(context.Context, int64) (*ModelAttributeConfig, error) {
	return r.config, nil
}

func (r *attributeRepoFixture) ForGroup(context.Context, int64) (*ModelAttributeConfig, error) {
	r.singleCalls++
	return r.config, nil
}

func (r *attributeRepoFixture) ForGroups(_ context.Context, ids []int64) (map[int64]*ModelAttributeConfig, error) {
	r.batchCalls++
	r.requested = ids
	return r.groups, r.err
}

func (r *attributeRepoFixture) Save(_ context.Context, value *ModelAttributeConfig) error {
	r.config = value
	return nil
}
func (r *attributeRepoFixture) Delete(context.Context, int64) error { r.config = nil; return nil }

// TestModelAttributesRequestLanguage checks that single and batch reads use the request language.
func TestModelAttributesRequestLanguage(t *testing.T) {
	en := "en"
	content := locale.Update[string]{Content: locale.Content[string]{
		SourceLocale: &en, Source: "English name", Revision: 1, SourceRevision: 1,
		Translations: map[string]locale.Translation[string]{"zh-Hans": {Value: "中文名称", SourceRevision: 1}},
	}}
	config := &ModelAttributeConfig{Status: StatusActive, Rules: []ModelAttributeRule{
		{Models: []string{"model"}, Attributes: modelcatalog.Attributes{DisplayNameLocalization: &content}},
	}}
	service := ModelAttributeService{
		Repo:    &attributeRepoFixture{config: config, groups: map[int64]*ModelAttributeConfig{1: config}},
		Catalog: ModelAttributeCatalog{Lookup: func(string) modelcatalog.Attributes { return modelcatalog.Attributes{} }},
	}
	models := []RequestableModel{{ID: "model"}}
	for code, want := range map[string]string{"en": "English name", "zh-Hans": "中文名称"} {
		ctx := locale.WithLanguage(context.Background(), code)
		single, err := service.ResolveModels(ctx, 1, models)
		require.NoError(t, err)
		batch, err := service.ResolveGroups(ctx, map[int64][]RequestableModel{1: models})
		require.NoError(t, err)
		require.Equal(t, want, *single["model"].DisplayName)
		require.Equal(t, single["model"], batch[1]["model"])
	}
}

func TestModelAttributesUseFinalModelsWithoutChangingRoutes(t *testing.T) {
	yes, no := true, false
	large, small := 100, 1
	config := &ModelAttributeConfig{Status: StatusActive, Rules: []ModelAttributeRule{
		{Models: []string{"upstream-*"}, Attributes: modelcatalog.Attributes{ToolCall: &no}},
		{Models: []string{"upstream-a"}, Attributes: modelcatalog.Attributes{OutputLimit: &small}},
	}}
	service := ModelAttributeService{Repo: &attributeRepoFixture{config: config}, Catalog: ModelAttributeCatalog{Lookup: func(string) modelcatalog.Attributes {
		return modelcatalog.Attributes{OutputLimit: &large, ToolCall: &yes}
	}}}
	models := []RequestableModel{{ID: "public-alias", PricingModel: "unrelated-price", UpstreamModels: []string{"upstream-a", "upstream-b"}}}
	before := models[0]
	result, err := service.ResolveModels(context.Background(), 1, models)
	require.NoError(t, err)
	require.Equal(t, before, models[0])
	require.Equal(t, 1, *result["public-alias"].OutputLimit)
	require.False(t, *result["public-alias"].ToolCall)
	require.True(t, result["public-alias"].RouteDifferences)
	// An exact rule supplies the output limit and takes precedence over wildcard rules.
	result, err = service.ResolveModels(context.Background(), 1, []RequestableModel{{ID: "single", UpstreamModels: []string{"upstream-a"}}})
	require.NoError(t, err)
	require.True(t, *result["single"].ToolCall)
	config.Status = StatusDisabled
	result, err = service.ResolveModels(context.Background(), 1, models)
	require.NoError(t, err)
	require.Equal(t, 100, *result["public-alias"].OutputLimit)
	require.True(t, *result["public-alias"].ToolCall)
}

// TestModelAttributesBatchReads checks group isolation, one batch read, and visibility of updates on the next query.
func TestModelAttributesBatchReads(t *testing.T) {
	no := false
	limit := 100
	config := &ModelAttributeConfig{Status: StatusActive, Rules: []ModelAttributeRule{
		{Models: []string{"upstream"}, Attributes: modelcatalog.Attributes{ToolCall: &no}},
	}}
	repo := &attributeRepoFixture{groups: map[int64]*ModelAttributeConfig{1: config, 2: config}}
	service := ModelAttributeService{Repo: repo, Catalog: ModelAttributeCatalog{Lookup: func(string) modelcatalog.Attributes {
		return modelcatalog.Attributes{OutputLimit: &limit}
	}}}
	groups := map[int64][]RequestableModel{
		1: {{ID: "alias-a", UpstreamModels: []string{"upstream"}}},
		2: {{ID: "alias-b", UpstreamModels: []string{"upstream"}}},
		3: {{ID: "upstream"}},
		4: {},
	}
	result, err := service.ResolveGroups(context.Background(), groups)
	require.NoError(t, err)
	require.Equal(t, 1, repo.batchCalls)
	require.Zero(t, repo.singleCalls)
	require.ElementsMatch(t, []int64{1, 2, 3}, repo.requested)
	require.Len(t, result, 3)
	require.False(t, *result[1]["alias-a"].ToolCall)
	require.False(t, *result[2]["alias-b"].ToolCall)
	require.Nil(t, result[3]["upstream"].ToolCall)
	require.Equal(t, 100, *result[3]["upstream"].OutputLimit)
	config.Status = StatusDisabled
	result, err = service.ResolveGroups(context.Background(), groups)
	require.NoError(t, err)
	require.Equal(t, 2, repo.batchCalls)
	require.Nil(t, result[1]["alias-a"].ToolCall)
}

// TestModelAttributesBatchEmptyAndFailure covers empty catalogues and repository failures.
func TestModelAttributesBatchEmptyAndFailure(t *testing.T) {
	repo := &attributeRepoFixture{err: errors.New("database unavailable")}
	service := ModelAttributeService{Repo: repo}
	result, err := service.ResolveGroups(context.Background(), map[int64][]RequestableModel{1: {}})
	require.NoError(t, err)
	require.Empty(t, result)
	require.Zero(t, repo.batchCalls)
	result, err = service.ResolveGroups(context.Background(), map[int64][]RequestableModel{1: {{ID: "model"}}})
	require.ErrorIs(t, err, repo.err)
	require.Nil(t, result)
	require.Equal(t, 1, repo.batchCalls)
}

func TestAttributeConfigValidation(t *testing.T) {
	service := ModelAttributeService{Repo: &attributeRepoFixture{}}
	zero := 0
	for _, config := range []ModelAttributeConfig{
		{Name: "x", Status: "other"},
		{Name: "x", Status: StatusActive, GroupIDs: []int64{1, 1}},
		{Name: "x", Status: StatusActive, Rules: []ModelAttributeRule{{Models: []string{"a*b"}}}},
		{Name: "x", Status: StatusActive, Rules: []ModelAttributeRule{{Models: []string{"a"}, Attributes: modelcatalog.Attributes{OutputLimit: &zero}}}},
	} {
		require.Error(t, service.Save(context.Background(), &config))
	}
	no := false
	valid := ModelAttributeConfig{Name: "metadata", Status: StatusActive, Rules: []ModelAttributeRule{{Models: []string{"*"}, Attributes: modelcatalog.Attributes{ToolCall: &no}}}}
	require.NoError(t, service.Save(context.Background(), &valid))
}

func TestAttributeRulesSeparateRoutesAndStableIDs(t *testing.T) {
	one, two := 1, 2
	config := &ModelAttributeConfig{Name: "routes", Status: StatusActive, Rules: []ModelAttributeRule{
		{Models: []string{"model"}, Match: &ModelAttributeMatch{Platform: "openai"}, Attributes: modelcatalog.Attributes{OutputLimit: &one}},
		{Models: []string{"model"}, Match: &ModelAttributeMatch{Platform: "grok"}, Attributes: modelcatalog.Attributes{OutputLimit: &two}},
	}}
	require.NoError(t, (&ModelAttributeService{Repo: &attributeRepoFixture{}}).Save(context.Background(), config))
	require.NotEqual(t, config.Rules[0].ID, config.Rules[1].ID)
	require.Equal(t, one, *config.match("model", nil, modelcatalog.Target{Platform: "openai"}).OutputLimit)
	require.Equal(t, two, *config.match("model", nil, modelcatalog.Target{Platform: "grok"}).OutputLimit)
	config.Rules[0].Match = nil
	config.Rules[1].Match = &ModelAttributeMatch{}
	require.Error(t, (&ModelAttributeService{Repo: &attributeRepoFixture{}}).Save(context.Background(), config))
}

func BenchmarkModelAttributeRoutes(b *testing.B) {
	view := testkit.OfflineView()
	for _, count := range []int{1, 65} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			svc := &ModelAttributeService{Catalog: ModelAttributeCatalog{View: func() modelcatalog.View { return view }}}
			models := make([]RequestableModel, 186)
			for i := range models {
				models[i].ID = fmt.Sprint("alias-", i)
				for range count {
					for _, wire := range []string{"openai_responses", "openai_chat_completions", "anthropic_messages"} {
						models[i].AttributeTargets = append(models[i].AttributeTargets, modelcatalog.Target{Platform: "openai", ProviderType: "apikey", Model: "gpt-5.4", ClientProtocol: wire, UpstreamProtocol: "openai_responses"})
					}
				}
			}
			b.ResetTimer()
			for range b.N {
				_, err := svc.ResolveGroups(context.Background(), map[int64][]RequestableModel{1: models})
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestModelAttributesReuseEquivalentRelays(t *testing.T) {
	lookups := 0
	svc := &ModelAttributeService{Catalog: ModelAttributeCatalog{Lookup: func(string) modelcatalog.Attributes {
		lookups++
		return modelcatalog.Attributes{DisplayNameLocalization: &locale.Update[string]{Content: locale.Original("Model")}}
	}}}
	first := modelcatalog.Target{Platform: "openai", ProviderType: "apikey", Model: "model", BaseURL: "https://relay-a.example/v1"}
	second := first
	second.BaseURL = "https://relay-b.example/v1"
	models := []RequestableModel{{ID: "alias", AttributeTargets: []modelcatalog.Target{first, second, first}}}
	result, err := svc.ResolveModels(context.Background(), 1, models)
	require.NoError(t, err)
	require.Equal(t, 1, lookups)
	require.Equal(t, "Model", *result["alias"].DisplayName)
	require.Equal(t, []string{"Model", "Model"}, result["alias"].SearchTerms)
	require.False(t, result["alias"].RouteDifferences)
	require.Equal(t, []modelcatalog.Target{first, second, first}, models[0].AttributeTargets)

	official := first
	official.BaseURL = "https://api.openai.com/v1"
	models[0].AttributeTargets = append(models[0].AttributeTargets, official)
	lookups = 0
	_, err = svc.ResolveModels(context.Background(), 1, models)
	require.NoError(t, err)
	require.Equal(t, 2, lookups)
}

func BenchmarkModelAttributeDistinctRoutes(b *testing.B) {
	for _, distinctRelays := range []bool{false, true} {
		b.Run(fmt.Sprint(distinctRelays), func(b *testing.B) {
			svc := &ModelAttributeService{Catalog: ModelAttributeCatalog{Lookup: func(string) modelcatalog.Attributes {
				yes, limit, modalities := true, 128000, []string{"text", "image"}
				return modelcatalog.Attributes{Context: &limit, Reasoning: &yes, ToolCall: &yes, InputModalities: &modalities}
			}}}
			models := make([]RequestableModel, 186)
			for i := range models {
				models[i].ID = fmt.Sprint("model-", i)
				for provider := range 65 {
					baseURL := ""
					if distinctRelays {
						baseURL = fmt.Sprintf("https://relay-%d.example/v1", provider)
					}
					for _, wire := range []string{"openai_responses", "openai_chat_completions", "anthropic_messages"} {
						models[i].AttributeTargets = append(models[i].AttributeTargets, modelcatalog.Target{Platform: "openai", ProviderType: "apikey", Model: models[i].ID, BaseURL: baseURL, ClientProtocol: wire, UpstreamProtocol: "openai_responses"})
					}
				}
			}
			b.ResetTimer()
			for range b.N {
				_, err := svc.ResolveGroups(context.Background(), map[int64][]RequestableModel{1: models})
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
