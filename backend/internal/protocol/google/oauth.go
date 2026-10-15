package google

// oauth.go defines Google OAuth token responses and the client credentials and
// scopes used for authorization.
// Google OAuth exchanges return credential lifetime and scopes separately from
// the client configuration used to request them. EffectiveOAuthConfig resolves
// blank credential pairs to the built-in CLI client and normalizes configured
// comma or space-separated scopes before constructing the authorization request.

// TokenResponse holds credentials and relative lifetime returned by Google OAuth.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`            // upstream access credential
	RefreshToken string `json:"refresh_token,omitempty"` // optional refresh credential
	TokenType    string `json:"token_type"`              // returned authorization scheme
	ExpiresIn    int64  `json:"expires_in"`              // seconds
	Scope        string `json:"scope,omitempty"`         // granted OAuth scopes
}

// OAuthConfig supplies client credentials and requested scopes to Google authorization.
type OAuthConfig struct {
	// ClientID and ClientSecret form a pair for EffectiveOAuthConfig. After
	// trimming, both empty select the built-in Gemini CLI client; supplying
	// exactly one produces an error instead of a partially configured client.
	ClientID     string // configured client identifier
	ClientSecret string // configured client credential
	// Scopes accepts comma or space-separated input. EffectiveOAuthConfig
	// normalizes it and chooses OAuth-type defaults when the normalized value is empty.
	Scopes string
}
