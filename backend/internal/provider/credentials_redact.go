package provider

import "maps"

// Administrator credential objects omit secrets before reaching the UI, so
// submitting one back requires a distinction between an omitted secret and a
// supplied empty value. The shared key list identifies fields for redaction and
// update merging, while storage sanitization removes temporary login material
// regardless of platform.
//
// MergePreservingSensitiveCreds builds a new top-level map from incoming fields
// and inherits only omitted sensitive entries from existing. Its nested values
// remain shared. SanitizeStoredCredentials instead mutates its supplied map in
// place, removing password, SSO, and cookie keys after a credential exchange so
// those temporary values cannot reappear through a later omitted-secret merge.

var (
	// SensitiveCredentialKeys lists secrets omitted by UI DTOs and inherited
	// when redacted input leaves them absent. DTO membership is captured at
	// package initialization, while merge helpers iterate this slice; keep it
	// unchanged during request handling.
	SensitiveCredentialKeys = []string{
		// OAuth tokens and signing material are private authentication inputs.
		"access_token", "refresh_token", "id_token", "agent_private_key",
		// Qoder COSY credentials.
		"pat", "security_oauth_token", "machine_token",
		// API keys and New API user-wallet query credentials.
		"api_key", "session_key", "cookie", "new_api_user_access_token",
		// Login passwords and Web SSO fields are redacted even for older records.
		"password", "sso_token", "sso", "sso-rw", "clearTextPassword",
		// Cloud credentials.
		"aws_secret_access_key", "aws_session_token",
		"service_account_json", "service_account", "private_key",
	}

	// sensitiveCredentialKeySet captures SensitiveCredentialKeys during package
	// initialization for repeated membership reads. Runtime changes to the
	// exported slice do not rebuild this lookup.
	sensitiveCredentialKeySet = func() map[string]struct{} {
		m := make(map[string]struct{}, len(SensitiveCredentialKeys))
		for _, k := range SensitiveCredentialKeys {
			m[k] = struct{}{}
		}
		return m
	}()
)

// IsSensitiveCredentialKey reports membership in the startup-built sensitive
// credential key set.
func IsSensitiveCredentialKey(key string) bool {
	_, ok := sensitiveCredentialKeySet[key]
	return ok
}

// MergePreservingSensitiveCreds builds a new top-level credential object from
// incoming and inherits omitted sensitive values from existing. A supplied
// secret key wins even when empty or nil.
//
// Nonsensitive fields omitted from incoming disappear, which lets a redacted
// form replace its ordinary configuration fields. Both input maps remain
// unchanged, but their nested mutable values are shared with the returned map.
// Callers that need separate ownership copy those supported values with
// CloneValues before later mutation.
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

// SanitizeStoredCredentials removes temporary password, SSO, and cookie keys in
// place and returns the same map. Nil remains nil, and platform is ignored so
// bulk changes that omit platform still sanitize credentials.
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
