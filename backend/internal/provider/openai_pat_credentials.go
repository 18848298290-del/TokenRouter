package provider

import "strings"

// A PAT can retain stale OAuth refresh and expiry fields when imported into an
// existing credential map. Normalization removes those fields after merging
// and marks both accepted authentication-mode keys as PAT, while preserving
// account metadata and unrelated configuration entries.
//
// Classification can come from returned token metadata, the stored provider or
// either authentication-mode key in the assembled map. Any recognized PAT
// source requests cleanup, even when another source omits its mode. Refresh
// token, ID token, client ID and OAuth expiry fields are removed together, so
// later request handling sees PAT mode paired with its access token and
// retained account configuration.

// OAuth-only keys are removed from PAT updates after credential merging so
// earlier refresh state cannot make the token source treat this PAT as renewable.
var openAIPersonalAccessTokenOAuthCredentialKeys = [...]string{
	"refresh_token",
	"id_token",
	"expires_at",
	"expires_in",
	"client_id",
}

// NormalizeOpenAIPersonalAccessTokenCredentials removes OAuth-only keys and
// installs PAT mode and Bearer type in the supplied map. Nil or unclassified
// input is returned unchanged, and the returned map remains the same mutable
// object.
//
// Any recognized PAT source among tokenInfo, provider or either map mode key
// requests normalization. Removing refresh, ID-token, client-ID and expiry
// state after merging prevents stored OAuth fields from accompanying the new
// PAT mode, while unrelated account and configuration entries remain present.
// Callers own synchronization and any later publication or persistence of this
// map.
func NormalizeOpenAIPersonalAccessTokenCredentials(provider *Record, tokenInfo *OpenAITokenInfo, credentials map[string]any) map[string]any {
	if credentials == nil || !isOpenAIPersonalAccessTokenCredentialSet(provider, tokenInfo, credentials) {
		return credentials
	}

	// Classification can come from the stored provider after merging omitted
	// fields, so remove stale refresh state before publishing the PAT credential map.
	for _, key := range openAIPersonalAccessTokenOAuthCredentialKeys {
		delete(credentials, key)
	}
	credentials[OpenAIAuthModeCredentialKey] = OpenAIAuthModePersonalAccessToken
	credentials[OpenAIAuthModeLegacyCredentialKey] = "personal_access_token"
	credentials["token_type"] = "Bearer"
	return credentials
}

// isOpenAIPersonalAccessTokenCredentialSet reports PAT classification from
// token metadata, the provider or either credential mode key. Any recognized
// source is sufficient, and absent sources are skipped; string mode values use
// the shared PAT spelling rules.
func isOpenAIPersonalAccessTokenCredentialSet(provider *Record, tokenInfo *OpenAITokenInfo, credentials map[string]any) bool {
	if tokenInfo != nil && IsOpenAIPersonalAccessTokenAuthMode(tokenInfo.AuthMode) {
		return true
	}
	if provider != nil && provider.IsOpenAIPersonalAccessToken() {
		return true
	}
	return IsOpenAIPersonalAccessTokenAuthMode(openAICredentialString(credentials[OpenAIAuthModeCredentialKey])) ||
		IsOpenAIPersonalAccessTokenAuthMode(openAICredentialString(credentials[OpenAIAuthModeLegacyCredentialKey]))
}

// openAICredentialString returns trimmed string input for authentication-mode
// comparison, or an empty string for every other type. This keeps absent or
// mistyped map values from becoming mode names through formatting.
func openAICredentialString(value any) string {
	if v, ok := value.(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
