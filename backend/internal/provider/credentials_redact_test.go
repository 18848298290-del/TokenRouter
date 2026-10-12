package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeStoredCredentials_StripsEphemeralSSOSecrets(t *testing.T) {
	creds := map[string]any{
		"access_token":      "at",
		"refresh_token":     "rt",
		"password":          "secret",
		"sso_token":         "sso",
		"sso":               "cookie-sso",
		"sso-rw":            "rw",
		"clearTextPassword": "plain",
		"cookie":            "jar",
		"base_url":          "https://api.x.ai",
	}
	out := SanitizeStoredCredentials(PlatformGrok, creds)
	require.Equal(t, "at", out["access_token"])
	require.Equal(t, "rt", out["refresh_token"])
	require.Equal(t, "https://api.x.ai", out["base_url"])
	require.NotContains(t, out, "password")
	require.NotContains(t, out, "sso_token")
	require.NotContains(t, out, "sso")
	require.NotContains(t, out, "sso-rw")
	require.NotContains(t, out, "clearTextPassword")
	require.NotContains(t, out, "cookie")
}

func TestSanitizeStoredCredentials_AlwaysStripsCookie(t *testing.T) {
	// Bulk updates may omit the platform, but cookies must still be removed before storage.
	for _, platform := range []string{PlatformOpenAI, PlatformGrok, ""} {
		creds := map[string]any{
			"cookie":   "session",
			"password": "x",
			"api_key":  "k",
		}
		out := SanitizeStoredCredentials(platform, creds)
		require.Equal(t, "k", out["api_key"], platform)
		require.NotContains(t, out, "password", platform)
		require.NotContains(t, out, "cookie", platform)
	}
}

func TestSanitizeStoredCredentials_NilSafe(t *testing.T) {
	require.Nil(t, SanitizeStoredCredentials(PlatformGrok, nil))
}

func TestMergePreservingSensitiveCreds_PreservesSensitiveWhenIncomingMissing(t *testing.T) {
	existing := map[string]any{
		"refresh_token":             "rt-old",
		"access_token":              "at-old",
		"api_key":                   "sk-old",
		"new_api_user_access_token": "wallet-old",
		"base_url":                  "https://old.example.com",
	}
	incoming := map[string]any{
		"base_url":      "https://new.example.com",
		"model_mapping": map[string]any{"foo": "bar"},
	}

	out := MergePreservingSensitiveCreds(existing, incoming)

	require.Equal(t, "rt-old", out["refresh_token"], "incoming 没传 refresh_token，应保留 existing")
	require.Equal(t, "at-old", out["access_token"])
	require.Equal(t, "sk-old", out["api_key"])
	require.Equal(t, "wallet-old", out["new_api_user_access_token"])
	require.Equal(t, "https://new.example.com", out["base_url"], "非敏感键由 incoming 决定")
	require.Equal(t, map[string]any{"foo": "bar"}, out["model_mapping"])
}

func TestMergePreservingSensitiveCreds_OverwritesWhenIncomingProvidesSensitive(t *testing.T) {
	existing := map[string]any{
		"refresh_token": "rt-old",
		"api_key":       "sk-old",
	}
	incoming := map[string]any{
		"refresh_token": "rt-new",
		// Omitting api_key retains its stored value.
	}
	out := MergePreservingSensitiveCreds(existing, incoming)
	require.Equal(t, "rt-new", out["refresh_token"], "incoming 显式传入应覆盖")
	require.Equal(t, "sk-old", out["api_key"], "incoming 没传应保留")
}

func TestMergePreservingSensitiveCreds_DoesNotMutateInputs(t *testing.T) {
	existing := map[string]any{"refresh_token": "rt"}
	incoming := map[string]any{"base_url": "x"}

	_ = MergePreservingSensitiveCreds(existing, incoming)

	require.Equal(t, "rt", existing["refresh_token"])
	require.NotContains(t, existing, "base_url")
	require.Equal(t, "x", incoming["base_url"])
	require.NotContains(t, incoming, "refresh_token")
}

func TestMergePreservingSensitiveCreds_NilInputs(t *testing.T) {
	out := MergePreservingSensitiveCreds(nil, map[string]any{"base_url": "x"})
	require.Equal(t, "x", out["base_url"])
	require.NotContains(t, out, "refresh_token")

	out2 := MergePreservingSensitiveCreds(map[string]any{"refresh_token": "rt"}, nil)
	require.Equal(t, "rt", out2["refresh_token"])
}

func TestMergePreservingSensitiveCreds_NonSensitiveDeletionAllowed(t *testing.T) {
	existing := map[string]any{
		"refresh_token": "rt",
		"base_url":      "https://old",
		"project_id":    "p1",
	}
	incoming := map[string]any{
		"base_url": "https://new",
		// Omitting project_id removes it because non-secret fields come from incoming.
	}
	out := MergePreservingSensitiveCreds(existing, incoming)
	require.Equal(t, "rt", out["refresh_token"], "敏感键保留")
	require.Equal(t, "https://new", out["base_url"])
	require.NotContains(t, out, "project_id", "非敏感键 incoming 不传 = 删除")
}

func TestIsSensitiveCredentialKey(t *testing.T) {
	require.True(t, IsSensitiveCredentialKey("refresh_token"))
	require.True(t, IsSensitiveCredentialKey("api_key"))
	require.True(t, IsSensitiveCredentialKey("private_key"))
	require.True(t, IsSensitiveCredentialKey("new_api_user_access_token"))
	require.False(t, IsSensitiveCredentialKey("base_url"))
	require.False(t, IsSensitiveCredentialKey(""))
	require.False(t, IsSensitiveCredentialKey("model_mapping"))
}
