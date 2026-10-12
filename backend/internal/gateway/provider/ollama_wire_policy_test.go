package provider

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	upstreamollama "github.com/TokenFlux/TokenRouter/internal/upstream/ollama"
)

// ollamaMaxTokensCapTestProvider 构造带自定义 cap 的 Ollama Cloud usage 提供商。
func ollamaMaxTokensCapTestProvider(id int64, cap any) *ExecutionProvider {
	provider := ollamaUsageProvider(id)
	provider.Record.Extra[upstreamollama.MaxTokensCapExtraKey] = cap
	return provider
}

func TestOllamaCloudMaxTokensCap(t *testing.T) {
	require.Equal(t, int64(65535), OllamaCloudMaxTokensCap(nil))
	require.Equal(t, int64(65535), OllamaCloudMaxTokensCap(ollamaUsageProvider(201)))

	tests := []struct {
		name string
		cap  any
		want int64
	}{
		{"float64", float64(32768), 32768},
		{"int", 40000, 40000},
		{"int64", int64(50000), 50000},
		{"json.Number", json.Number("60000"), 60000},
		{"json.Number invalid", json.Number("abc"), 65535},
		{"zero disables", 0, 0},
		{"negative disables", int64(-1), -1},
		{"string falls back", "abc", 65535},
		{"bool falls back", true, 65535},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := ollamaMaxTokensCapTestProvider(202, test.cap)
			require.Equal(t, test.want, OllamaCloudMaxTokensCap(provider))
		})
	}
}

func TestApplyOllamaCloudRawChatCompletionsRequestPreservesOutputBeforeResolution(t *testing.T) {
	body := []byte(`{"model":"deepseek-chat","max_tokens":100000}`)

	// 参数上限在最终能力解析后应用。
	ollama := ollamaCloudRawChatCompletionsTestProvider()
	require.JSONEq(t, `{"model":"deepseek-chat","max_tokens":100000}`,
		string(ApplyOllamaCloudRawChatCompletionsRequest(ollama, body)))

	// 官方 DeepSeek（api.deepseek.com + force_chat_completions）→ 字节级不变。
	official := rawChatCompletionsTestProvider()
	official.Record.Credentials["base_url"] = "https://api.deepseek.com"
	official.Record.Extra = map[string]any{
		providercore.ExtraKeyTextRouteMode: string(providercore.TextRouteModeForceChatCompletions),
	}
	require.Equal(t, body, ApplyOllamaCloudRawChatCompletionsRequest(official, body))

	// ollama.com 但无 force_chat_completions（Extra 缺键）→ 不通过钩子判定门槛，字节级不变。
	noForce := ollamaCloudRawChatCompletionsTestProvider()
	noForce.Record.Extra = nil
	require.Equal(t, body, ApplyOllamaCloudRawChatCompletionsRequest(noForce, body))

	// 空 body → 原样返回。
	require.Equal(t, []byte(nil), ApplyOllamaCloudRawChatCompletionsRequest(ollama, nil))
	require.Equal(t, []byte{}, ApplyOllamaCloudRawChatCompletionsRequest(ollama, []byte{}))
}

func ollamaUsageProvider(id int64) *ExecutionProvider {
	return &ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: id, Name: fmt.Sprintf("ollama-%d", id), Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
			Credentials: map[string]any{"base_url": "https://ollama.com", "api_key": fmt.Sprintf("key-%d", id)},
			Extra:       map[string]any{}, Status: billing.StatusActive, Schedulable: true, Concurrency: 1,
		},
	}
}

func ollamaCloudRawChatCompletionsTestProvider() *ExecutionProvider {
	return &ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 143,
			Name:     "DeepSeek Ollama",
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				"api_key":  "sk-test",
				"base_url": "https://ollama.com",
			},
			Extra: map[string]any{
				providercore.ExtraKeyTextRouteMode: string(providercore.TextRouteModeForceChatCompletions),
			},
		},
	}
}

func rawChatCompletionsTestProvider() *ExecutionProvider {
	return &ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: 101,
			Name:        "raw-openai-apikey",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Concurrency: 1,
			Credentials: map[string]any{
				"api_key":  "sk-test",
				"base_url": "http://upstream.example",
			},
		},
	}
}
