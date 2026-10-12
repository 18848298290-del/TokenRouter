package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol/grok"
)

type grokSSOPanicClient struct{}

func TestGrokSSOImportExpiryUsesTokenExpiryWithoutRefreshToken(t *testing.T) {
	tokenExpiry := time.Now().Add(6 * time.Hour).Unix()
	expiresAt, autoPause := GrokSSOImportExpiry(nil, nil, &GrokTokenInfo{
		ExpiresAt: tokenExpiry,
	})

	require.NotNil(t, expiresAt)
	require.Equal(t, tokenExpiry, *expiresAt)
	require.NotNil(t, autoPause)
	require.True(t, *autoPause)
}

func TestGrokSSOImportExpiryUsesEarlierRequestedExpiryWithoutRefreshToken(t *testing.T) {
	requestedExpiry := time.Now().Add(2 * time.Hour).Unix()
	tokenExpiry := time.Now().Add(6 * time.Hour).Unix()
	requestedAutoPause := false
	expiresAt, autoPause := GrokSSOImportExpiry(&requestedExpiry, &requestedAutoPause, &GrokTokenInfo{
		ExpiresAt: tokenExpiry,
	})

	require.NotNil(t, expiresAt)
	require.Equal(t, requestedExpiry, *expiresAt)
	require.NotNil(t, autoPause)
	require.True(t, *autoPause)
}

func TestGrokSSOImportExpiryPreservesRequestSettingsWithRefreshToken(t *testing.T) {
	requestedExpiry := time.Now().Add(2 * time.Hour).Unix()
	requestedAutoPause := false
	expiresAt, autoPause := GrokSSOImportExpiry(&requestedExpiry, &requestedAutoPause, &GrokTokenInfo{
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(6 * time.Hour).Unix(),
	})

	require.Same(t, &requestedExpiry, expiresAt)
	require.Same(t, &requestedAutoPause, autoPause)
}

func TestGrokSSOImportCredentialsPreservesRequestedBaseURL(t *testing.T) {
	built := map[string]any{
		"access_token": "at-1",
		"base_url":     "https://cli-chat-proxy.grok.com/v1",
	}
	reqCredentials := map[string]any{
		"base_url":                "https://relay.example.com/v1",
		"header_override_enabled": true,
		"header_overrides":        map[string]any{"x-relay-key": "k"},
	}

	credentials := GrokSSOImportCredentials(built, reqCredentials)

	// Use exchanged tokens while retaining the operator's custom endpoint.
	require.Equal(t, "at-1", credentials["access_token"])
	require.Equal(t, "https://relay.example.com/v1", credentials["base_url"])
	require.Equal(t, true, credentials["header_override_enabled"])
	require.Equal(t, map[string]any{"x-relay-key": "k"}, credentials["header_overrides"])
	// Workers share the request credentials, so merging must leave that map unchanged.
	require.Equal(t, "https://relay.example.com/v1", reqCredentials["base_url"])
}

func TestGrokSSOImportCredentialsDefaultsToOfficialBaseURL(t *testing.T) {
	built := map[string]any{
		"access_token": "at-1",
		"base_url":     "https://cli-chat-proxy.grok.com/v1",
	}

	credentials := GrokSSOImportCredentials(built, nil)
	require.Equal(t, "https://cli-chat-proxy.grok.com/v1", credentials["base_url"])

	credentials = GrokSSOImportCredentials(map[string]any{
		"access_token": "at-2",
		"base_url":     "https://cli-chat-proxy.grok.com/v1",
	}, map[string]any{"base_url": "   "})
	require.Equal(t, "https://cli-chat-proxy.grok.com/v1", credentials["base_url"])
	require.Equal(t, "at-2", credentials["access_token"])
}

func (grokSSOPanicClient) ExchangeCode(context.Context, string, string, string, string, string) (*grok.TokenResponse, error) {
	return nil, nil
}

func (grokSSOPanicClient) RefreshToken(context.Context, string, string, string) (*grok.TokenResponse, error) {
	return nil, nil
}

func (grokSSOPanicClient) LoginWithPassword(context.Context, string, string, string) (*grok.PasswordLoginResult, error) {
	return nil, nil
}

func (grokSSOPanicClient) ConvertSSOToBuild(_ context.Context, ssoToken, _ string) (*grok.TokenResponse, error) {
	panic(ssoToken)
}

func TestGrokSSOImportWorkerRecoversPanicWithoutExposingToken(t *testing.T) {
	const sensitiveToken = "sensitive-sso-token"
	oauthService := NewGrokAuthorization(grokSSOPanicClient{}, GrokAuthorizationOptions{})
	oauthService.Start()
	defer func() { _ = oauthService.StopContext(context.Background()) }()
	h := &GrokProviderImport{Authorization: oauthService, Options: GrokProviderImportOptions{LogError: func(string, ...any) {}}}

	// A worker panic becomes a failed item without disclosing the token in its response.
	result := h.safeCreateProviderFromSSOToken(context.Background(), GrokSSOToOAuthRequest{}, sensitiveToken, 2, 3)
	require.False(t, result.created)
	require.Equal(t, 2, result.item.Index)
	require.Equal(t, "internal worker panic", result.item.Error)
	require.NotContains(t, result.item.Error, sensitiveToken)
}
