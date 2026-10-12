package identity

import (
	"context"
)

type EmailOAuthOptions struct {
	Enabled             bool   `mapstructure:"enabled"`
	ClientID            string `mapstructure:"client_id"`
	ClientSecret        string `mapstructure:"client_secret"`
	AuthorizeURL        string `mapstructure:"authorize_url"`
	TokenURL            string `mapstructure:"token_url"`
	UserInfoURL         string `mapstructure:"userinfo_url"`
	EmailsURL           string `mapstructure:"emails_url"`
	Scopes              string `mapstructure:"scopes"`
	RedirectURL         string `mapstructure:"redirect_url"`
	FrontendRedirectURL string `mapstructure:"frontend_redirect_url"`
}
type EmailOAuthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope,omitempty"`
}

// EmailOAuthClient exchanges tokens and fetches verified email profiles.
// Provider implementations handle GitHub email lookup and Google profile validation.
type EmailOAuthClient interface {
	ExchangeCode(context.Context, EmailOAuthOptions, string) (*EmailOAuthTokenResponse, error)
	FetchProfile(context.Context, string, EmailOAuthOptions, *EmailOAuthTokenResponse) (*EmailOAuthProfile, error)
}
