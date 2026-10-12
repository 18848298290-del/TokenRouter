package provider

import "maps"

var (
	// SensitiveCredentialKeys lists fields removed from credentials returned to the UI.
	// DTO redaction and update merging share this list; extend it when introducing new secret fields.
	SensitiveCredentialKeys = []string{
		// OAuth
		"access_token", "refresh_token", "id_token", "agent_private_key",
		// Qoder COSY credentials.
		"pat", "security_oauth_token", "machine_token",
		// API keys and New API user-wallet query credentials.
		"api_key", "session_key", "cookie", "new_api_user_access_token",
		// Temporary Grok Web SSO and passwords must not be stored or returned after Build OAuth exchange.
		"password", "sso_token", "sso", "sso-rw", "clearTextPassword",
		// Cloud credentials.
		"aws_secret_access_key", "aws_session_token",
		"service_account_json", "service_account", "private_key",
	}

	sensitiveCredentialKeySet = func() map[string]struct{} {
		m := make(map[string]struct{}, len(SensitiveCredentialKeys))
		for _, k := range SensitiveCredentialKeys {
			m[k] = struct{}{}
		}
		return m
	}()
)

// IsSensitiveCredentialKey reports whether a credential key is sensitive.
func IsSensitiveCredentialKey(key string) bool {
	_, ok := sensitiveCredentialKeySet[key]
	return ok
}

// MergePreservingSensitiveCreds shallow-copies incoming and fills omitted secrets from existing.
// This retains tokens omitted by the UI when submitting a redacted credential object.
// Non-secret fields come from incoming; a supplied secret key replaces the stored value, including with an empty value.
func MergePreservingSensitiveCreds(existing, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(incoming)+len(SensitiveCredentialKeys))
	maps.Copy(out, incoming)
	for _, key := range SensitiveCredentialKeys {
		if _, hasIncoming := incoming[key]; hasIncoming {
			continue
		}
		if existingVal, ok := existing[key]; ok {
			out[key] = existingVal
		}
	}
	return out
}

// SanitizeStoredCredentials removes temporary SSO, password, and cookie fields before storage.
// Cookies are removed for all platforms, including bulk updates with an empty platform.
func SanitizeStoredCredentials(platform string, creds map[string]any) map[string]any {
	if creds == nil {
		return nil
	}
	_ = platform
	for _, key := range []string{
		"password", "sso_token", "sso", "sso-rw", "clearTextPassword", "cookie",
	} {
		delete(creds, key)
	}
	return creds
}
