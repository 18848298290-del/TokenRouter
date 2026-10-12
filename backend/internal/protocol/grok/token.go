package grok

// TokenResponse is an xAI OAuth token response.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// PasswordLoginResult contains temporary password-login credentials.
// Callers pass SSOToken to ConvertSSOToBuild and discard it after conversion.
type PasswordLoginResult struct {
	Email    string `json:"email,omitempty"`
	SSOToken string `json:"sso_token"`
}

// AuthorizationInput contains parsed manual OAuth callback values.
type AuthorizationInput struct {
	Code          string
	State         string
	RequiresState bool
}
