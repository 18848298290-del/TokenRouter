package provider

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

func TestCodexRequestBodyIdentityNamespaceIsStablePerOAuthProvider(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-codex","prompt_cache_key":"client-session","client_metadata":{"x-codex-installation-id":"client-installation","session_id":"client-session","thread_id":"client-thread","x-codex-window-id":"client-window","x-codex-turn-metadata":"{\"installation_id\":\"client-installation\",\"session_id\":\"client-session\",\"thread_id\":\"client-thread\",\"turn_id\":\"client-turn\",\"window_id\":\"client-window\"}"}}`)
	provider11 := &providercore.Record{LoadLocation: time.LoadLocation, ID: 11, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "chatgpt-provider-11"}}
	provider19 := &providercore.Record{LoadLocation: time.LoadLocation, ID: 19, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "chatgpt-provider-19"}}

	first, changed, err := openai.ApplyCodexProviderIdentityClientMetadataRaw(body, CodexIdentityNamespace(provider11), 77)
	require.NoError(t, err)
	require.True(t, changed)
	firstAgain, changed, err := openai.ApplyCodexProviderIdentityClientMetadataRaw(body, CodexIdentityNamespace(provider11), 77)
	require.NoError(t, err)
	require.True(t, changed)
	second, changed, err := openai.ApplyCodexProviderIdentityClientMetadataRaw(body, CodexIdentityNamespace(provider19), 77)
	require.NoError(t, err)
	require.True(t, changed)
	require.JSONEq(t, string(first), string(firstAgain))

	paths := []string{
		"prompt_cache_key",
		"client_metadata.x-codex-installation-id",
		"client_metadata.session_id",
		"client_metadata.thread_id",
		"client_metadata.x-codex-window-id",
	}
	for _, path := range paths {
		require.NotEqual(t, gjson.GetBytes(body, path).String(), gjson.GetBytes(first, path).String(), path)
		require.NotEqual(t, gjson.GetBytes(first, path).String(), gjson.GetBytes(second, path).String(), path)
	}
	require.Equal(t, gjson.GetBytes(first, "prompt_cache_key").String(), gjson.GetBytes(first, "client_metadata.session_id").String())

	var embeddedFirst map[string]any
	var embeddedSecond map[string]any
	require.NoError(t, json.Unmarshal([]byte(gjson.GetBytes(first, "client_metadata.x-codex-turn-metadata").String()), &embeddedFirst))
	require.NoError(t, json.Unmarshal([]byte(gjson.GetBytes(second, "client_metadata.x-codex-turn-metadata").String()), &embeddedSecond))
	for _, field := range []string{"installation_id", "session_id", "thread_id", "turn_id", "window_id"} {
		require.NotEqual(t, embeddedFirst[field], embeddedSecond[field], field)
	}
}

func TestCodexProviderIdentityNamespaceUsesStableCredentialSource(t *testing.T) {
	firstRow := &providercore.Record{LoadLocation: time.LoadLocation, ID: 8, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "shared-upstream-provider"}}
	secondRow := &providercore.Record{LoadLocation: time.LoadLocation, ID: 19, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "shared-upstream-provider"}}
	require.Equal(t, CodexIdentityNamespace(firstRow), CodexIdentityNamespace(secondRow))

	firstUser := &providercore.Record{LoadLocation: time.LoadLocation, ID: 20, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "team-provider", "chatgpt_user_id": "user-1"}}
	sameUser := &providercore.Record{LoadLocation: time.LoadLocation, ID: 21, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "team-provider", "chatgpt_user_id": "user-1"}}
	secondUser := &providercore.Record{LoadLocation: time.LoadLocation, ID: 22, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "team-provider", "chatgpt_user_id": "user-2"}}
	require.Equal(t, CodexIdentityNamespace(firstUser), CodexIdentityNamespace(sameUser))
	require.NotEqual(t, CodexIdentityNamespace(firstUser), CodexIdentityNamespace(secondUser))

	seed := "11111111-1111-4111-8111-111111111111"
	seeded := &providercore.Record{LoadLocation: time.LoadLocation, ID: 11, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Extra: map[string]any{providercore.CodexFingerprintSeedExtraKey: seed}}
	// Local row IDs can collide across deployments; use the seed for upstream identity.
	require.Equal(t, "seed:"+seed, CodexIdentityNamespace(seeded))

	require.Empty(t, CodexIdentityNamespace(&providercore.Record{LoadLocation: time.LoadLocation, ID: 11, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}))

	setupTokenA := &providercore.Record{LoadLocation: time.LoadLocation, ID: 30, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeSetupToken, Credentials: map[string]any{"access_token": "setup-token-a"}}
	setupTokenADuplicate := &providercore.Record{LoadLocation: time.LoadLocation, ID: 31, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeSetupToken, Credentials: map[string]any{"access_token": "setup-token-a"}}
	setupTokenB := &providercore.Record{LoadLocation: time.LoadLocation, ID: 32, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeSetupToken, Credentials: map[string]any{"access_token": "setup-token-b"}}
	setupNamespace := CodexIdentityNamespace(setupTokenA)
	require.NotEmpty(t, setupNamespace)
	require.NotContains(t, setupNamespace, "setup-token-a")
	require.Equal(t, setupNamespace, CodexIdentityNamespace(setupTokenADuplicate))
	require.NotEqual(t, setupNamespace, CodexIdentityNamespace(setupTokenB))
}

func TestSetOpenAIChatGPTAccountHeadersAddsFedRAMP(t *testing.T) {
	provider := &providercore.Record{
		LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
		Type: capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id":         "acct-fed",
			"chatgpt_account_is_fedramp": true,
		},
	}
	headers := make(http.Header)
	SetChatGPTAccountHeaders(headers, provider)

	require.Equal(t, "acct-fed", headers.Get("chatgpt-account-id"))
	require.Equal(t, "true", headers.Get("x-openai-fedramp"))
}
