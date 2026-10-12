package provider

import (
	"context"
	"fmt"
	"strings"
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

func TestGeminiThinkingDistinguishesUnknownAndEmptyOptions(t *testing.T) {
	yes, no := true, false
	empty := []modelcatalog.ReasoningOption{}
	for _, tc := range []struct {
		name  string
		attrs modelcatalog.Attributes
		fail  bool
	}{
		{"unknown", modelcatalog.Attributes{}, false},
		{"reasoning known options unknown", modelcatalog.Attributes{Reasoning: &yes}, false},
		{"known empty", modelcatalog.Attributes{Reasoning: &yes, ReasoningOptions: &empty}, true},
		{"reasoning disabled", modelcatalog.Attributes{Reasoning: &no}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := testkit.OfflineView().Resolve(modelcatalog.Target{Platform: "gemini", Model: "private-model"}, tc.attrs)
			for _, control := range []struct {
				name, body, effort string
				budget             int
			}{
				{"effort", `{"reasoning_effort":"high"}`, "high", 0},
				{"budget", `{"thinking":{"type":"enabled","budget_tokens":4096}}`, "", 4096},
			} {
				t.Run(control.name, func(t *testing.T) {
					thinking, err := GeminiThinking(result, []byte(control.body))
					if tc.fail {
						require.Error(t, err)
						return
					}
					require.NoError(t, err)
					require.Equal(t, control.effort, thinking.ThinkingLevel)
					require.Equal(t, control.budget, thinking.ThinkingBudget)
				})
			}
		})
	}
}

func TestApplyModelAttributeParametersUsesGLMEffortOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, model, protocol, body, path, want string
		efforts                                 []string
	}{
		{"chat accepts configured low", "glm-5.2", "openai_chat_completions", `{"reasoning_effort":"low"}`, "reasoning_effort", "low", []string{"low", "high"}},
		{"responses accepts configured medium", "glm-5.3", "openai_responses", `{"reasoning":{"effort":"medium"}}`, "reasoning.effort", "medium", []string{"medium", "high"}},
		{"alias stays in allowed set", "glm-5.3", "openai_chat_completions", `{"reasoning_effort":"xhigh"}`, "reasoning_effort", "high", []string{"low", "high"}},
		{"messages accepts configured medium", "glm-5.3", "anthropic_messages", `{"output_config":{"effort":"medium"}}`, "output_config.effort", "medium", []string{"medium", "high"}},
		{"messages alias stays in allowed set", "glm-5.3", "anthropic_messages", `{"thinking":{"type":"disabled"}}`, "output_config.effort", "high", []string{"high", "max"}},
		{"no selectable efforts", "glm-5.2", "openai_chat_completions", `{"reasoning_effort":"low"}`, "reasoning_effort", "", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := make([]*string, 0, len(tc.efforts))
			for _, effort := range tc.efforts {
				values = append(values, &effort)
			}
			options := []modelcatalog.ReasoningOption{{Type: "effort", Values: values}}
			result := testkit.OfflineView().Resolve(modelcatalog.Target{Platform: "zhipu", Model: tc.model, UpstreamProtocol: tc.protocol}, modelcatalog.Attributes{ReasoningOptions: &options})
			body, err := ApplyModelAttributeParameters([]byte(tc.body), result)
			require.NoError(t, err)
			require.Equal(t, tc.want, gjson.GetBytes(body, tc.path).String())
			if tc.want != "" {
				require.True(t, *result.Attributes.EffortSupport(tc.want))
			}
			if tc.protocol == "anthropic_messages" {
				require.Equal(t, "enabled", gjson.GetBytes(body, "thinking.type").String())
			}
		})
	}
}

func TestModelRequestStateGeminiFieldAliases(t *testing.T) {
	for _, config := range []string{"generationConfig", "generation_config"} {
		for _, thinking := range []string{"thinkingConfig", "thinking_config"} {
			for _, tc := range []struct {
				field, value string
				enabled      bool
			}{
				{"thinkingLevel", `"high"`, true},
				{"thinking_level", `"none"`, false},
				{"thinkingBudget", "1024", true},
				{"thinking_budget", "0", false},
			} {
				t.Run(config+"/"+thinking+"/"+tc.field, func(t *testing.T) {
					body := []byte(fmt.Sprintf(`{%q:{%q:{%q:%s}}}`, config, thinking, tc.field, tc.value))
					state := ModelRequestState(body)
					require.NotNil(t, state.Thinking)
					require.Equal(t, tc.enabled, *state.Thinking)
				})
			}
		}
	}
}

func TestModelRequestStateFieldPrecedence(t *testing.T) {
	high, low, medium, none := "high", "low", "medium", "none"
	yes, no := true, false
	for _, tc := range []struct {
		name, body string
		effort     *string
		thinking   *bool
	}{
		{"absent", `{}`, nil, nil},
		{"effort priority", `{"output_config":{"effort":"low"},"reasoning_effort":"medium","reasoning":{"effort":" HIGH "}}`, &high, &yes},
		{"thinking overrides effort", `{"thinking":{"type":"disabled"},"reasoning":{"effort":"high"}}`, &high, &no},
		{"duplicate nested match", `{"reasoning":{},"reasoning":{"effort":"high"}}`, &high, &yes},
		{"duplicate null match", `{"reasoning":{"effort":null},"reasoning":{"effort":"high"},"reasoning_effort":"medium"}`, &medium, &yes},
		{"duplicate scalar match", `{"reasoning_effort":"low","reasoning_effort":"high"}`, &low, &yes},
		{"duplicate thinking match", `{"thinking":{},"thinking":{"type":"disabled"},"thinking":{"type":"enabled"}}`, nil, &no},
		{"config spelling priority", `{"generation_config":{"thinkingConfig":{"thinkingLevel":"none"}},"generationConfig":{"thinking_config":{"thinking_level":"high"}}}`, &none, &no},
		{"duplicate config match", `{"generationConfig":{},"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`, nil, nil},
		{"budget overrides level", `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high","thinkingBudget":0}}}`, &high, &no},
		{"escaped field", `{"reasoning_\u0065ffort":"high"}`, &high, &yes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			state := ModelRequestState(body)
			for i := range body {
				body[i] = 'x'
			}
			require.Equal(t, modelcatalog.RequestState{Effort: tc.effort, Thinking: tc.thinking}, state)
		})
	}
}

func BenchmarkModelRequestState(b *testing.B) {
	for _, size := range []int{1024, 1024 * 1024, 8 * 1024 * 1024} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			body := []byte(`{"input":"` + strings.Repeat("x", size) + `","reasoning":{"effort":"high"}}`)
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for range b.N {
				state := ModelRequestState(body)
				if state.Effort == nil || *state.Effort != "high" {
					b.Fatal("missing reasoning effort")
				}
			}
		})
	}
}
