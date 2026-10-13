package codeassist

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/imroc/req/v3"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

// Code Assist and Google One token exchanges use the built-in Gemini CLI
// credentials even when configured credentials exist. AI Studio reads its
// configured client on each exchange or refresh, and the shared OAuth client
// supplies proxy routing and a sixty-second timeout.

// OAuthClient exchanges Google authorization codes and refresh tokens using
// dynamic configuration.
type OAuthClient struct {
	TokenURL string // Google token exchange endpoint
	// Config is required and called once per exchange or refresh. Its current
	// credentials are replaced for Code Assist and Google One before effective
	// configuration is resolved.
	Config func() OAuthConfig // current OAuth configuration callback
}

// TokenResponse represents Google access, refresh, expiry, and granted-scope
// observations.
type TokenResponse = wire.TokenResponse

// NewOAuthClient retains config and the fixed Google token URL for later
// requests. Callers supply a nonnil callback; each exchange or refresh reads
// current configuration through that callback.
func NewOAuthClient(config func() OAuthConfig) *OAuthClient {
	return &OAuthClient{TokenURL: TokenURL, Config: config}
}

// ExchangeCode exchanges the supplied code, verifier, and redirect URI for
// token fields. Code Assist and Google One replace configured credentials with
// the built-in client; HTTP, proxy, configuration, and decode errors return to
// the caller.
func (c *OAuthClient) ExchangeCode(ctx context.Context, oauthType, code, codeVerifier, redirectURI, proxyURL string) (*TokenResponse, error) {
	client, err := CreateOAuthReqClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}

	// Resolve each request from current configuration, then clear configured
	// credentials for the CLI variants so EffectiveOAuthConfig selects their
	// built-in OAuth identity.
	oauthCfgInput := c.Config()
	if oauthType == "code_assist" || oauthType == "google_one" {
		oauthCfgInput.ClientID = ""
		oauthCfgInput.ClientSecret = ""
	}

	oauthCfg, err := EffectiveOAuthConfig(oauthCfgInput, oauthType)
	if err != nil {
		return nil, err
	}

	formData := url.Values{}
	formData.Set("grant_type", "authorization_code")
	formData.Set("client_id", oauthCfg.ClientID)
	formData.Set("client_secret", oauthCfg.ClientSecret)
	formData.Set("code", code)
	formData.Set("code_verifier", codeVerifier)
	formData.Set("redirect_uri", redirectURI)

	var tokenResp TokenResponse
	resp, err := client.R().
		SetContext(ctx).
		SetFormDataFromValues(formData).
		SetSuccessResult(&tokenResp).
		Post(c.TokenURL)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if !resp.IsSuccessState() {
		return nil, fmt.Errorf("token exchange failed: status %d, body: %s", resp.StatusCode, SanitizeBodyForLogs(resp.String()))
	}
	return &tokenResp, nil
}

// RefreshToken exchanges refreshToken using current effective OAuth
// configuration. Code Assist and Google One select built-in credentials before
// resolving scopes; the returned fields are server observations and do not
// validate access-token presence here.
func (c *OAuthClient) RefreshToken(ctx context.Context, oauthType, refreshToken, proxyURL string) (*TokenResponse, error) {
	client, err := CreateOAuthReqClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}

	oauthCfgInput := c.Config()
	if oauthType == "code_assist" || oauthType == "google_one" {
		// Refresh with the same built-in client identity used by these CLI
		// authorization variants, even when the current application configuration
		// contains custom client credentials.
		oauthCfgInput.ClientID = ""
		oauthCfgInput.ClientSecret = ""
	}

	oauthCfg, err := EffectiveOAuthConfig(oauthCfgInput, oauthType)
	if err != nil {
		return nil, err
	}

	formData := url.Values{}
	formData.Set("grant_type", "refresh_token")
	formData.Set("refresh_token", refreshToken)
	formData.Set("client_id", oauthCfg.ClientID)
	formData.Set("client_secret", oauthCfg.ClientSecret)

	var tokenResp TokenResponse
	resp, err := client.R().
		SetContext(ctx).
		SetFormDataFromValues(formData).
		SetSuccessResult(&tokenResp).
		Post(c.TokenURL)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if !resp.IsSuccessState() {
		return nil, fmt.Errorf("token refresh failed: status %d, body: %s", resp.StatusCode, SanitizeBodyForLogs(resp.String()))
	}
	return &tokenResp, nil
}

// CreateOAuthReqClient selects the shared proxy-aware req client with a
// sixty-second timeout. Client construction errors return to token operations,
// and the caller uses its configured protocol negotiation.
func CreateOAuthReqClient(proxyURL string) (*req.Client, error) {
	return httpclient.GetSharedReqClient(httpclient.ReqClientOptions{
		ProxyURL: proxyURL,
		Timeout:  60 * time.Second,
	})
}
