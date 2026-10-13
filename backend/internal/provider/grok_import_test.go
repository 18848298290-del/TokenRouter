package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol/grok"
)

// grokSSOPanicClient injects an SSO exchange panic into worker recovery tests.
type grokSSOPanicClient struct{}

// TestGrokSSOImportExpiryUsesTokenExpiryWithoutRefreshToken supplies expiring
// token information with no renewable token. It asserts new provider expiry
// equals token expiry and automatic pause is enabled.
func TestGrokSSOImportExpiryUsesTokenExpiryWithoutRefreshToken(t *testing.T) {
	// Import expiry selection consumes epoch seconds from token information.
	tokenExpiry := time.Now().Add(6 * time.Hour).Unix()
	expiresAt, autoPause := GrokSSOImportExpiry(nil, nil, &GrokTokenInfo{
		ExpiresAt: tokenExpiry,
	})

	require.NotNil(t, expiresAt)
	require.Equal(t, tokenExpiry, *expiresAt)
	require.NotNil(t, autoPause)
	require.True(t, *autoPause)
}

// TestGrokSSOImportExpiryUsesEarlierRequestedExpiryWithoutRefreshToken requests
// an earlier expiry and disabled automatic pause for token-only information. It
// asserts the earlier deadline wins and token-only handling forces pause.
func TestGrokSSOImportExpiryUsesEarlierRequestedExpiryWithoutRefreshToken(t *testing.T) {
	// Both deadlines use epoch seconds so the earlier requested time can be
	// compared directly with the token information's credential expiry.
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

// TestGrokSSOImportExpiryPreservesRequestSettingsWithRefreshToken supplies
// renewable credentials beside requested expiry and pause settings. It asserts
// the exact request pointers are returned rather than newly constructed values.
func TestGrokSSOImportExpiryPreservesRequestSettingsWithRefreshToken(t *testing.T) {
	// Renewable-credential handling accepts the same epoch-seconds input format
	// as token-only expiry selection, even when returning the supplied pointers.
	requestedExpiry := time.Now().Add(2 * time.Hour).Unix()
	requestedAutoPause := false
	expiresAt, autoPause := GrokSSOImportExpiry(&requestedExpiry, &requestedAutoPause, &GrokTokenInfo{
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(6 * time.Hour).Unix(),
	})

	require.Same(t, &requestedExpiry, expiresAt)
	require.Same(t, &requestedAutoPause, autoPause)
}

// TestGrokSSOImportCredentialsPreservesRequestedBaseURL combines exchanged
// access text with custom endpoint and header operations in a separate request
// map. It asserts those final fields and that the request's base URL remains
// unchanged.
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

// TestGrokSSOImportCredentialsDefaultsToOfficialBaseURL supplies an exchanged
// default endpoint with absent and whitespace-only requested endpoints. It
// asserts both retain the built endpoint and the second keeps its exchanged
// access token.
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

// ExchangeCode is an unused interface stub returning no response and no error.
// The panic scenario reaches only ConvertSSOToBuild, so this method supplies
// interface shape for that fixture.
func (grokSSOPanicClient) ExchangeCode(context.Context, string, string, string, string, string) (*grok.TokenResponse, error) {
	return nil, nil
}

// RefreshToken is an unused renewal stub returning no response and no error.
// The worker panic test reaches the SSO converter, leaving this interface
// method outside its observed path.
func (grokSSOPanicClient) RefreshToken(context.Context, string, string, string) (*grok.TokenResponse, error) {
	return nil, nil
}

// LoginWithPassword is an unused password stub returning no response and no
// error. The fixture implements it for client compatibility while the worker
// scenario reaches direct SSO conversion.
func (grokSSOPanicClient) LoginWithPassword(context.Context, string, string, string) (*grok.PasswordLoginResult, error) {
	return nil, nil
}

// ConvertSSOToBuild panics with supplied SSO text to model a dependency
// exposing sensitive panic data. The worker wrapper catches it and the test
// checks its public item error.
func (grokSSOPanicClient) ConvertSSOToBuild(_ context.Context, ssoToken, _ string) (*grok.TokenResponse, error) {
	panic(ssoToken)
}

// TestGrokSSOImportWorkerRecoversPanicWithoutExposingToken makes the SSO
// dependency panic with sensitive text during one indexed worker operation. It
// asserts a failed item keeps the supplied index and a generic error without
// that token.
func TestGrokSSOImportWorkerRecoversPanicWithoutExposingToken(t *testing.T) {
	const sensitiveToken = "sensitive-sso-token"
	oauthService := NewGrokAuthorization(grokSSOPanicClient{}, GrokAuthorizationOptions{})
	oauthService.Start()
	// The synchronous SSO panic unwinds its activity registration before this
	// fixture stops. Discard the shutdown result under a background context
	// after cleanup joins its session loop.
	defer func() { _ = oauthService.StopContext(context.Background()) }()
	h := &GrokProviderImport{Authorization: oauthService, Options: GrokProviderImportOptions{LogError: func(string, ...any) {}}}

	// A worker panic becomes a failed item without disclosing the token in its response.
	result := h.safeCreateProviderFromSSOToken(context.Background(), GrokSSOToOAuthRequest{}, sensitiveToken, 2, 3)
	require.False(t, result.created)
	require.Equal(t, 2, result.item.Index)
	require.Equal(t, "internal worker panic", result.item.Error)
	require.NotContains(t, result.item.Error, sensitiveToken)
}
