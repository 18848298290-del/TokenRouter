package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/egress"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/ollama"
)

func IsOllamaCloudRawChatCompletionsProvider(provider *ExecutionProvider) bool {
	if provider == nil || provider.Record.Platform != capability.PlatformOpenAI || provider.Record.Type != capability.ProviderTypeAPIKey {
		return false
	}
	// fork 将手动模式与探测状态分开存储；两者合并后的实际协议为 Chat 时才启用桥。
	if providercore.ResolveUpstreamTextProtocol(
		provider.Record.Extra,
		providercore.TextProtocolResponses,
	) != providercore.TextProtocolChatCompletions {
		return false
	}
	if ProviderHasOllamaCloudUsageExtra(provider) {
		return true
	}
	if provider.Record.Credentials == nil {
		return false
	}
	baseURL, _ := provider.Record.Credentials["base_url"].(string)
	return egress.IsOllamaCloudBaseURL(baseURL)
}

func ProviderHasOllamaCloudUsageExtra(provider *ExecutionProvider) bool {
	if provider == nil || provider.Record.Extra == nil {
		return false
	}
	for _, key := range []string{
		providercore.OllamaCloudUsageSessionExtraKey,
		providercore.OllamaCloudUsageAutoRefreshExtraKey,
		providercore.OllamaCloudUsageSnapshotExtraKey,
	} {
		if _, ok := provider.Record.Extra[key]; ok {
			return true
		}
	}
	return false
}

func ApplyOllamaCloudRawChatCompletionsRequest(provider *ExecutionProvider, body []byte) []byte {
	if !IsOllamaCloudRawChatCompletionsProvider(provider) || len(body) == 0 {
		return body
	}
	return ollama.NormalizeOllamaCloudChatCompletionsRequest(body)
}

func ApplyOllamaCloudRawChatCompletionsResponse(provider *ExecutionProvider, body []byte) []byte {
	if !IsOllamaCloudRawChatCompletionsProvider(provider) || len(body) == 0 {
		return body
	}
	return ollama.NormalizeOllamaCloudChatCompletionsResponseJSON(body)
}

func ApplyOllamaCloudRawChatCompletionsSSELine(provider *ExecutionProvider, line string) string {
	if !IsOllamaCloudRawChatCompletionsProvider(provider) || line == "" {
		return line
	}
	return ollama.NormalizeOllamaCloudChatCompletionsSSELine(line)
}

func OllamaCloudMaxTokensCap(provider *ExecutionProvider) int64 {
	if provider == nil {
		return ollama.MaxTokensCap(nil, false)
	}
	value, ok := provider.Record.Extra[ollama.MaxTokensCapExtraKey]
	return ollama.MaxTokensCap(value, ok)
}
