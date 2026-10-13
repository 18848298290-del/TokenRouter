package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/egress"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/ollama"
)

// Ollama Cloud compatibility applies to OpenAI API Key targets whose resolved
// upstream text protocol is Chat. Saved usage metadata or a recognized cloud
// base URL identifies those targets, and the same gate controls request,
// JSON-response, and SSE-line normalization.

// IsOllamaCloudRawChatCompletionsProvider recognizes OpenAI API Key targets
// using resolved Chat routing. Any known Ollama usage key is sufficient
// evidence; otherwise a string base_url credential must identify an Ollama
// Cloud host.
func IsOllamaCloudRawChatCompletionsProvider(provider *ExecutionProvider) bool {
	if provider == nil || provider.Record.Platform != capability.PlatformOpenAI || provider.Record.Type != capability.ProviderTypeAPIKey {
		return false
	}
	// Manual routing and probe results are stored separately; enable the bridge when their resolved protocol is Chat.
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

// ProviderHasOllamaCloudUsageExtra reports whether any session, auto-refresh,
// or snapshot key is present in Extra. Presence is sufficient regardless of
// its value, and nil targets or extras return false.
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

// ApplyOllamaCloudRawChatCompletionsRequest delegates request normalization
// for recognized Ollama Cloud Chat targets. Other targets and empty bodies
// pass through unchanged; the upstream normalizer controls malformed-body and
// update-error handling.
func ApplyOllamaCloudRawChatCompletionsRequest(provider *ExecutionProvider, body []byte) []byte {
	if !IsOllamaCloudRawChatCompletionsProvider(provider) || len(body) == 0 {
		return body
	}
	return ollama.NormalizeOllamaCloudChatCompletionsRequest(body)
}

// ApplyOllamaCloudRawChatCompletionsResponse delegates JSON-response
// normalization for recognized Ollama Cloud Chat targets. Other targets and
// empty bodies pass through unchanged, retaining their existing response
// representation.
func ApplyOllamaCloudRawChatCompletionsResponse(provider *ExecutionProvider, body []byte) []byte {
	if !IsOllamaCloudRawChatCompletionsProvider(provider) || len(body) == 0 {
		return body
	}
	return ollama.NormalizeOllamaCloudChatCompletionsResponseJSON(body)
}

// ApplyOllamaCloudRawChatCompletionsSSELine delegates stream-line
// normalization for recognized Ollama Cloud Chat targets. Other targets and
// empty lines pass through unchanged, allowing the output reader to retain
// non-Ollama framing.
func ApplyOllamaCloudRawChatCompletionsSSELine(provider *ExecutionProvider, line string) string {
	if !IsOllamaCloudRawChatCompletionsProvider(provider) || line == "" {
		return line
	}
	return ollama.NormalizeOllamaCloudChatCompletionsSSELine(line)
}

// OllamaCloudMaxTokensCap resolves the provider output cap through Ollama's
// numeric policy. Nil targets and absent or invalid values use its default;
// nonpositive configured caps disable clamping.
func OllamaCloudMaxTokensCap(provider *ExecutionProvider) int64 {
	if provider == nil {
		return ollama.MaxTokensCap(nil, false)
	}
	value, ok := provider.Record.Extra[ollama.MaxTokensCapExtraKey]
	return ollama.MaxTokensCap(value, ok)
}
