package provider

import (
	"net/http"
	"strings"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Codex metadata isolation follows upstream credentials, so two local rows for
// the same account can share a namespace. A ChatGPT account ID takes priority,
// with an optional user ID separating members of one account; a stored
// fingerprint seed supplies identity when account hints are absent.
//
// Setup Token records can finally use a digest of their access token. An
// unseeded OAuth record returns an empty namespace, leaving callers to their
// existing session isolation. Request fingerprint IDs read the inbound session
// before rewriting, and account headers use the same record's FedRAMP flag.

// CodexIdentityNamespace chooses the credential namespace used to isolate Codex
// metadata and upstream sessions. Nil records and providers outside OpenAI
// OAuth-like authentication return an empty string.
//
// Account and user hints take priority over a stored fingerprint seed, and
// Setup Token credentials supply the final digest-based choice. Local database
// IDs do not enter this namespace, so matching credential sources keep the same
// identity across different rows while team members with distinct user IDs
// remain separate.
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

// CodexFingerprintIDsFromRequest derives one attempt's IDs from the record's
// fingerprint mode and inbound session headers. Read it before outbound header
// rewriting; nil records, disabled modes, or unusable fingerprint configuration
// return nil.
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

// SetChatGPTAccountHeaders writes ChatGPT account and FedRAMP headers for
// OpenAI OAuth-like authentication. Nil headers and unsupported records are
// left untouched; the platform helper uses the record's account ID and FedRAMP
// flag.
func SetChatGPTAccountHeaders(headers http.Header, value *providercore.Record) {
	if headers == nil || value == nil || !value.IsOpenAIOAuthLike() {
		return
	}
	openai.SetChatGPTAccountHeaders(headers, value.GetChatGPTAccountID(), value.IsChatGPTAccountFedRAMP())
}
