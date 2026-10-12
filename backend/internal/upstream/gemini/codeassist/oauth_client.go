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

type OAuthClient struct {
	TokenURL string
	Config   func() OAuthConfig
}

// TokenResponse is a Google OAuth token response.
type TokenResponse = wire.TokenResponse

// NewOAuthClient reads configuration through config on each exchange or refresh.
func NewOAuthClient(config func() OAuthConfig) *OAuthClient {
	return &OAuthClient{TokenURL: TokenURL, Config: config}
}

func (c *OAuthClient) ExchangeCode(ctx context.Context, oauthType, code, codeVerifier, redirectURI, proxyURL string) (*TokenResponse, error) {
	client, err := CreateOAuthReqClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}

	// Code Assist and Google One exchanges use the built-in Gemini CLI credentials.
	// AI Studio uses configured credentials or the defaults from EffectiveOAuthConfig.
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

func (c *OAuthClient) RefreshToken(ctx context.Context, oauthType, refreshToken, proxyURL string) (*TokenResponse, error) {
	client, err := CreateOAuthReqClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}

	oauthCfgInput := c.Config()
	if oauthType == "code_assist" || oauthType == "google_one" {
		// Code Assist and Google One refreshes use the built-in Gemini CLI credentials.
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

func CreateOAuthReqClient(proxyURL string) (*req.Client, error) {
	return httpclient.GetSharedReqClient(httpclient.ReqClientOptions{
		ProxyURL: proxyURL,
		Timeout:  60 * time.Second,
	})
}
