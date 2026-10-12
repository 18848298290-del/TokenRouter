package provider

import "strings"

var openAIPersonalAccessTokenOAuthCredentialKeys = [...]string{
	"refresh_token",
	"id_token",
	"expires_at",
	"expires_in",
	"client_id",
}

// NormalizeOpenAIPersonalAccessTokenCredentials removes OAuth refresh fields and sets PAT authentication
// fields in place.
func NormalizeOpenAIPersonalAccessTokenCredentials(provider *Record, tokenInfo *OpenAITokenInfo, credentials map[string]any) map[string]any {
	if credentials == nil || !isOpenAIPersonalAccessTokenCredentialSet(provider, tokenInfo, credentials) {
		return credentials
	}

	for _, key := range openAIPersonalAccessTokenOAuthCredentialKeys {
		delete(credentials, key)
	}
	credentials[OpenAIAuthModeCredentialKey] = OpenAIAuthModePersonalAccessToken
	credentials[OpenAIAuthModeLegacyCredentialKey] = "personal_access_token"
	credentials["token_type"] = "Bearer"
	return credentials
}

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

func openAICredentialString(value any) string {
	if v, ok := value.(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
