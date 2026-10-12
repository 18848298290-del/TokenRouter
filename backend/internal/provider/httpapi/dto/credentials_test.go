package dto

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Known sensitive credential spellings must disappear from administrator output
// even when their values are empty. These tests cover nil map handling,
// presence rules and retained source values in credentials.go, using permitted
// metadata alongside tokens to distinguish response fields.

// TestRedactCredentials_NilInput checks absent credential input through the
// redaction helper. Both output maps remain nil so response encoding can retain
// omission instead of inventing empty configuration objects.
func TestRedactCredentials_NilInput(t *testing.T) {
	out, status := RedactCredentials(nil)
	require.Nil(t, out)
	require.Nil(t, status)
}

// TestRedactCredentials_StripsSensitiveKeysAndReportsStatus supplies secret
// tokens, keys and permitted configuration together. Secret values disappear
// from output, configured flags are true and ordinary configuration remains
// visible without secret-status flags.
func TestRedactCredentials_StripsSensitiveKeysAndReportsStatus(t *testing.T) {
	in := map[string]any{
		"refresh_token":             "rt-secret",
		"access_token":              "at-secret",
		"api_key":                   "sk-secret",
		"new_api_user_access_token": "new-api-user-secret",
		"aws_secret_access_key":     "aws-secret",
		"service_account_json":      map[string]any{"private_key": "..."},
		"private_key":               "raw-key",
		"agent_private_key":         "agent-key-secret",
		// Non-sensitive values remain visible.
		"base_url":      "https://api.example.com",
		"model_mapping": map[string]any{"foo": "bar"},
		"project_id":    "proj-1",
		"expires_at":    int64(123456),
	}

	out, status := RedactCredentials(in)

	require.NotContains(t, out, "refresh_token")
	require.NotContains(t, out, "access_token")
	require.NotContains(t, out, "api_key")
	require.NotContains(t, out, "new_api_user_access_token")
	require.NotContains(t, out, "aws_secret_access_key")
	require.NotContains(t, out, "service_account_json")
	require.NotContains(t, out, "private_key")
	require.NotContains(t, out, "agent_private_key")

	require.Equal(t, "https://api.example.com", out["base_url"])
	require.Equal(t, map[string]any{"foo": "bar"}, out["model_mapping"])
	require.Equal(t, "proj-1", out["project_id"])
	require.Equal(t, int64(123456), out["expires_at"])

	require.True(t, status["has_refresh_token"])
	require.True(t, status["has_access_token"])
	require.True(t, status["has_api_key"])
	require.True(t, status["has_new_api_user_access_token"])
	require.True(t, status["has_aws_secret_access_key"])
	require.True(t, status["has_service_account_json"])
	require.True(t, status["has_private_key"])
	require.True(t, status["has_agent_private_key"])

	// Presence flags are reserved for sensitive keys.
	require.NotContains(t, status, "has_base_url")
	require.NotContains(t, status, "has_project_id")
}

// TestRedactCredentials_EmptyValuesNotMarkedPresent supplies empty, nil and
// false sensitive values alongside an ID token. Every secret key disappears,
// while only the configured ID token receives a true presence flag.
func TestRedactCredentials_EmptyValuesNotMarkedPresent(t *testing.T) {
	in := map[string]any{
		"refresh_token": "",
		"access_token":  nil,
		"api_key":       false,
		"id_token":      "actual-id",
	}
	out, status := RedactCredentials(in)
	require.Empty(t, out, "敏感键即使为空也不应出现在 redacted output")
	require.False(t, status["has_refresh_token"])
	require.False(t, status["has_access_token"])
	require.False(t, status["has_api_key"])
	require.True(t, status["has_id_token"])
}

// TestRedactCredentials_DoesNotMutateInput redacts a map containing secret and
// ordinary scalar values. The caller's source retains both entries after the
// helper returns its response objects.
func TestRedactCredentials_DoesNotMutateInput(t *testing.T) {
	in := map[string]any{
		"refresh_token": "secret",
		"base_url":      "x",
	}
	_, _ = RedactCredentials(in)
	require.Equal(t, "secret", in["refresh_token"], "原始 map 不应被修改")
	require.Equal(t, "x", in["base_url"])
}

// TestRedactCredentials_AllKnownSensitiveKeys fills each listed token and key
// spelling with a configured value. The output is empty and each listed
// sensitive key has its configured-presence flag.
func TestRedactCredentials_AllKnownSensitiveKeys(t *testing.T) {
	keys := []string{
		"access_token", "refresh_token", "id_token",
		"pat", "security_oauth_token", "machine_token",
		"api_key", "session_key", "cookie", "new_api_user_access_token",
		"aws_secret_access_key", "aws_session_token",
		"service_account_json", "service_account", "private_key",
		"agent_private_key",
	}
	in := make(map[string]any, len(keys))
	for _, k := range keys {
		in[k] = "filled"
	}
	out, status := RedactCredentials(in)
	require.Empty(t, out)
	for _, k := range keys {
		require.True(t, status["has_"+k], "key %s 应在 status 中标记为已配置", k)
	}
}
