package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog/testkit"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

type mediaAttributeRepo struct {
	routing.ModelAttributeRepository
	config *routing.ModelAttributeConfig
	reads  int
}

func TestApplyModelAttributeParameters(t *testing.T) {
	cases := []struct {
		name           string
		target         modelcatalog.Target
		body, expected string
	}{
		{"gpt sampling none", modelcatalog.Target{Platform: "openai", Model: "gpt-5.4"}, `{"reasoning":{"effort":"none"},"temperature":0.4,"top_p":0.8}`, `{"reasoning":{"effort":"none"},"temperature":0.4,"top_p":0.8}`},
		{"gpt sampling high", modelcatalog.Target{Platform: "openai", Model: "gpt-5.4"}, `{"reasoning":{"effort":"high"},"temperature":0.4,"top_p":0.8}`, `{"reasoning":{"effort":"high"}}`},
		{"compact max", modelcatalog.Target{Platform: "openai", ProviderType: "oauth", Model: "gpt-5.6-sol", Endpoint: "compact"}, `{"reasoning":{"effort":"max","summary":"auto"},"max_output_tokens":4096,"temperature":0.4,"top_p":0.8}`, `{"reasoning":{"effort":"xhigh","summary":"auto"}}`},
		{"standard max", modelcatalog.Target{Platform: "openai", ProviderType: "apikey", Model: "gpt-5.6-sol"}, `{"reasoning":{"effort":"max"}}`, `{"reasoning":{"effort":"max"}}`},
		{"glm high", modelcatalog.Target{Platform: "zhipu", Model: "glm-5.2"}, `{"reasoning_effort":"low"}`, `{"reasoning_effort":"high"}`},
		{"glm max", modelcatalog.Target{Platform: "zhipu", Model: "glm-5.3"}, `{"reasoning_effort":"xhigh"}`, `{"reasoning_effort":"max"}`},
		{"deepseek thinking", modelcatalog.Target{Platform: "deepseek", Model: "deepseek-v4-pro"}, `{"thinking":{"type":"enabled"},"temperature":0.4,"top_p":0.98}`, `{"thinking":{"type":"enabled"},"top_p":0.98}`},
		{"deepseek disabled", modelcatalog.Target{Platform: "deepseek", Model: "deepseek-v4-flash"}, `{"thinking":{"type":"disabled"},"temperature":0.4,"top_p":0.98}`, `{"thinking":{"type":"disabled"},"temperature":0.4}`},
		{"unknown", modelcatalog.Target{Platform: "openai", Model: "company-latest", BaseURL: "https://relay.example/v1"}, `{"reasoning":{"effort":"none"},"temperature":0.4,"top_p":0.8}`, `{"reasoning":{"effort":"none"},"temperature":0.4,"top_p":0.8}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved := testkit.OfflineView().Resolve(tc.target, modelcatalog.Attributes{})
			body, err := ApplyModelAttributeParameters([]byte(tc.body), resolved)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(body))
		})
	}
}

func TestApplyModelAttributeParametersOllamaOverrides(t *testing.T) {
	for _, cap := range []int{-1, 0, 32768, 65535} {
		target := modelcatalog.Target{Platform: "openai", Model: "company-model", Ollama: true, OllamaOutputCap: &cap}
		resolved := testkit.OfflineView().Resolve(target, modelcatalog.Attributes{})
		body, err := ApplyModelAttributeParameters([]byte(`{"max_tokens":90000,"max_completion_tokens":100000}`), resolved)
		require.NoError(t, err)
		if cap > 0 {
			require.EqualValues(t, cap, gjson.GetBytes(body, "max_tokens").Int())
			require.EqualValues(t, cap, gjson.GetBytes(body, "max_completion_tokens").Int())
		} else {
			require.EqualValues(t, 90000, gjson.GetBytes(body, "max_tokens").Int())
		}
	}
	cap := 80000
	target := modelcatalog.Target{Platform: "openai", Model: "company-model", Ollama: true}
	resolved := testkit.OfflineView().Resolve(target, modelcatalog.Attributes{OutputLimit: &cap})
	body := ApplyOllamaCloudRawChatCompletionsRequest(ollamaCloudRawChatCompletionsTestProvider(), []byte(`{"max_tokens":100000}`))
	body, err := ApplyModelAttributeParameters(body, resolved)
	require.NoError(t, err)
	require.EqualValues(t, cap, gjson.GetBytes(body, "max_tokens").Int())
}

func (r *mediaAttributeRepo) ForGroup(context.Context, int64) (*routing.ModelAttributeConfig, error) {
	r.reads++
	return r.config, nil
}

func TestPrepareModelAttributeParametersImageToolOverrides(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		repo := &mediaAttributeRepo{config: &routing.ModelAttributeConfig{Status: "active", Rules: []routing.ModelAttributeRule{{Models: []string{"gpt-image-2"}, Attributes: modelcatalog.Attributes{Media: &modelcatalog.MediaCapabilities{InputFidelity: &enabled}}}}}}
		service := &routing.ModelAttributeService{Repo: repo, Catalog: routing.ModelAttributeCatalog{View: testkit.OfflineView}}
		ctx := requeststate.WithGroup(context.Background(), &routing.Group{ID: 1})
		provider := &ExecutionProvider{Record: providercore.Record{Platform: "openai", Type: "apikey"}}
		original := []byte(`{"model":"gpt-4o","tools":[{"type":"image_generation","model":"gpt-image-2","input_fidelity":"high"},{"type":"image_generation","model":"gpt-image-1.5","input_fidelity":"high"},{"type":"image_generation","model":"private-image","input_fidelity":"high"}]}`)
		body, err := PrepareModelAttributeParameters(ctx, provider, "gpt-4o", original, service, "openai_responses")
		require.NoError(t, err)
		require.Equal(t, enabled, gjson.GetBytes(body, "tools.0.input_fidelity").Exists())
		require.Equal(t, "high", gjson.GetBytes(body, "tools.1.input_fidelity").String())
		require.Equal(t, "high", gjson.GetBytes(body, "tools.2.input_fidelity").String())
		require.Equal(t, "high", gjson.GetBytes(original, "tools.0.input_fidelity").String())
		require.Equal(t, 1, repo.reads)
	}
}
