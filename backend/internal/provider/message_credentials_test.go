package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Messages credential selection combines provider type with the availability of
// an injected token source. The tests inspect exact authentication labels,
// stored-token behavior, source call counts, input identity, and returned
// failures across OAuth, Setup Token, API Key, service-account, and Bedrock
// branches.

// TestMessageCredentialsPreserveStoredTypes resolves credentials with no
// injected source across the supported provider branches. It checks exact token
// bytes, authentication labels, and missing-token or unsupported-type errors,
// including empty Bedrock credentials.
func TestMessageCredentialsPreserveStoredTypes(t *testing.T) {
	tests := []struct {
		// An empty errorText selects successful resolution assertions. Failure
		// cases keep token and auth empty while checking the returned message;
		// platform and kind select the credential branch for either outcome.
		// name: test case label; platform: upstream platform identifier.
		// kind: provider authentication type; token: expected credential value.
		// auth: expected authentication label; errorText: expected failure message.
		name, platform, kind, token, auth, errorText string
		credentials                                  map[string]any // stored credential test input
	}{
		{name: "api key preserves whitespace", platform: capability.PlatformAnthropic, kind: capability.ProviderTypeAPIKey, credentials: map[string]any{"api_key": " key "}, token: " key ", auth: "apikey"},
		{name: "missing api key", platform: capability.PlatformAnthropic, kind: capability.ProviderTypeAPIKey, errorText: "api_key not found in credentials"},
		{name: "oauth fallback", platform: capability.PlatformAnthropic, kind: capability.ProviderTypeOAuth, credentials: map[string]any{"access_token": "stored"}, token: "stored", auth: "oauth"},
		{name: "setup does not refresh", platform: capability.PlatformAnthropic, kind: capability.ProviderTypeSetupToken, credentials: map[string]any{"access_token": "setup"}, token: "setup", auth: "oauth"},
		{name: "grok stored", platform: capability.PlatformGrok, kind: capability.ProviderTypeOAuth, credentials: map[string]any{"access_token": "grok"}, token: "grok", auth: "oauth"},
		{name: "grok missing", platform: capability.PlatformGrok, kind: capability.ProviderTypeOAuth, errorText: "grok access_token not found in credentials"},
		{name: "bedrock separate signing", platform: capability.PlatformAnthropic, kind: capability.ProviderTypeBedrock, auth: "bedrock"},
		{name: "vertex source missing", platform: capability.PlatformAnthropic, kind: capability.ProviderTypeServiceAccount, errorText: "claude token provider not configured"},
		{name: "foreign service account", platform: capability.PlatformGemini, kind: capability.ProviderTypeServiceAccount, errorText: "unsupported service account platform: gemini"},
		{name: "unknown type", platform: capability.PlatformAnthropic, kind: "unknown", errorText: "unsupported provider type: unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var source *MessageCredentialSource
			token, auth, err := source.Resolve(context.Background(), &Record{Platform: tt.platform, Type: tt.kind, Credentials: tt.credentials})
			if tt.errorText != "" {
				require.EqualError(t, err, tt.errorText)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.token, token)
			require.Equal(t, tt.auth, auth)
		})
	}
}

// TestMessageCredentialsReuseConfiguredSourceOnlyForOriginalBranches installs a
// source for Anthropic OAuth, service accounts, and Setup Tokens. The first two
// call it with the original record, Setup Tokens use stored credentials, and a
// source failure is returned.
func TestMessageCredentialsReuseConfiguredSourceOnlyForOriginalBranches(t *testing.T) {
	for _, kind := range []string{capability.ProviderTypeOAuth, capability.ProviderTypeServiceAccount, capability.ProviderTypeSetupToken} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			value := &Record{ID: 8, Platform: capability.PlatformAnthropic, Type: kind, Credentials: map[string]any{"access_token": "stored"}}
			source := &MessageCredentialSource{Claude: func(ctx context.Context, read *Record) (string, error) {
				calls++
				require.Same(t, value, read)
				require.NoError(t, ctx.Err())
				return "loaded", nil
			}}
			token, auth, err := source.Resolve(context.Background(), value)
			require.NoError(t, err)
			if kind == capability.ProviderTypeSetupToken {
				require.Zero(t, calls)
				require.Equal(t, "stored", token)
			} else {
				require.Equal(t, 1, calls)
				require.Equal(t, "loaded", token)
			}
			want := "oauth"
			if kind == capability.ProviderTypeServiceAccount {
				want = "service_account"
			}
			require.Equal(t, want, auth)
		})
	}
	sentinel := errors.New("source failed")
	source := &MessageCredentialSource{Claude: func(ctx context.Context, _ *Record) (string, error) { return "", sentinel }}
	token, auth, err := source.Resolve(context.Background(), &Record{Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth})
	require.ErrorIs(t, err, sentinel)
	require.Empty(t, token)
	require.Empty(t, auth)
}
