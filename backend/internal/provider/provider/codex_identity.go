package provider

import (
	"net/http"
	"strings"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// CodexIdentityNamespace derives an identity namespace from the current provider credentials.
func CodexIdentityNamespace(provider *providercore.Record) string {
	if provider == nil || !provider.IsOpenAIOAuthLike() {
		return ""
	}
	upstreamProviderID := strings.TrimSpace(provider.GetChatGPTAccountID())
	if upstreamProviderID != "" {
		return openai.CodexProviderNamespace(openai.ProviderIdentityInput{ChatGPTAccountID: upstreamProviderID, ChatGPTUserID: strings.TrimSpace(provider.GetCredential("chatgpt_user_id"))})
	}
	if seed, ok := providercore.CodexFingerprintSeed(provider.Extra); ok {
		return openai.CodexProviderNamespace(openai.ProviderIdentityInput{Seed: seed, HasSeed: true})
	}
	if provider.Type == capability.ProviderTypeSetupToken {
		return openai.CodexProviderNamespace(openai.ProviderIdentityInput{SetupToken: strings.TrimSpace(provider.GetOpenAIAccessToken())})
	}
	return ""
}

// CodexFingerprintIDsFromRequest derives fingerprint IDs from this attempt's client session headers.
func CodexFingerprintIDsFromRequest(value *providercore.Record, headers http.Header) *openai.FingerprintIDs {
	if value == nil {
		return nil
	}
	mode := value.GetCodexFingerprintMode()
	if mode == providercore.CodexFingerprintOff {
		return nil
	}
	session := ""
	if headers != nil {
		session = openai.ExtractClientSessionID(headers)
	}
	return CodexFingerprintIDs(value, session, mode)
}

// SetChatGPTAccountHeaders adds account headers for OAuth-like providers when headers is non-nil.
func SetChatGPTAccountHeaders(headers http.Header, value *providercore.Record) {
	if headers == nil || value == nil || !value.IsOpenAIOAuthLike() {
		return
	}
	openai.SetChatGPTAccountHeaders(headers, value.GetChatGPTAccountID(), value.IsChatGPTAccountFedRAMP())
}
